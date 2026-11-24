package api

// agent_werkzeug_katalog_lesen.go — Katalog-Einträge der Lese-Werkzeuge aus
// ADR-0139 Stufe 4a (Karte #755). Sie hängen sich beim Start an
// werkzeugKatalog an (init), damit agent_werkzeug_katalog_daten.go den
// Bestand unverändert führt.
//
// Keines dieser Werkzeuge ist für ein delegierendes Token (Open WebUI)
// freigegeben: Neue Werkzeuge gibt es für den Open-WebUI-Assistenten nur einzeln nach Absprache mit
// einem anderen Projekt und dem Maintainer (ADR-0139 §7). werkzeugDarf sperrt sie dort ebenso.

import (
	"encoding/json"
	"net/http"
)

var boardIDParameter = werkzeugParameter{Name: "boardId", Ort: "path", Pflicht: true,
	Beschreibung: "boardId aus dem Werkzeug boards (nicht der Name des Boards).", Schema: json.RawMessage(`{"type":"string"}`)}

var karteParameter = werkzeugParameter{Name: "karte", Ort: "path", Pflicht: true,
	Beschreibung: "Kartennummer (displayId) aus board_lesen, z. B. 42. Tragen mehrere Karten dieselbe Nummer, antwortet das Werkzeug mit 400 und nennt ihre ids — dann statt der Nummer die passende id angeben.",
	Schema:       json.RawMessage(`{"type":"string"}`)}

var katalogLesen4a = []werkzeugEintrag{
	{
		Name:            "karten_suchen",
		Methode:         http.MethodGet,
		Muster:          "/werkzeuge/karten",
		Zusammenfassung: "Karten über alle Boards suchen",
		Beschreibung:    "Sucht Karten, deren Titel, Beschreibung, Owner oder Labels den Suchbegriff enthalten (ohne Rücksicht auf Groß-/Kleinschreibung) — über alle Boards, die du lesen darfst, oder mit boardId in einem. Liefert je Treffer boardId, board, displayId, titel, spalte, swimlane, fundort und link. Erledigte und archivierte Karten nur auf Wunsch. Bei sehr vielen Treffern wird gekürzt (gekuerzt: true); dann eingrenzen.",
		Parameter: []werkzeugParameter{
			{Name: "q", Ort: "query", Pflicht: true, Beschreibung: "Suchbegriff, mindestens 2 Zeichen.", Schema: json.RawMessage(`{"type":"string"}`)},
			{Name: "boardId", Ort: "query", Pflicht: false, Beschreibung: "Optional: nur in diesem Board suchen (boardId aus boards).", Schema: json.RawMessage(`{"type":"string"}`)},
			{Name: "mitErledigten", Ort: "query", Pflicht: false, Beschreibung: "true, um auch Karten der Spalte „Erledigt“ zu finden. Standard: false.", Schema: json.RawMessage(`{"type":"boolean","default":false}`)},
			{Name: "mitArchivierten", Ort: "query", Pflicht: false, Beschreibung: "true, um auch archivierte Karten und Swimlanes zu durchsuchen. Standard: false.", Schema: json.RawMessage(`{"type":"boolean","default":false}`)},
		},
		AntwortText: "Treffer", Antwort: "SuchErgebnis", Stufe: stufeLesen, Zugaenge: zugangOpenAPI | zugangMCP,
		handler: func(a *BoardsAPI) http.HandlerFunc { return a.werkzeugKartenSuchen },
	},
	{
		Name:            "board_verlauf",
		Methode:         http.MethodGet,
		Muster:          "/werkzeuge/boards/{boardId}/verlauf",
		Zusammenfassung: "Jüngste Änderungen eines Boards lesen",
		Beschreibung:    "Liefert die jüngsten Einträge des Board-Protokolls (neueste zuerst): zeit, wer, für wen (bei Agenten), art (z. B. card.move), karte (Kartennummer), feld, vorher, nachher. Mit karte nur die Einträge einer Karte. Menschen erscheinen mit Namen; E-Mail-Adressen nur, wenn deine Person owner des Boards ist.",
		Parameter: []werkzeugParameter{
			boardIDParameter,
			{Name: "karte", Ort: "query", Pflicht: false, Beschreibung: "Optional: nur Einträge dieser Karte (Kartennummer aus board_lesen).", Schema: json.RawMessage(`{"type":"string"}`)},
			{Name: "anzahl", Ort: "query", Pflicht: false, Beschreibung: "Höchstzahl der Einträge, 1 bis 100. Standard: 30.", Schema: json.RawMessage(`{"type":"integer","minimum":1,"maximum":100,"default":30}`)},
		},
		AntwortText: "Verlauf", Antwort: "Verlauf", Stufe: stufeLesen, Zugaenge: zugangOpenAPI | zugangMCP,
		handler: func(a *BoardsAPI) http.HandlerFunc { return a.werkzeugBoardVerlauf },
	},
	{
		Name:            "mitglieder",
		Methode:         http.MethodGet,
		Muster:          "/werkzeuge/boards/{boardId}/mitglieder",
		Zusammenfassung: "Mitglieder eines Boards mit Rolle",
		Beschreibung:    "Nennt, wer am Board welche Rolle hat (owner, editor, viewer) — mit Namen, damit du Owner und Beteiligte einer Karte richtig setzen kannst. E-Mail-Adressen und Freigaben für ganze Domains nur, wenn deine Person owner des Boards ist.",
		Parameter:       []werkzeugParameter{boardIDParameter},
		AntwortText:     "Mitglieder", Antwort: "Mitglieder", Stufe: stufeLesen, Zugaenge: zugangOpenAPI | zugangMCP,
		handler: func(a *BoardsAPI) http.HandlerFunc { return a.werkzeugMitglieder },
	},
	{
		Name:            "anhang_lesen",
		Methode:         http.MethodGet,
		Muster:          "/werkzeuge/boards/{boardId}/karten/{karte}/anhaenge",
		Zusammenfassung: "Anhänge einer Karte lesen",
		Beschreibung:    "Listet die Anhänge einer Karte (id, dateiname, mime, groesse, angelegt). Mit anhang zusätzlich den Inhalt, wenn es ein Text-Anhang bis 32 KiB ist (Text, Markdown, JSON, CSV …); sonst ein hinweis. Dateien hochladen kann dieses Werkzeug nicht.",
		Parameter: []werkzeugParameter{boardIDParameter, karteParameter,
			{Name: "anhang", Ort: "query", Pflicht: false, Beschreibung: "Optional: id eines Anhangs aus der Liste, um seinen Inhalt zu lesen.", Schema: json.RawMessage(`{"type":"string"}`)},
		},
		AntwortText: "Anhänge", Antwort: "Anhaenge", Stufe: stufeLesen, Zugaenge: zugangOpenAPI | zugangMCP,
		handler: func(a *BoardsAPI) http.HandlerFunc { return a.werkzeugAnhangLesen },
	},
}

