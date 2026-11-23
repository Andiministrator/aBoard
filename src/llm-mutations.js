/* llm-mutations.js — Tool-Call → Karten-Mutation-Bridge (#300, ADR-0096 §3+§4, Etappe 5).
 *
 * __SERVER_BUILD__-gegated → im Slim/Claude-Code-Build per DCE eliminiert.
 *
 * Vertrag: nimmt validierte Tool-Call-Payloads vom Server (Etappe 4 emittiert
 * `event:tool-call data:{id,name,allowed:true,args:…}`) und faltet sie auf die
 * Karte. Server-Validierung (server/llm/tools.go ValidateMutation 5-Layer) ist
 * führend; client-seitig spiegeln wir die Blocklist als Defense-in-depth + er-
 * gänzen Pre-Apply-Validate gegen den AKTUELLEN Karten-Snapshot — der LLM hat
 * möglicherweise auf einem inzwischen veralteten Stand basiert (Karte wurde
 * verschoben, Feld vorher schon geändert, Spalte umbenannt …) — analog zum
 * Lost-Update-Guard #186.
 *
 * Public API:
 *   LLMMutations.MUTATION_TOOLS       — Set erlaubter Tool-Namen
 *   LLMMutations.BLOCKLIST            — Set verbotener Action-Namen
 *   LLMMutations.SET_FIELD_ALLOWED    — Set erlaubter set-field-Feldnamen
 *   LLMMutations.preview(call, card)  → {ok, summary, apply, args} | {ok:false, code, hint}
 *   LLMMutations.applyToCard(card, call) → boolean (mutiert card in-place)
 *
 * `apply(card)` mutiert die übergebene Karte in-place und gibt true zurück.
 * Der Aufrufer ist verantwortlich, danach State.set() zu triggern, damit der
 * Standard-Persist-Pfad (state:changed → debouncedPersist → Sync.persistGated
 * → diffToOps → Inbox) die Mutation als card.field/card.move/comment.add
 * persistiert.
 */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let LLMMutations = null;

