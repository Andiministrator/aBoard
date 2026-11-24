package storage

import "encoding/json"

// CanonicalEmptyBoardState liefert das kanonische, leere v2-Board-State-JSON
// mit gegebener Board-ID und Titel — die EINZIGE serverseitige Quelle der
// Wahrheit für „leeres Default-Board" (ADR-0052, ADR-0054 Teil B).
//
// Struktur 1:1 wie `Model.createBoard(title)` im Frontend (src/model.js),
// nur mit fester id statt clientseitig erzeugter UUID. Bewusste, einzige
// Ausnahme von ADR-0044 (state_json sonst opak): genau dieses eine Seed wird
// serverseitig materialisiert — vom Default-Board-Seed (main.go,
// id="default") UND vom Personal-Board-Bootstrap (ADR-0066/#225, id=UUID).
// Beide Pfade teilen DIESE Funktion, damit es keinen Drift zwischen zwei
// handgepflegten Seed-Kopien gibt (ADR-0052-Hazard).
//
// Die Feld-Reihenfolge ist als Template festgeschrieben (nicht
// json.Marshal eines structs), weil `seed_test.go` Byte-Gleichheit gegen
// das kanonische Frontend-Board prüft; nur `id`/`title` werden über
// json.Marshal injektionssicher als JSON-String-Token eingesetzt.
func CanonicalEmptyBoardState(boardID, title string) []byte {
	idTok, _ := json.Marshal(boardID)
	titleTok, _ := json.Marshal(title)
	s := `{"version":"2","board":{"id":` + string(idTok) +
		`,"title":` + string(titleTok) +
		`,"nextDisplayId":1,"columnDefs":[` +
		`{"id":"col-backlog","title":"Backlog","color":null},` +
		`{"id":"col-ready","title":"Ready","color":"#E9C85D"},` +
		`{"id":"col-in-arbeit","title":"In Arbeit","color":"#ea580c"},` +
		`{"id":"col-erledigt","title":"Erledigt","color":"#63D2B0"}],` +
		`"sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[]}],` +
		`"templates":[],"background":null,"attachments":[],"awaitingUser":null}}`
	return []byte(s)
}
