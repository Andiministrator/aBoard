/* Karte #604, Kritiker-Befund K-1 (Sitzungs-QA 2026-09-30): „Ihre behalten“
   beim Feldkonflikt darf die eigene Fassung nie verlieren (ADR-0130 §2).
   Bis zur Nacharbeit lief das Kopieren als `navigator.clipboard?.writeText`
   in einem leeren catch: Ohne Zwischenablage meldete der Banner trotzdem
   „kopiert“, bei einem Fehler meldete er nichts — und der Banner mit dem
   Text war da schon zu. Rein und ohne Abhängigkeiten, damit testbar. */

/** Die eigene Fassung als ein Text (mehrere Felder durch Leerzeilen getrennt). */
export function eigeneFassungAlsText(feld) {
  return (feld || [])
    .map(a => (typeof a?.mein === 'string' ? a.mein : JSON.stringify(a?.mein ?? '')))
    .join('\n\n');
}

/**
 * Kopiert `text` in die Zwischenablage.
 * @returns {Promise<boolean>} true nur, wenn das Kopieren sicher gelang.
 */
export async function kopiereEigeneFassung(text, clipboard) {
  if (!clipboard || typeof clipboard.writeText !== 'function') return false;
  try {
    await clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