if (IS_SERVER_BUILD) {
  const MUTATION_TOOLS = new Set([
    'set-field', 'move', 'add-comment', 'extend-description', 'add-label',
  ]);

  /* Defense-in-depth: server/llm/tools.go L1-Blocklist; client mirrored
     für den Fall, dass ein halluziniertes tool_use trotzdem durchrutscht
     (z. B. Provider-Bug oder Test-Mock). */
  const BLOCKLIST = new Set([
    'card.delete', 'card.transfer', 'sprint.delete',
    'board.field', 'attachment.delete',
  ]);

  /* set-field-Allowlist — MUSS mit server/llm/tools.go SetFieldAllowedFields
     deckungsgleich sein (F-414: Drift macht Server-erlaubte Felder client-seitig
     tot). Paritäts-Snapshot-Test: tests/llm-mutations.test.js. */
  const SET_FIELD_ALLOWED = new Set([
    'qa', 'critical', 'blocked', 'complexity', 'effort',
    'dueDate', 'startDate', 'dueTime', 'startTime', 'priority', 'status',
  ]);

  /* F-412: zulässige WERTE für die Felder mit fester Auswahl. Die Listen sind
     nicht erfunden, sondern aus den <option>-Elementen in src/modal.js
     abgelesen (complexity :2114-2115, priority :2079). Der Leerstring ist bei
     complexity eine echte Auswahl („unbekannt"); bei priority ist er kein
     UI-Wert, kommt aber im Bestand vor — ihn abzulehnen hiesse, einer Karte
     den Rückweg zu verbauen.

     `status` fehlt hier ABSICHTLICH: Das Feld existiert im Modell (Default
     'offen'), hat aber kein Bedienelement und damit keine belastbare
     Werteliste. Im Bestand stehen '', 'offen' und 'erledigt'. Eine erfundene
     Aufzählung würde entweder legitime Altwerte blockieren oder falsche
     durchlassen — deshalb für status nur die Typprüfung.

     MUSS mit setFieldEnums in server/llm/tools.go deckungsgleich bleiben. */
  const SET_FIELD_ENUMS = {
    complexity: ['', 'einfach', 'mittel', 'hoch'],
    priority:   ['', 'niedrig', 'mittel', 'hoch'],
  };

  /* Benennt den vorgefundenen Typ für die Fehlermeldung. Die geht an das
     Modell zurück; „bekam String" ist eine Auskunft, mit der es den nächsten
     Versuch korrigieren kann — „ungültiger Wert" nicht. */
  function _typName(v) {
    if (v === null) return 'null';
    if (Array.isArray(v)) return 'Liste';
    switch (typeof v) {
      case 'boolean':   return 'Boolean';
      case 'number':    return 'Zahl';
      case 'string':    return 'String';
      case 'undefined': return 'null';
      case 'object':    return 'Objekt';
      default:          return 'unbekannten Typ';
    }
  }

  /* Validiert den WERT einer set-field-Mutation gegen den Typ, den das Feld
     tatsächlich trägt (F-412). Rückgabe '' heisst: in Ordnung.
     Spiegelt pruefeSetFieldWert in server/llm/tools.go. */
  function _pruefeSetFieldWert(field, wert) {
    switch (field) {
      case 'qa': case 'critical': case 'blocked':
        if (typeof wert !== 'boolean') {
          return `${field} erwartet true oder false, bekam ${_typName(wert)}`;
        }
        return '';
      case 'effort':
        /* null bedeutet „nicht geschätzt" und ist ein legitimer Wunsch. */
        if (wert === null || wert === undefined) return '';
        if (typeof wert !== 'number' || !Number.isFinite(wert)) {
          return `effort erwartet eine Zahl (Stunden) oder null, bekam ${_typName(wert)}`;
        }
        if (wert < 0) return `effort erwartet eine Zahl >= 0, bekam ${wert}`;
        return '';
      case 'dueDate': case 'startDate': {
        if (wert === null || wert === undefined || wert === '') return '';
        if (typeof wert !== 'string') {
          return `${field} erwartet ein ISO-Datum YYYY-MM-DD oder null, bekam ${_typName(wert)}`;
        }
        /* Strikt: Format UND Kalendertreue. Ein reiner Regex liesse
           2026-02-31 durch; die Round-Trip-Prüfung über Date nicht. */
        if (!/^\d{4}-\d{2}-\d{2}$/.test(wert)) {
          return `${field} erwartet ein ISO-Datum YYYY-MM-DD, bekam "${wert}"`;
        }
        const d = new Date(`${wert}T00:00:00Z`);
        if (Number.isNaN(d.getTime()) || d.toISOString().slice(0, 10) !== wert) {
          return `${field} erwartet ein ISO-Datum YYYY-MM-DD, bekam "${wert}"`;
        }
        return '';
      }
      case 'dueTime': case 'startTime': {
        /* ADR-0138 §1: "HH:MM" (24 h) oder null/'' zum Löschen. */
        if (wert === null || wert === undefined || wert === '') return '';
        if (typeof wert !== 'string' || !/^([01]\d|2[0-3]):[0-5]\d$/.test(wert)) {
          return `${field} erwartet eine Uhrzeit HH:MM (24 h) oder null, bekam ${typeof wert === 'string' ? `"${wert}"` : _typName(wert)}`;
        }
        return '';
      }
      case 'complexity': case 'priority': case 'status': {
        if (typeof wert !== 'string') {
          return `${field} erwartet einen String, bekam ${_typName(wert)}`;
        }
        const erlaubt = SET_FIELD_ENUMS[field];
        if (!erlaubt) return ''; /* status: nur Typprüfung, siehe oben */
        if (erlaubt.includes(wert)) return '';
        return `${field} erwartet einen der Werte ${JSON.stringify(erlaubt)}, bekam "${wert}"`;
      }
      default:
        return '';
    }
  }

  /* F-411: Kanonische Darstellung des Ausgangswerts für den Basis-Vergleich.
     JSON, damit `false`, `0`, `''` und `null` unterscheidbar bleiben — ein
     String-Vergleich machte aus `0` und `''` dasselbe und liesse genau die
     Änderungen durch, die man nicht sieht.

     `basis: null` heisst bewusst „kein Lost-Update möglich": Die
     Append-Operationen (add-comment, extend-description, add-label) hängen
     etwas an, statt etwas zu ersetzen. Sie sind kommutativ — zwei parallele
     Kommentare überschreiben einander nicht. */
  function _basisSchluessel(wert) {
    return JSON.stringify(wert === undefined ? null : wert);
  }

  /* extend-description Append-Cap — spiegelt server/llm/tools.go L3 (10 KB). */
  const EXTEND_DESC_MAX_BYTES = 10 * 1024;

  function _uuid() {
    const g = (typeof globalThis !== 'undefined') ? globalThis : {};
    if (g.crypto && typeof g.crypto.randomUUID === 'function') return g.crypto.randomUUID();
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, c => {
      const r = Math.floor(Math.random() * 16);
      const v = c === 'x' ? r : (r & 0x3) | 0x8;
      return v.toString(16);
    });
  }

  function _truncate(s, max) {
    if (typeof s !== 'string') return '';
    return s.length > max ? s.slice(0, max) + '…' : s;
  }

  /**
   * Validiert einen Tool-Call gegen die aktuelle Karte und liefert eine
   * Diff-Vorschau + apply-Funktion. Liefert {ok:false, code, hint} bei
   * jedem Fehlerfall — die Vier-Codes sind UI-lesbar:
   *   'blocked'         — Aktion auf Client-Blocklist (Defense-in-depth)
   *   'unknown-tool'    — Name nicht in MUTATION_TOOLS
   *   'card-stale'      — Karte fehlt / id fehlt (Lost-Update-Guard)
   *   'invalid-value'   — set-field-Wert hat den falschen Typ oder liegt
   *                       ausserhalb der erlaubten Enum-Werte (F-412).
   *                       Hat mit 'card-stale' NICHTS zu tun — die beiden
   *                       standen bis F-644 in einer Zeile, weil der neue
   *                       Code an die bestehende Beschreibung angehaengt
   *                       statt eingeordnet wurde.
   *   'invalid-args'    — Args-Struktur fehlt/falsch
   *   'forbidden-field' — set-field-Feld nicht in SET_FIELD_ALLOWED
   *   'too-large'       — extend-description > 10 KB
   *   'unknown-column'  — move-Ziel nicht in board.columnDefs (F-410)
   *
   * @param validColumns {string[]|Set<string>=} — gültige Spalten-IDs (aus
   *   board.columnDefs). Leer/weggelassen → move-Spalten-Check übersprungen
   *   (Backward-Compat; der Aufrufer SOLL die echten Spalten mitgeben).
   */
  function preview(call, card, validColumns) {
    const _cols = validColumns instanceof Set
      ? validColumns
      : (Array.isArray(validColumns) && validColumns.length ? new Set(validColumns) : null);
    if (!call || typeof call !== 'object') {
      return { ok: false, code: 'invalid-args', hint: 'malformed tool-call payload' };
    }
    const name = call.name;
    if (BLOCKLIST.has(name)) {
      return { ok: false, code: 'blocked', hint: name };
    }
    if (!MUTATION_TOOLS.has(name)) {
      return { ok: false, code: 'unknown-tool', hint: String(name || '?') };
    }
    if (!card || typeof card !== 'object' || !card.id) {
      return { ok: false, code: 'card-stale', hint: '' };
    }
    const args = (call.args && typeof call.args === 'object') ? call.args : {};

    switch (name) {
      case 'set-field': {
        const field = args.field;
        if (!field || typeof field !== 'string') {
          return { ok: false, code: 'invalid-args', hint: 'field' };
        }
        if (!SET_FIELD_ALLOWED.has(field)) {
          return { ok: false, code: 'forbidden-field', hint: field };
        }
        const value = args.value;
        /* F-412: Der FELDNAME war geprüft, der WERT nicht. Ein Modell, das
           `set-field(complexity, "sehr hoch")` oder `set-field(qa, "ja")`
           vorschlägt, kam durch — und der Wert stand danach im Board, wo weder
           die UI noch die Velocity-Berechnung ihn deuten kann. Spiegelt
           `pruefeSetFieldWert` in server/llm/tools.go; die Paritäts-Prüfung liegt in
           tests/llm-mutations.test.js und braucht dafür den Export von
           SET_FIELD_ENUMS (F-612 — vorher war die Behauptung unbelegt und der
           Wert von aussen gar nicht prüfbar). */
        const wertGrund = _pruefeSetFieldWert(field, value);
        if (wertGrund) {
          return { ok: false, code: 'invalid-value', hint: wertGrund };
        }
        const from = card[field];
        return {
          ok: true,
          args: { field, value },
          /* F-411: `basis` ist der Zustand, GEGEN den dieser Vorschlag
             berechnet wurde. Der Aufrufer wertet preview() beim Apply-Klick
             erneut aus und vergleicht — weicht die Basis ab, hat inzwischen
             jemand anderes dasselbe Feld geändert, und ein blindes Apply wäre
             ein stilles Überschreiben. */
          basis: _basisSchluessel(from),
          summary: `${field}: ${_fmt(from)} → ${_fmt(value)}`,
          apply: (c) => { c[field] = value; return true; },
        };
      }

      case 'move': {
        const toColId = args.toColumnId || args.toColId;
        if (!toColId || typeof toColId !== 'string') {
          return { ok: false, code: 'invalid-args', hint: 'toColumnId' };
        }
        /* F-410 (ADR-0096 §3 Schritt 4 / §4 L3): Ziel-Spalte MUSS existieren —
           sonst setzt apply card.columnId auf einen Phantom-Wert und die Karte
           verschwindet aus allen gerenderten Spalten. */
        if (_cols && !_cols.has(toColId)) {
          return { ok: false, code: 'unknown-column', hint: toColId };
        }
        return {
          ok: true,
          args: { toColumnId: toColId },
          basis: _basisSchluessel(card.columnId),
          summary: `columnId: ${card.columnId || '–'} → ${toColId}`,
          /* Nur columnId mutieren — diffToOps erkennt Positionswechsel und
             emittiert ein sauberes card.move (CARD_NON_FIELD ⊃ {columnId}
             schließt eine direkte card.field-Emission aus). */
          apply: (c) => { c.columnId = toColId; return true; },
        };
      }

      case 'add-comment': {
        const text = typeof args.text === 'string' ? args.text : '';
        if (!text.trim()) {
          return { ok: false, code: 'invalid-args', hint: 'text' };
        }
        return {
          ok: true,
          args: { text },
          summary: _truncate(text, 120),
          apply: (c) => {
            if (!Array.isArray(c.comments)) c.comments = [];
            c.comments.push({
              id:      _uuid(),
              author:  'LLM',
              text,
              created: new Date().toISOString().slice(0, 10),
            });
            return true;
          },
        };
      }

      case 'extend-description': {
        const append = typeof args.appendMarkdown === 'string'
          ? args.appendMarkdown
          : (typeof args.append === 'string' ? args.append : '');
        if (!append) {
          return { ok: false, code: 'invalid-args', hint: 'appendMarkdown' };
        }
        let bytes;
        try { bytes = new TextEncoder().encode(append).byteLength; }
        catch { bytes = append.length; }
        if (bytes > EXTEND_DESC_MAX_BYTES) {
          return { ok: false, code: 'too-large', hint: String(bytes) };
        }
        return {
          ok: true,
          args: { appendMarkdown: append },
          summary: _truncate(append, 120),
          apply: (c) => {
            const cur = (typeof c.description === 'string') ? c.description : '';
            const sep = (cur && !cur.endsWith('\n')) ? '\n' : '';
            c.description = cur + sep + append;
            return true;
          },
        };
      }

      case 'add-label': {
        const label = typeof args.label === 'string' ? args.label.trim() : '';
        if (!label) {
          return { ok: false, code: 'invalid-args', hint: 'label' };
        }
        return {
          ok: true,
          args: { label },
          summary: `+ "${label}"`,
          apply: (c) => {
            if (!Array.isArray(c.labels)) c.labels = [];
            if (!c.labels.includes(label)) c.labels.push(label);
            return true;
          },
        };
      }
    }
    return { ok: false, code: 'unknown-tool', hint: String(name) };
  }

  function _fmt(v) {
    if (v == null || v === '') return '–';
    if (typeof v === 'string') return _truncate(v, 60);
    return String(v);
  }

  function applyToCard(card, call, validColumns) {
    const p = preview(call, card, validColumns);
    if (!p.ok) return false;
    return p.apply(card);
  }

  LLMMutations = {
    MUTATION_TOOLS,
    BLOCKLIST,
    SET_FIELD_ALLOWED, SET_FIELD_ENUMS,
    EXTEND_DESC_MAX_BYTES,
    preview,
    applyToCard,
  };
}

export { LLMMutations };
