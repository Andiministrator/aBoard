export const Model = (() => {
  function newId() { return crypto.randomUUID(); }
  function now()   { return new Date().toISOString().slice(0, 10); }

  function createCard(title, columnId = null, displayId = null) {
    return {
      id:          newId(),
      displayId,
      title,
      description: '',
      status:      'offen',
      priority:    'mittel',
      labels:      [],
      milestone:   null,
      session:     null,
      comments:    [],
      qa:          false,
      critical:    false,
      blocked:     false,
      owner:        '',
      /* #302: Zusätzlich Beteiligte neben dem Owner. Owner bleibt Pre-Card-Check-
         Spawn-Quelle (ADR-0043), Assignees sind nur sichtbar. Default []. */
      assignees:    [],
      /* #307/ADR-0102: Projekt-Zuordnung (server-weite Entity). Karte gehört zu
         maximal einem Projekt; Server bleibt opak (ADR-0044). null = kein Projekt. */
      project_id:   null,
      complexity:   '',
      effort:       null,
      agentContext: '',
      /* ADR-0032: Karten-Verweise auf andere Karten.
         Schema: { targetId: <uuid>, type: 'blocks'|'blocked-by'|'depends-on'|'required-for'|'related', created: <iso> } */
      links:        [],
      /* ADR-0033: Anhänge.
         Schema: { id, filename, mime, size, created, data? (Base64, nur Standalone) } */
      attachments:  [],
      /* Karte #62: Cover-Bild — referenziert einen Anhang per ID (Reference-Modus).
         Kein Base64 hier — das Bild liegt im Attachment-Objekt (Standalone: data-Feld,
         Projekt-Modus: FSA-Ordner). SVG-Anhänge sind als Cover ausgeschlossen (F-128). */
      coverAttachmentId: null,
      /* ADR-0034: Datumsfelder (ersetzt Verbot aus ADR-0008).
         Format: ISO 8601 YYYY-MM-DD oder null */
      dueDate:      null,
      startDate:    null,
      /* ADR-0138 §1: dueTime/startTime ("HH:MM") stehen bewusst NICHT hier.
         Sie sind optional, ein fehlendes Feld heisst „keine Uhrzeit“. Stünden
         sie in der Fabrik, wüchse der Feld-Massstab (src/feldmassstab.js,
         F-925): Jede Neuanlage eines älteren Schreibers (installierte
         aboard-op-Kopie, Konsumenten-Board, alter Tab) würde als „fehlt
         dauerhaft — dueTime, startTime“ gemeldet. */
      /* ADR-0041: Wiederkehrende Karten.
         Schema: { interval: 'daily'|'weekly'|'monthly', day?: number, lastSpawned: 'YYYY-MM-DD' } | null
         null = keine Wiederholung. Template-Karten bleiben im Sprint — Spawn-Instanzen haben kein recurrence-Feld. */
      recurrence:   null,
      /* Archiv-Flag: archivierte Karten sind nicht mehr in Spalten sichtbar,
         aber über den „Archiv anzeigen"-Toggle abrufbar (Karte #60) */
      archived:     false,
      /* ADR-0037: Aktivitäts-Log — wer hat wann was geändert.
         Schema: { id, author, action, field?, from?, to?, timestamp } */
      activity:     [],
      columnId,
      created:     now(),
      updated:     now(),
    };
  }

  /**
   * Erzeugt eine Sprint-Meta-Sonderkarte (ADR-0036).
   * Meta-Karten haben kind='sprint-meta' + metaType und feste Eigenschaften,
   * die nicht über das Modal editierbar sind.
   *
   * @param {'start'|'end'} metaType - Typ der Sonderkarte
   * @param {number} displayId       - Bereits vergebene displayId
   * @param {string} columnId        - Ziel-Spalte ('col-erledigt' oder 'col-backlog')
   * @param {boolean} qa             - QA-Flag (true für abgeschlossene Karten)
   */
  function createSprintMetaCard(metaType, displayId, columnId, qa) {
    /* F-1046 (Zusatzfall, 2026-09-28): Die Meta-Karte baut auf `createCard` auf,
       statt die Feldliste ein zweites Mal aufzuschreiben. Die eigene Liste hatte
       `assignees` (#302), `project_id` (ADR-0102) und `recurrence` (ADR-0041)
       verpasst — jede Erweiterung von `createCard` seit ADR-0041 ging an ihr
       vorbei. Der headless-Projektor meldete deshalb zwei per `migrateSprintMeta`
       angelegte Meta-Karten als unvollständige Neuanlage (F-925). Mit dem Aufbau
       auf `createCard` erbt die Meta-Karte jedes künftige Feld von selbst.
       `recurrence: null` heisst „keine Wiederholung" und wird für Meta-Karten
       ohnehin von der Normalisierung (ADR-0127) gesetzt — geerbt ist es also
       nur früher da, nicht anders. */
    return {
      ...createCard(metaType === 'start' ? '📋 Sprint-Start' : '🏁 Sprint-Abschluss',
        columnId, displayId),
      kind:        'sprint-meta',
      metaType,
      qa,
    };
  }

  /* Vergabe der nächsten displayId — inkrementiert board.nextDisplayId */
  function assignDisplayId(board) {
    if (!board.nextDisplayId) board.nextDisplayId = 1;
    return board.nextDisplayId++;
  }

  function createColumnDef(title) {
    return { id: newId(), title };
  }

  /* F-249/ADR-0052: kanonischer Standard-Spaltensatz für jedes frische/leere
     Board (Backlog → Ready → In Arbeit → Erledigt, CLAUDE.md-Workflow).
     Feste IDs — Code referenziert col-erledigt/col-backlog direkt (renderer.js,
     F-242). Farben gespiegelt vom Projekt-eigenen Board. Single Source of Truth;
     storage.js migrateDefaultColumns nutzt diese Funktion ebenfalls. */
  function createDefaultColumnDefs() {
    return [
      { id: 'col-backlog',   title: 'Backlog',   color: null      },
      { id: 'col-ready',     title: 'Ready',     color: '#E9C85D' },
      { id: 'col-in-arbeit', title: 'In Arbeit', color: '#ea580c' },
      { id: 'col-erledigt',  title: 'Erledigt',  color: '#63D2B0' },
    ];
  }

  /* ADR-0046: kind = 'sprint' (default) oder 'lane' — bestimmt Sprint-Mechanik (Meta-Karten, Velocity, /sprint-abschluss). */
  function createSprint(title, kind = 'sprint') {
    return { id: newId(), title, kind: kind === 'lane' ? 'lane' : 'sprint', cards: [] };
  }

  function createBacklogSprint() {
    return { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] };
  }

  /* Erzeugt ein Template-Skelett aus einer bestehenden Karte.
     Nur strukturrelevante Felder werden übernommen — keine UUIDs/Daten/Kommentare/Anhänge. */
  function createTemplate(fromCard = {}) {
    return {
      id:           newId(),
      isTemplate:   true,
      title:        fromCard.title        || '',
      description:  fromCard.description  || '',
      labels:       Array.isArray(fromCard.labels) ? [...fromCard.labels] : [],
      owner:        fromCard.owner        || '',
      complexity:   fromCard.complexity   ?? '',
      effort:       fromCard.effort       ?? null,
    };
  }

  function createBoard(title) {
    /* Karte #114: background + attachments für Board-Hintergrundbild (Reference-Modus) */
    /* Karte #65 (ADR-0040): templates — Pool für Karten-Skelette */
    /* Karte #115: awaitingUser — Claude wartet auf User-Antwort.
       Schema seit Karte #609 (ADR-0130 §7): { <Person>: { question, since, askedBy } } | null;
       geschrieben nur per board.awaiting (src/awaiting.js). */
    /* F-249/ADR-0052: frisches Board startet mit dem Standard-Spaltensatz,
       nicht spaltenlos (vorher columnDefs: []). */
    return { id: newId(), title, nextDisplayId: 1, columnDefs: createDefaultColumnDefs(), sprints: [createBacklogSprint()], templates: [], background: null, attachments: [], awaitingUser: null };
  }

  /* ADR-0117 §5 (Karte #346): Darf diese Swimlane archiviert werden?
     Als reine Funktion, weil die ADR einen DOPPELTEN Guard zusagt — UI und
     Event-Handler — und ein Guard, der nur behauptet ist, keiner ist. Der
     EventBus-Handler in `main.js` wird von keinem Test importiert; ohne diese
     Funktion waere die Zusage aus dem Repo nicht nachpruefbar (Sprint-56-
     Kritiker, F-656).

     `__backlog__` ist der Auffangort fuer Karten geloeschter Sprints —
     verborgen waere er eine Falle. `kind:'service'` ist Server-Wahrheit und
     schon heute von Drag-and-Drop und Reordering ausgenommen (ADR-0092). */
  function darfArchiviertWerden(sprint) {
    if (!sprint || typeof sprint !== 'object') return false;
    if (sprint.id === '__backlog__') return false;
    if (sprint.kind === 'service' || sprint.kind === 'backlog') return false;
    return true;
  }

  /* ADR-0117 (Karte #346): Swimlane an eine neue Position bringen — als reine
     Funktion, damit sie pruefbar ist. Der EventBus-Handler in `main.js` ist
     nicht exportiert; eine Referenz-Implementierung im Test wuerde nur sich
     selbst pruefen und bliebe gruen, wenn der Produktivpfad kaputtgeht.

     Warum ein Anker und nicht die Zahl: Beide Aufrufer (Tastatur in
     renderer.js, Maus in dragdrop.js) leiten ihre Zielposition aus der
     DOM-Reihenfolge ab. Sind archivierte Swimlanes ausgeblendet, ist das nicht
     die Reihenfolge in `board.sprints` — die Swimlane landet dann um so viele
     Plaetze daneben, wie ausgeblendete dazwischenliegen. Die id des Ziels ist
     dagegen unabhaengig davon, was gerade sichtbar ist.

     Richtung wie im bisherigen Verhalten: abwaerts hinter den Anker, aufwaerts
     davor. Bei vollstaendig sichtbarer Liste ist das Ergebnis identisch mit dem
     alten reinen Index-Splice.

     Mutiert `sprints` in place (der Aufrufer arbeitet ohnehin auf dem
     mutateBoard-Entwurf) und liefert true, wenn etwas bewegt wurde. */
  function verschiebeSwimlane(sprints, sprintId, { ankerSprintId = null, targetIndex = null } = {}) {
    if (!Array.isArray(sprints) || !sprintId || sprintId === '__backlog__') return false;
    const idx = sprints.findIndex(s => s?.id === sprintId);
    if (idx === -1) return false;
    /* ADR-0092/ADR-0117 §5: Service-Boards sind Server-Wahrheit und vom
       Reordering ausgenommen. Die Ausnahme lebte bisher allein darin, dass
       `renderSprintHeader` fuer `kind:'service'` vor dem Drag-Handle
       zurueckkehrt — also nur in der UI. Genau davor warnt dieselbe ADR zwei
       Absaetze weiter („ein Guard, der nur in der UI sitzt, ist keiner"): Das
       Service-Element wird trotzdem als `.swimlane` mit `dataset.sprintId`
       gerendert und kann damit Ziel eines Drops sein (Sprint-56-Kritiker,
       F-660). Hier ist der zweite Guard. */
    if (sprints[idx]?.kind === 'service') return false;

    if (ankerSprintId) {
      if (ankerSprintId === sprintId) return false;
      const ankerIdx = sprints.findIndex(s => s?.id === ankerSprintId);
      if (ankerIdx === -1) return false;
      const abwaerts = idx < ankerIdx;
      const [sp] = sprints.splice(idx, 1);
      const neuerAnkerIdx = sprints.findIndex(s => s?.id === ankerSprintId);
      if (neuerAnkerIdx === -1) { sprints.splice(idx, 0, sp); return false; }
      sprints.splice(abwaerts ? neuerAnkerIdx + 1 : neuerAnkerIdx, 0, sp);
      return true;
    }

    if (!Number.isInteger(targetIndex)) return false;
    const [sp] = sprints.splice(idx, 1);
    sprints.splice(Math.max(0, Math.min(targetIndex, sprints.length)), 0, sp);
    return true;
  }

  return { newId, now, createCard, createSprintMetaCard, createTemplate, assignDisplayId, createColumnDef, createDefaultColumnDefs, createSprint, createBacklogSprint, createBoard, verschiebeSwimlane, darfArchiviertWerden };
})();