var schemasLesen4a = map[string]json.RawMessage{
	"SuchErgebnis": json.RawMessage(`{"type":"object","properties":{
		"sicherheitshinweis":{"type":"string"},"suche":{"type":"string"},
		"treffer":{"type":"array","items":{"type":"object","properties":{
			"boardId":{"type":"string"},"board":{"type":"string"},"displayId":{"type":"integer"},"titel":{"type":"string"},
			"spalte":{"type":"string"},"swimlane":{"type":"string"},"fundort":{"type":"string","enum":["titel","beschreibung","owner","label"]},
			"archiviert":{"type":"boolean"},"link":{"$ref":"#/components/schemas/Link"}}}},
		"anzahl":{"type":"integer"},"gekuerzt":{"type":"boolean"},"hinweis":{"type":"string"}}}`),
	"Verlauf": json.RawMessage(`{"type":"object","properties":{
		"sicherheitshinweis":{"type":"string"},"boardId":{"type":"string"},"board":{"type":"string"},
		"eintraege":{"type":"array","items":{"type":"object","properties":{
			"zeit":{"type":"string"},"wer":{"type":"string"},"fuer":{"type":"string"},"art":{"type":"string"},
			"karte":{"type":"integer"},"feld":{"type":"string"},"vorher":{"type":"string"},"nachher":{"type":"string"}}}},
		"gekuerzt":{"type":"boolean"}}}`),
	"Mitglieder": json.RawMessage(`{"type":"object","properties":{
		"sicherheitshinweis":{"type":"string"},"boardId":{"type":"string"},
		"mitglieder":{"type":"array","items":{"type":"object","properties":{
			"name":{"type":"string"},"rolle":{"type":"string","enum":["owner","editor","viewer"]},"email":{"type":"string"}}}},
		"hinweis":{"type":"string"}}}`),
	"Anhaenge": json.RawMessage(`{"type":"object","properties":{
		"sicherheitshinweis":{"type":"string"},"boardId":{"type":"string"},"displayId":{"type":"integer"},
		"anhaenge":{"type":"array","items":{"$ref":"#/components/schemas/Anhang"}},
		"anhang":{"$ref":"#/components/schemas/Anhang"},"inhalt":{"type":"string"},"hinweis":{"type":"string"},
		"link":{"$ref":"#/components/schemas/Link"}}}`),
	"Anhang": json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"string"},"dateiname":{"type":"string"},"mime":{"type":"string"},"groesse":{"type":"integer"},"angelegt":{"type":"string"}}}`),
}

func init() {
	werkzeugKatalog = append(werkzeugKatalog, katalogLesen4a...)
	for k, v := range schemasLesen4a {
		werkzeugSchemas[k] = v
	}
}
