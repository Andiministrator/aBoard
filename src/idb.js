/* Generischer IndexedDB-Key-Value-Helper für FileSystemHandle-Persistenz.
   Wird von storage.js (Save-File-Handles) und sync.js (Projekt-Verzeichnis-
   Handles) genutzt — beide Module teilen DB + Store und unterscheiden sich
   nur im KEY_PREFIX. (F-103, Sprint 17) */

const DB_NAME  = 'aboard-handles';
const DB_STORE = 'handles';
let dbPromise  = null;
/* F-3106 (Karte #1179): Chromium beantwortet das Öffnen manchmal nie (gesehen
   bei gespeicherten Handles, deren Rechte-Eintrag hängt). Ohne Frist hängt der
   ganze Start; danach gilt das Öffnen als gescheitert und der nächste Aufruf
   versucht es neu. */
export const IDB_FRIST_MS = 3000;

function openDB() {
  if (dbPromise) return dbPromise;
  /* `meine` zeigt nach der Zuweisung unten auf genau diese Promise — die
     Event-Handler vergleichen damit, ob der Modul-Cache noch ihre eigene
     Verbindung hält (V-B). */
  let meine;
  meine = dbPromise = new Promise((resolve, reject) => {
    let erledigt = false;
    const frist = setTimeout(() => {
      erledigt = true;
      if (dbPromise === meine) dbPromise = null;
      const e = new Error('IndexedDB antwortet nicht (Frist)');
      e.idbFrist = true;
      reject(e);
    }, IDB_FRIST_MS);
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = e => e.target.result.createObjectStore(DB_STORE);
    req.onsuccess  = e => {
      const db = e.target.result;
      clearTimeout(frist);
      /* Kam die Verbindung erst nach der Frist, wartet niemand mehr auf sie. */
      if (erledigt) { db.close(); return; }
      erledigt = true;
      /* F-138: dbPromise cached die offene Verbindung dauerhaft. Der Browser
         kann sie aber jederzeit von sich aus schließen — Quota-Eviction,
         oder ein anderer Tab/eine andere aboard.html-Instanz verlangt ein
         Schema-Upgrade (`onversionchange`). Vor diesem Fix hat nur der
         onerror-Zweig den Cache zurückgesetzt; onclose/onversionchange
         liefen ins Leere, und jeder weitere idbPut/idbGet/idbDelete griff
         danach auf eine tote Connection zu — Fehlschlag bis zum Tab-Reload.
         Beide Events invalidieren den Cache jetzt wie onerror, damit der
         nächste Aufruf openDB() eine frische Verbindung öffnet. */
      /* Sprint-51-Kritiker (V-B): NUR den eigenen Cache-Eintrag entwerten.
         Die erste Fassung setzte `dbPromise = null` unbedingt — feuerte der
         Handler einer alten Verbindung, nachdem `openDB()` bereits eine neue
         Promise gecacht hatte, flog die frische Verbindung aus dem Cache und
         wurde nie geschlossen (Connection-Leak; kein Korrektheitsfehler, aber
         vermeidbar). `meine` ist die Promise, zu der DIESE Verbindung gehört —
         sie wird unten nach dem Konstruktor gesetzt. */
      db.onclose = () => { if (dbPromise === meine) dbPromise = null; };
      db.onversionchange = () => {
        db.close();
        if (dbPromise === meine) dbPromise = null;
      };
      resolve(db);
    };
    req.onerror    = () => {
      clearTimeout(frist);
      erledigt = true;
      if (dbPromise === meine) dbPromise = null;
      reject(req.error);
    };
  });
  return dbPromise;
}

export async function idbPut(key, value) {
  try {
    const db = await openDB();
    const tx = db.transaction(DB_STORE, 'readwrite');
    tx.objectStore(DB_STORE).put(value, key);
  } catch {}
}

/* F-3106 Verify B1: `streng` unterscheidet „Eintrag fehlt“ (null) von „IndexedDB antwortet nicht“
   (wirft den Frist-Fehler weiter); ohne `streng` bleibt jeder Fehler wie bisher `null`. */
export async function idbGet(key, { streng = false } = {}) {
  try {
    const db = await openDB();
    return new Promise(resolve => {
      const req = db.transaction(DB_STORE, 'readonly').objectStore(DB_STORE).get(key);
      req.onsuccess = () => resolve(req.result ?? null);
      req.onerror   = () => resolve(null);
    });
  } catch (e) {
    if (streng && e?.idbFrist) throw e;
    return null;
  }
}

export async function idbDelete(key) {
  try {
    const db = await openDB();
    const tx = db.transaction(DB_STORE, 'readwrite');
    tx.objectStore(DB_STORE).delete(key);
  } catch {}
}

/* Liste aller Einträge deren Key mit `keyPrefix` beginnt.
   Liefert Array von { id, value } wobei id = key.slice(keyPrefix.length). */
export async function idbListWithPrefix(keyPrefix) {
  try {
    const db = await openDB();
    return new Promise(resolve => {
      const result = [];
      const cursorReq = db.transaction(DB_STORE, 'readonly').objectStore(DB_STORE).openCursor();
      cursorReq.onsuccess = e => {
        const cursor = e.target.result;
        if (!cursor) { resolve(result); return; }
        const key = String(cursor.key);
        if (key.startsWith(keyPrefix)) {
          result.push({ id: key.slice(keyPrefix.length), value: cursor.value });
        }
        cursor.continue();
      };
      cursorReq.onerror = () => resolve([]);
    });
  } catch { return []; }
}
