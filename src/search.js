/* Such-Modul — UI-only, wird nicht persistiert (analog filter.js) */

export const Search = (() => {
  let query = '';
  let _debounceTimer = null;
  const _listeners = [];

  /* Gibt die aktuelle Such-Query zurück */
  function getQuery() { return query; }

  /* Gibt true zurück wenn eine Suche aktiv ist */
  function isActive() { return query.length > 0; }

  /* Setzt die Query sofort (ohne Debounce) und ruft Listener auf */
  function _applyQuery(q) {
    query = q;
    _listeners.forEach(fn => fn(q));
  }

  /* Setzt die Query mit Debounce (~150 ms) */
  function setQuery(q) {
    clearTimeout(_debounceTimer);
    _debounceTimer = setTimeout(() => _applyQuery(q), 150);
  }

  /* Sofortiger Reset ohne Debounce */
  function clear() {
    clearTimeout(_debounceTimer);
    _applyQuery('');
  }

  /* Listener registrieren — wird bei jeder Query-Änderung aufgerufen */
  function onChange(fn) { _listeners.push(fn); }

  /* F-145: Gegenstück zu onChange. Heute registriert nur main.js genau einen
     Listener auf Modul-Ebene, der nie wieder abgemeldet werden muss — der
     fehlende Abmeldeweg war deshalb kein Leck, sondern eine halbe API: ein
     Modal oder Dialog, das sich später an die Suche hängt, hätte seinen
     Listener (samt Closure auf sein DOM) bis zum Reload stehen lassen müssen.
     Gibt true zurück, wenn tatsächlich etwas entfernt wurde. */
  function off(fn) {
    const i = _listeners.indexOf(fn);
    if (i === -1) return false;
    _listeners.splice(i, 1);
    return true;
  }

  /* Prüft ob eine Karte der aktuellen Query entspricht.
     Felder: title, description, comments[].text, labels[], owner.
     Case-insensitiv, Teilstring-Match. */
  function matches(card) {
    if (!query) return true;
    const q = query.toLowerCase();
    if ((card.title ?? '').toLowerCase().includes(q)) return true;
    if ((card.description ?? '').toLowerCase().includes(q)) return true;
    if (Array.isArray(card.comments)) {
      for (const c of card.comments) {
        if ((c.text ?? '').toLowerCase().includes(q)) return true;
      }
    }
    if (Array.isArray(card.labels)) {
      for (const l of card.labels) {
        if ((l ?? '').toLowerCase().includes(q)) return true;
      }
    }
    if ((card.owner ?? '').toLowerCase().includes(q)) return true;
    return false;
  }

  /* Zerlegt einen String in Textknoten-Segmente für Highlighting.
     Gibt ein Array von { text, highlight } zurück.
     Kein innerHTML — reines textNode-Splitting via createElement (ADR-0009). */
  function highlightSegments(text) {
    if (!query || !text) return [{ text: text ?? '', highlight: false }];
    const q = query.toLowerCase();
    const result = [];
    let remaining = text;
    let lowerRemaining = remaining.toLowerCase();
    let idx;
    while ((idx = lowerRemaining.indexOf(q)) !== -1) {
      if (idx > 0) result.push({ text: remaining.slice(0, idx), highlight: false });
      result.push({ text: remaining.slice(idx, idx + q.length), highlight: true });
      remaining = remaining.slice(idx + q.length);
      lowerRemaining = remaining.toLowerCase();
    }
    if (remaining.length > 0) result.push({ text: remaining, highlight: false });
    return result;
  }

  /* Baut einen DOM-Container mit highlight-Spans aus einem Text (ADR-0009).
     Gibt ein DocumentFragment zurück. */
  function buildHighlightedText(text) {
    const frag = document.createDocumentFragment();
    const segments = highlightSegments(text);
    for (const seg of segments) {
      if (seg.highlight) {
        const mark = document.createElement('mark');
        mark.className = 'search-hit';
        mark.textContent = seg.text;
        frag.appendChild(mark);
      } else {
        frag.appendChild(document.createTextNode(seg.text));
      }
    }
    return frag;
  }

  return { getQuery, isActive, setQuery, clear, onChange, off, matches, highlightSegments, buildHighlightedText };
})();
