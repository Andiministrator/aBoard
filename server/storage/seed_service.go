package storage

import "encoding/json"

// CanonicalServiceBoardState liefert das kanonische, leere v2-Board-State-JSON
// für ein Service-Board (kind:service, ADR-0092). Strukturell identisch zum
// Default-Board (CanonicalEmptyBoardState), aber mit:
//
//   - columnDefs aus ADR-0092 §3: inbox / in-bearbeitung / wartet / erledigt / spam
//   - genau einer Lane `__service__` mit kind="service" (kein __backlog__)
//
// Die Feld-Reihenfolge ist als Template festgeschrieben — Byte-Stabilität wird
// in seed_service_test.go gegen die ADR-0092-Spaltendefinition geprüft. Nur
// boardID und title fließen über json.Marshal als String-Token ein
// (Injection-Safe, analog CanonicalEmptyBoardState).
func CanonicalServiceBoardState(boardID, title string) []byte {
	idTok, _ := json.Marshal(boardID)
	titleTok, _ := json.Marshal(title)
	s := `{"version":"2","board":{"id":` + string(idTok) +
		`,"title":` + string(titleTok) +
		`,"nextDisplayId":1,"columnDefs":[` +
		`{"id":"col-inbox","title":"Inbox","color":null},` +
		`{"id":"col-in-bearbeitung","title":"In Bearbeitung","color":"#ea580c"},` +
		`{"id":"col-wartet","title":"Wartet","color":"#E9C85D"},` +
		`{"id":"col-erledigt","title":"Erledigt","color":"#63D2B0"},` +
		`{"id":"col-spam","title":"Spam","color":"#9ca3af"}],` +
		`"sprints":[{"id":"__service__","title":"Service","kind":"service","cards":[]}],` +
		`"templates":[],"background":null,"attachments":[],"awaitingUser":null}}`
	return []byte(s)
}

// ServiceBoardID ist die feste Board-ID des einen Service-Boards pro Server-
// Instanz (ADR-0092 §2). Konvention statt UUID, weil deterministischer
// Bootstrap-Pfad gewünscht ist.
const ServiceBoardID = "service"

// MailWorkerEmail ist die System-Identität, die der Mail-Ingest (#295) +
// SpamRepo verwendet, um Karten ins Service-Board zu schreiben. Bewusst eine
// nicht-routbare Domain — kein realer Forward-Auth-Login möglich.
const MailWorkerEmail = "aboard-mail-worker@system.invalid"
