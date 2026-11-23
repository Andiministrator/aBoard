/* Gemeinsamer Mock für das Attachment-Backend des Storage-Plugins.
 *
 * WOZU: Seit Karte #450 (F-798) holen `renderer.js`, `modal.js`,
 * `main.js`, `configdialog.js` und `identity.js` ihre Attachment-Funktionen
 * über `StoragePlugin.getActivePlugin().attachments()` statt direkt aus
 * `attachments.js` — nur so greift im Server-Modus das REST-Backend.
 *
 * Für Tests hatte das eine Nebenwirkung, die einzeln nicht sichtbar ist:
 * Wer `attachments.js` mockt, um Renderer-Verhalten zu prüfen, mockt seither
 * ein Modul, das im server-Kontext gar nicht mehr befragt wird (dort liefert
 * `getActivePlugin()` das REST-Backend mit eigenen Implementierungen). Die
 * betroffenen Dateien blieben isoliert grün und fielen nur in der vollen
 * Kette um — das kostete beim ersten Auftreten Zeit, weil die Ursache
 * aussieht wie eine Test-Interferenz und keine ist.
 *
 * Dieser Helfer stellt den Zustand her, den der Direktimport früher
 * implizit hatte: Das Backend IST das (gemockte) `attachments.js`-Modul,
 * unabhängig vom `boardMode`. Wer das REST-Backend selbst prüfen will,
 * nutzt ihn nicht — dafür ist `tests/anhang-transportweg-450.test.js` da.
 *
 * Aufruf in der Testdatei (vi.mock wird gehoben, deshalb als Factory):
 *
 *   vi.mock('../src/storage-plugin.js', () => import('./helpers/attachment-backend-mock.js')
 *     .then(m => m.storagePluginMock()))
 */
export async function storagePluginMock(zusatz = {}) {
  const att = await import('../../src/attachments.js');
  const backend = {
    ...att,
    /* Auskünfte über den Speicherort (ADR-0033 §Nachtrag Sprint 61). Für den
       FSA-/Base64-Weg, den diese Tests spiegeln, ist beides `true`. */
    kartenAnhangImBoardState: () => true,
    boardAnhangImBoardState:  () => true,
    ...zusatz,
  };
  return {
    StoragePlugin: { getActivePlugin: () => ({ attachments: () => backend }) },
    createFsaAttachmentBackend: () => backend,
  };
}
