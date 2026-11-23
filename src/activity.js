/* Aktivitäts-Log-Modul — ADR-0037
   Erzeugt und verwaltet Einträge im `activity[]`-Array jeder Karte.

   Aufrufstellen annotieren Änderungen explizit — kein globaler Reflection-Hook.
   Sync-Polling (Sync.poll → onUpdate) ruft recordChange NICHT auf. */

export const Activity = (() => {

  /**
   * Erzeugt einen neuen Aktivitäts-Eintrag und hängt ihn an card.activity.
   *
   * @param {object} card    - Die zu mutierende Karte (wird in-place verändert)
   * @param {string} action  - "owner-changed" | "column-moved" | "field-updated"
   * @param {string} author  - Name des Nutzers / Modells (Config.get('userName') oder Modell-Name)
   * @param {object} [opts]  - Optionale Felder: { field, from, to }
   */
  function recordChange(card, action, author, opts = {}) {
    if (!card) return;
    if (!Array.isArray(card.activity)) card.activity = [];

    const entry = {
      id:        crypto.randomUUID(),
      author:    author || '',
      action,
      timestamp: new Date().toISOString(),
    };
    if (opts.field !== undefined) entry.field = opts.field;
    if (opts.from  !== undefined) entry.from  = String(opts.from  ?? '');
    if (opts.to    !== undefined) entry.to    = String(opts.to    ?? '');

    card.activity.push(entry);
  }

  /**
   * Vergleicht alte und neue Karte und erzeugt Einträge für alle getackten Felder.
   * Gibt die neuen Einträge zurück (für Tests), mutierende Wirkung auf newCard.
   *
   * Getrackte Felder (ADR-0037): owner, columnId, complexity, effort, qa, critical, blocked.
   *
   * @param {object} oldCard  - Alter Karten-Stand (aus dem State vor der Mutation)
   * @param {object} newCard  - Neuer Karten-Stand (wird in-place mit Einträgen ergänzt)
   * @param {string} author   - Author des Eintrags
   * @returns {Array}         - Erzeugte Einträge (zur Überprüfung in Tests)
   */
  function diffAndRecord(oldCard, newCard, author) {
    if (!oldCard || !newCard) return [];
    const created = [];

    /* Owner-Wechsel */
    if (oldCard.owner !== newCard.owner) {
      const entry = { id: crypto.randomUUID(), author: author || '', action: 'owner-changed',
                      from: String(oldCard.owner ?? ''), to: String(newCard.owner ?? ''),
                      timestamp: new Date().toISOString() };
      if (!Array.isArray(newCard.activity)) newCard.activity = [];
      newCard.activity.push(entry);
      created.push(entry);
    }

    /* #302: Assignees — Set-Diff (added/removed). Reihenfolge ist semantisch egal,
       daher kein Event bei reiner Umordnung. */
    {
      const oldA = Array.isArray(oldCard.assignees) ? oldCard.assignees : [];
      const newA = Array.isArray(newCard.assignees) ? newCard.assignees : [];
      const oldSet = new Set(oldA);
      const newSet = new Set(newA);
      const added   = newA.filter(x => !oldSet.has(x));
      const removed = oldA.filter(x => !newSet.has(x));
      if (added.length || removed.length) {
        const entry = { id: crypto.randomUUID(), author: author || '', action: 'assignees-changed',
                        added, removed, timestamp: new Date().toISOString() };
        if (!Array.isArray(newCard.activity)) newCard.activity = [];
        newCard.activity.push(entry);
        created.push(entry);
      }
    }

    /* #307/ADR-0102: Projekt-Wechsel — analog Owner mit from/to project_id. */
    if (oldCard.project_id !== newCard.project_id) {
      const entry = { id: crypto.randomUUID(), author: author || '', action: 'project-changed',
                      from: String(oldCard.project_id ?? ''), to: String(newCard.project_id ?? ''),
                      timestamp: new Date().toISOString() };
      if (!Array.isArray(newCard.activity)) newCard.activity = [];
      newCard.activity.push(entry);
      created.push(entry);
    }

    /* Spalten-Wechsel (columnId) */
    if (oldCard.columnId !== newCard.columnId) {
      const entry = { id: crypto.randomUUID(), author: author || '', action: 'column-moved',
                      from: String(oldCard.columnId ?? ''), to: String(newCard.columnId ?? ''),
                      timestamp: new Date().toISOString() };
      if (!Array.isArray(newCard.activity)) newCard.activity = [];
      newCard.activity.push(entry);
      created.push(entry);
    }

    /* Boolean-Flags */
    for (const flag of ['qa', 'critical', 'blocked']) {
      if (!!oldCard[flag] !== !!newCard[flag]) {
        const entry = { id: crypto.randomUUID(), author: author || '', action: 'field-updated',
                        field: flag,
                        from: String(!!oldCard[flag]), to: String(!!newCard[flag]),
                        timestamp: new Date().toISOString() };
        if (!Array.isArray(newCard.activity)) newCard.activity = [];
        newCard.activity.push(entry);
        created.push(entry);
      }
    }

    /* Skalare Felder */
    const scalarFields = [
      { key: 'complexity', stringify: v => String(v ?? '') },
      { key: 'effort',     stringify: v => String(v ?? '') },
    ];
    for (const { key, stringify } of scalarFields) {
      const oldVal = stringify(oldCard[key]);
      const newVal = stringify(newCard[key]);
      if (oldVal !== newVal) {
        const entry = { id: crypto.randomUUID(), author: author || '', action: 'field-updated',
                        field: key, from: oldVal, to: newVal,
                        timestamp: new Date().toISOString() };
        if (!Array.isArray(newCard.activity)) newCard.activity = [];
        newCard.activity.push(entry);
        created.push(entry);
      }
    }

    return created;
  }

  return { recordChange, diffAndRecord };
})();
