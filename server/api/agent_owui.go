package api

// agent_owui.go — Open WebUI handelt im Namen der angemeldeten Person
// (ADR-0133 §4, Karte #669).
//
// Ein delegierendes Dienst-Token (agent_tokens.delegierend) berechtigt allein
// zu nichts. Jede Anfrage damit muss ein von Open WebUI signiertes Nutzer-JWT
// im Header X-OpenWebUI-User-Jwt tragen (HS256, iss=open-webui, exp, iat).
// Das gemeinsame Geheimnis liegt in ABOARD_OWUI_JWT_SECRET_FILE und ist nur
// OWUI und aBoard bekannt.
//
// Die Klartext-Kopfzeilen X-OpenWebUI-User-Name/-Id/-Email/-Role werden NIE
// ausgewertet: OWUI fällt bei einem Signierfehler still auf sie zurück, sie
// sind dann reine Behauptung (knowledge/topics/open-webui-integration.md §4).
// Die Middleware löscht sie, bevor ein Handler sie sehen könnte.
//
// Wirksame Rolle = ACL-Rolle der Person auf dem Board aus dem Pfad (inkl.
// Domain-Freigabe, ADR-0132), gekappt auf die Token-Rolle (höchstens editor).
// Die Rolle aus dem JWT (OWUI-Rolle admin/user) zählt nicht.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// OWUIJWTSecretFileEnv nennt die Datei mit dem gemeinsamen Geheimnis
// (FORWARD_USER_INFO_HEADER_JWT_SECRET in OWUI). Eine Datei, kein Wert in der
// Variable — sonst stünde das Geheimnis in jeder Prozessliste.
const OWUIJWTSecretFileEnv = "ABOARD_OWUI_JWT_SECRET_FILE"

// OWUIJWTMaxAgeEnv (Karte #730): Höchstalter eines OWUI-JWT in Sekunden, ab
// iat gerechnet. Open WebUI stellt das JWT EINMAL pro Chat-Anfrage aus und
// nutzt es für alle Werkzeugaufrufe dieses Zugs (v0.11.4, utils/tools.py,
// get_tools: Header im Closure). Ein Zug, der länger denkt als das Höchstalter,
// scheitert ab dann mit 401 — beim NVIDIA-Dreh 06.10. vier karte_anlegen.
// Wer längere Züge braucht, stellt BEIDE Seiten ein: in OWUI
// FORWARD_USER_INFO_HEADER_JWT_EXPIRES_SECONDS, hier dieselbe Zahl. exp gilt
// weiterhin; ein JWT allein reicht ohnehin nie (das delegierende Token gehört
// dazu). Standard 300 s wie bisher, harte Obergrenze owuiHoechstalterMax.
const OWUIJWTMaxAgeEnv = "ABOARD_OWUI_JWT_MAX_AGE_SECONDS"

// OWUIUserJWTHeader ist der Header, in dem OWUI das signierte JWT schickt.
const OWUIUserJWTHeader = "X-OpenWebUI-User-Jwt"

// owuiUserHeaderPraefix: alle Nutzer-Kopfzeilen von OWUI, kanonisch
// geschrieben (Go schreibt „X-OpenWebUI-User-Email“ als „X-Openwebui-User-Email“).
var owuiUserHeaderPraefix = http.CanonicalHeaderKey("X-OpenWebUI-User-")

const (
	// owuiIssuer: iss, den OWUI v0.11.4 setzt (utils/headers.py).
	owuiIssuer = "open-webui"
	// owuiHoechstalter: Standard-Höchstalter ab iat — OWUI stellt das JWT
	// ohne eigene Einstellung für 300 s aus. Älter wird es nicht angenommen,
	// auch wenn OWUI ein weiteres exp setzt, es sei denn, der Betreiber hebt
	// OWUIJWTMaxAgeEnv an (Karte #730).
	owuiHoechstalter = 300 * time.Second
	// owuiHoechstalterMin/Max: erlaubter Bereich für OWUIJWTMaxAgeEnv. Eine
	// Stunde deckt lange Denk-Züge; mehr hiesse, ein abgefangenes JWT bliebe
	// einen halben Arbeitstag lang brauchbar.
	owuiHoechstalterMin = 60 * time.Second
	owuiHoechstalterMax = 3600 * time.Second
	// owuiUhrToleranz: Abweichung der Uhren zwischen OWUI und aBoard.
	owuiUhrToleranz = 60 * time.Second
	// owuiGeheimnisMin: kürzer ist ein HS256-Geheimnis zu raten (RFC 7518
	// §3.2 verlangt mindestens die Länge des Hashes, 256 Bit).
	owuiGeheimnisMin = 32
	// owuiJWTMaxLaenge: ein OWUI-JWT ist wenige hundert Byte lang.
	owuiJWTMaxLaenge = 8 << 10
)

// ladeOWUIGeheimnis liest das Geheimnis aus der Datei in
// ABOARD_OWUI_JWT_SECRET_FILE. Nicht gesetzt: nil, nil — delegierende Tokens
// werden dann bei jeder Anfrage abgelehnt. Gesetzt, aber unlesbar oder zu
// kurz: Fehler, der Start bricht ab (wer die Variable setzt, will die
// Delegation; ein still abgeschalteter Weg fiele erst am ausbleibenden Chat auf).
// Nur ein abschliessender Zeilenumbruch wird entfernt — `echo geheim > datei`.
func ladeOWUIGeheimnis() ([]byte, error) {
	pfad := os.Getenv(OWUIJWTSecretFileEnv)
	if pfad == "" {
		return nil, nil
	}
	roh, err := os.ReadFile(pfad)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", OWUIJWTSecretFileEnv, err)
	}
	g := strings.TrimRight(string(roh), "\r\n")
	if len(g) < owuiGeheimnisMin {
		return nil, fmt.Errorf("%s: Geheimnis zu kurz (%d Zeichen, mindestens %d)", OWUIJWTSecretFileEnv, len(g), owuiGeheimnisMin)
	}
	return []byte(g), nil
}

// OWUIGeheimnisRechteWarnung (Kritiker Sprint 82 Runde 2, KO-2): Warnsatz,
// wenn die Datei aus ABOARD_OWUI_JWT_SECRET_FILE offener als 0640 ist — wie
// beim LLM-Serverschlüssel (llm.ServerschluesselRechteWarnung) nur eine
// Warnung, kein Startabbruch. Wer das Geheimnis liest, kann für jede Person
// ein gültiges OWUI-JWT signieren und mit dem delegierenden Token in ihrem
// Namen handeln. "" ohne Variable, bei unlesbarer Datei (das meldet
// ladeOWUIGeheimnis) und bei passenden Rechten.
func OWUIGeheimnisRechteWarnung() string {
	return llm.DateiRechteWarnung(OWUIJWTSecretFileEnv, os.Getenv(OWUIJWTSecretFileEnv), "das gemeinsame Geheimnis mit Open WebUI")
}

// ladeOWUIHoechstalter liest OWUIJWTMaxAgeEnv. Nicht gesetzt: Standard.
// Gesetzt, aber keine ganze Zahl im erlaubten Bereich: Fehler, der Start
// bricht ab — wer die Variable setzt, will längere Züge, und ein still
// verworfener Wert fiele erst am nächsten 401 im Dreh auf.
func ladeOWUIHoechstalter() (time.Duration, error) {
	roh := strings.TrimSpace(os.Getenv(OWUIJWTMaxAgeEnv))
	if roh == "" {
		return owuiHoechstalter, nil
	}
	// Bereich in Sekunden prüfen, BEVOR in eine Dauer umgerechnet wird — sonst
	// liefe eine riesige Zahl in time.Duration über (Kritiker #730, K-1).
	n, err := strconv.Atoi(roh)
	if err != nil || n < int(owuiHoechstalterMin.Seconds()) || n > int(owuiHoechstalterMax.Seconds()) {
		return 0, fmt.Errorf("%s: %q ist keine ganze Sekundenzahl von %d bis %d",
			OWUIJWTMaxAgeEnv, roh, int(owuiHoechstalterMin.Seconds()), int(owuiHoechstalterMax.Seconds()))
	}
	return time.Duration(n) * time.Second, nil
}

// MitOWUIDelegation schaltet den delegierenden Weg ein: Geheimnis aus
// ABOARD_OWUI_JWT_SECRET_FILE, Höchstalter aus ABOARD_OWUI_JWT_MAX_AGE_SECONDS,
// Rollen aus acl. Ohne Geheimnis bleibt der Weg zu (jedes delegierende Token
// bekommt 401).
func (z *AgentZugang) MitOWUIDelegation(acl owuiRollenLeser) error {
	g, err := ladeOWUIGeheimnis()
	if err != nil {
		return err
	}
	// Ohne Geheimnis ist die Delegation aus; dann bricht auch ein falscher
	// Höchstalter-Wert den Start nicht ab (Kritiker #730, K-2).
	alter := owuiHoechstalter
	if g != nil {
		if alter, err = ladeOWUIHoechstalter(); err != nil {
			return err
		}
	}
	z.owuiGeheimnis = g
	z.owuiACL = acl
	z.owuiHoechstalter = alter
	return nil
}

// OWUIHoechstalterHinweis: Startzeile, wenn das Höchstalter vom Standard
// abweicht — damit im Log steht, womit der Server läuft. "" beim Standard.
func OWUIHoechstalterHinweis() string {
	if os.Getenv(OWUIJWTSecretFileEnv) == "" {
		return "" // Delegation aus — das Höchstalter wirkt nicht (K-2)
	}
	d, err := ladeOWUIHoechstalter()
	if err != nil || d == owuiHoechstalter {
		return ""
	}
	return fmt.Sprintf("aboard-server: OWUI-JWT-Höchstalter %d s (%s) — in Open WebUI muss FORWARD_USER_INFO_HEADER_JWT_EXPIRES_SECONDS mindestens ebenso hoch stehen", int(d.Seconds()), OWUIJWTMaxAgeEnv)
}

// owuiRollenLeser ist der Teil von storage.AclRepo, den die Delegation
// braucht: die WIRKSAME Rolle einer E-Mail inklusive Domain-Freigabe.
type owuiRollenLeser interface {
	GetRole(ctx context.Context, boardID, email string) (string, bool, error)
}

var errOWUIJWT = errors.New("owui-jwt ungültig")

// pruefeOWUIJWT prüft mit dem Standard-Höchstalter (Tests, Werkzeuge).
func pruefeOWUIJWT(token string, geheimnis []byte, jetzt time.Time) (string, time.Time, error) {
	return pruefeOWUIJWTMitAlter(token, geheimnis, jetzt, owuiHoechstalter)
}

// pruefeOWUIJWTMitAlter prüft ein OWUI-Nutzer-JWT und liefert E-Mail und
// Ablauf — den früheren von exp und iat+hoechstalter (K1-3).
// Reihenfolge: Form → Header (alg strikt HS256) → Signatur (konstanter
// Vergleich) → erst DANN der Inhalt. Die Gründe gehen nur ins Server-Log,
// nach aussen gibt es ein einheitliches 401.
func pruefeOWUIJWTMitAlter(token string, geheimnis []byte, jetzt time.Time, hoechstalter time.Duration) (email string, ablauf time.Time, err error) {
	fehler := func(grund string) (string, time.Time, error) {
		return "", time.Time{}, fmt.Errorf("%w: %s", errOWUIJWT, grund)
	}
	if len(geheimnis) == 0 {
		return fehler("kein geheimnis konfiguriert")
	}
	if len(token) > owuiJWTMaxLaenge {
		return fehler("zu lang")
	}
	teile := strings.Split(token, ".")
	if len(teile) != 3 || teile[0] == "" || teile[1] == "" || teile[2] == "" {
		return fehler("keine drei teile")
	}
	kopfRoh, err := base64.RawURLEncoding.DecodeString(teile[0])
	if err != nil {
		return fehler("kopf nicht base64url")
	}
	var kopf map[string]json.RawMessage
	if json.Unmarshal(kopfRoh, &kopf) != nil {
		return fehler("kopf kein json-objekt")
	}
	// alg strikt: genau "HS256". "none", "hs256", "HS384", RS256 (Verwechslung
	// mit öffentlichem Schlüssel) — alles abgelehnt.
	var alg string
	if json.Unmarshal(kopf["alg"], &alg) != nil || alg != "HS256" {
		return fehler("alg ist nicht HS256")
	}
	if typ, ok := kopf["typ"]; ok {
		var s string
		if json.Unmarshal(typ, &s) != nil || !strings.EqualFold(s, "JWT") {
			return fehler("typ ist nicht JWT")
		}
	}
	if _, ok := kopf["crit"]; ok {
		// Erweiterungen, die wir verstehen müssten (RFC 7515 §4.1.11) — kennen wir nicht.
		return fehler("crit nicht unterstützt")
	}
	sig, err := base64.RawURLEncoding.DecodeString(teile[2])
	if err != nil {
		return fehler("signatur nicht base64url")
	}
	mac := hmac.New(sha256.New, geheimnis)
	mac.Write([]byte(teile[0] + "." + teile[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return fehler("signatur falsch")
	}

	inhaltRoh, err := base64.RawURLEncoding.DecodeString(teile[1])
	if err != nil {
		return fehler("inhalt nicht base64url")
	}
	var c struct {
		Iss   *string  `json:"iss"`
		Exp   *float64 `json:"exp"`
		Iat   *float64 `json:"iat"`
		Email *string  `json:"email"`
	}
	if json.Unmarshal(inhaltRoh, &c) != nil {
		return fehler("inhalt kein json-objekt")
	}
	if c.Iss == nil || *c.Iss != owuiIssuer {
		return fehler("iss ist nicht open-webui")
	}
	if c.Exp == nil || c.Iat == nil || !endlich(*c.Exp) || !endlich(*c.Iat) {
		return fehler("exp oder iat fehlt")
	}
	exp := sekunden(*c.Exp)
	iat := sekunden(*c.Iat)
	// Karte #730: Alter und von OWUI ausgestellte Gültigkeit stehen in der
	// Meldung — ohne sie liess sich „abgelaufen“ (exp von OWUI) nicht von
	// „zu alt“ (unser Höchstalter) trennen, beide liegen beim Standard auf
	// 300 s. Zahlen aus dem geprüften JWT, kein Geheimnis.
	zeit := fmt.Sprintf(" (alter %d s, owui-gültigkeit %d s, höchstalter %d s)",
		int(jetzt.Sub(iat).Seconds()), int(exp.Sub(iat).Seconds()), int(hoechstalter.Seconds()))
	if !jetzt.Before(exp.Add(owuiUhrToleranz)) {
		return fehler("abgelaufen" + zeit)
	}
	if iat.After(jetzt.Add(owuiUhrToleranz)) {
		return fehler("iat in der zukunft" + zeit)
	}
	if jetzt.Sub(iat) > hoechstalter+owuiUhrToleranz {
		return fehler("zu alt" + zeit)
	}
	if c.Email == nil {
		return fehler("email fehlt")
	}
	e := storage.NormalizeEmail(*c.Email)
	if !personenAdresse(e) {
		return fehler("email unbrauchbar")
	}
	// Kritiker Sprint 82, K1-3: Länger als das Höchstalter ab iat lebt keine
	// Verbindung auf diesem JWT — auch wenn OWUI ein weites exp ausstellt.
	// Angenommen wird ein JWT bis iat+Höchstalter+Toleranz; der Aufrufer
	// rechnet die Toleranz auf das Ende auf, beides passt also zusammen.
	if grenze := iat.Add(hoechstalter); grenze.Before(exp) {
		exp = grenze
	}
	return e, exp, nil
}

func endlich(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e15 }

func sekunden(f float64) time.Time {
	s, rest := math.Modf(f)
	return time.Unix(int64(s), int64(rest*1e9)).UTC()
}

// personenAdresse: eine persönliche E-Mail in schlichter Form — genau ein
// `@`, lokaler Teil und Domain nicht leer, kein Leerraum und keine
// Steuerzeichen, gültig nach RFC 5322 (net/mail) OHNE Anzeigenamen,
// Kommentar oder Quoting: Die geparste Adresse muss Zeichen für Zeichen der
// Eingabe gleichen. Erwartet die schon normalisierte Form (klein, getrimmt).
// Ausdrücklich KEIN Domain-Schlüssel „@firma.de“: der träfe in GetRole den
// Domain-Eintrag direkt und wäre keine Person. Kritiker Sprint 82, K1-2:
// `a@b@firma.de` las die alte Prüfung (letztes `@`) als Person der Domain
// firma.de — und die Domain-Freigabe zählt den Teil nach dem LETZTEN `@`.
func personenAdresse(e string) bool {
	if e == "" || storage.IstDomainSubjekt(e) || strings.Count(e, "@") != 1 {
		return false
	}
	// IsPrint schliesst auch Format-Zeichen wie das nullbreite Leerzeichen aus,
	// die net/mail als Buchstaben annähme.
	if strings.IndexFunc(e, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }) >= 0 ||
		strings.ContainsAny(e, ",;<>()\"\\[]") {
		return false
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Name != "" || a.Address != e {
		return false
	}
	lokal, domain, _ := strings.Cut(e, "@")
	return lokal != "" && domain != "" && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

// owuiHeaderEntfernen liest das JWT (höchstens ein Wert) und entfernt danach
// ALLE OWUI-Nutzer-Kopfzeilen samt JWT aus dem Request — kein Handler soll
// eine davon je auswerten können.
func owuiHeaderEntfernen(r *http.Request) (jwt string, anzahl int) {
	werte := r.Header.Values(OWUIUserJWTHeader)
	if len(werte) == 1 {
		jwt = strings.TrimSpace(werte[0])
	}
	for k := range r.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), owuiUserHeaderPraefix) {
			delete(r.Header, k)
		}
	}
	return jwt, len(werte)
}

// delegierterPrincipal baut für ein delegierendes Token den Principal der
// Anfrage: Person aus dem JWT, Board aus dem Pfad, Rolle aus der ACL. Liefert
// bei Ablehnung Status und Meldung. 401 heisst „Identität fehlt/ungültig“,
// 403 „Person darf auf diesem Board nicht“ — für ein nicht vorhandenes Board
// ebenso 403, damit die Antwort nicht verrät, welche Boards es gibt.
func (z *AgentZugang) delegierterPrincipal(r *http.Request, t *storage.AgentToken, jwt string, anzahl int) (*AgentPrincipal, int, string) {
	segs, segsOK := agentSegmente(r.URL.EscapedPath())
	return z.delegierterPrincipalFuer(r.Context(), segs, segsOK, t, jwt, anzahl)
}

// delegierterPrincipalFuer bildet den Principal für einen Werkzeug-Weg
// (segs). Zweimal benutzt: von der Middleware für den Pfad der Anfrage und
// vom MCP-Eingang (Karte #753, ADR-0139 §3 Schritt B) für den Werkzeug-Pfad,
// auf den ein tools/call abgebildet wird — dieselbe Prüfung, kein zweiter
// Rechteweg.
func (z *AgentZugang) delegierterPrincipalFuer(ctx context.Context, segs []string, segsOK bool, t *storage.AgentToken, jwt string, anzahl int) (*AgentPrincipal, int, string) {
	if z.owuiGeheimnis == nil || z.owuiACL == nil {
		return nil, http.StatusUnauthorized, "delegating token, but delegation is not configured on this server (" + OWUIJWTSecretFileEnv + ")"
	}
	verboten := "forbidden for agent"
	if segsOK && len(segs) > 0 && segs[0] == werkzeugSegment {
		verboten = werkzeugVerbotenMeldung
	}
	if segsOK && (werkzeugIstSpezifikation(segs) || werkzeugIstMCP(segs)) {
		// Karte #671: Die Spezifikation holt Open WebUI beim Einrichten und
		// Auffrischen des Werkzeug-Servers — aus dem Backend, ohne angemeldete
		// Person und damit ohne JWT. Sie beschreibt nur Wege, keine Daten.
		// Kein Board, keine Person, keine Rolle: agentDarf lässt mit diesem
		// Principal nichts anderes zu.
		// Karte #753 (ADR-0139 §3 Schritt A): /mcp ebenso — server/discover,
		// initialize und tools/list liefern keine Board-Daten. Ein tools/call
		// prüft das JWT erst in Schritt B, gegen den Werkzeug-Pfad.
		return &AgentPrincipal{TokenID: t.ID, Name: t.Name, Delegiert: true, tokenRolle: t.Role,
			delegiertBis: z.jetzt().Add(requestTimeout)}, 0, ""
	}
	if !segsOK || len(segs) == 0 || segs[0] != werkzeugSegment {
		// Kritiker Sprint 82, K1-1: Ein delegierendes Token handelt nur über die
		// Werkzeuge (agentDarf sperrt den Rest ebenfalls). Abgelehnt wird hier
		// schon VOR JWT und ACL — ein Weg, den es für dieses Token nie gibt,
		// soll weder die Rolle der Person abfragen noch ihr Fehlen verraten.
		return nil, http.StatusForbidden, "forbidden for agent: a delegating token may only use the tool endpoints (/werkzeuge/…, /openapi.json, /mcp)"
	}
	if anzahl != 1 || jwt == "" {
		return nil, http.StatusUnauthorized, "delegating token needs exactly one signed " + OWUIUserJWTHeader + " header"
	}
	alter := z.owuiHoechstalter
	if alter == 0 {
		alter = owuiHoechstalter
	}
	email, ablauf, err := pruefeOWUIJWTMitAlter(jwt, z.owuiGeheimnis, z.jetzt(), alter)
	if err != nil {
		owuiLog("aboard-server: delegierendes token %q: %v", t.Name, err)
		return nil, http.StatusUnauthorized, "invalid " + OWUIUserJWTHeader
	}
	if segsOK && werkzeugOhneBoard(segs) {
		// Karte #671, Werkzeug `boards` (und #743 `termine_lesen`): Person
		// bekannt, Board noch nicht — welche Boards sie sieht, liest der
		// Handler aus der ACL.
		return &AgentPrincipal{TokenID: t.ID, Name: t.Name, OnBehalfOf: email, Delegiert: true, tokenRolle: t.Role,
			delegiertBis: ablauf.Add(owuiUhrToleranz)}, 0, ""
	}
	board, ok := agentBoardAusPfad(segs)
	if !ok {
		return nil, http.StatusForbidden, verboten
	}
	if board == storage.ServiceBoardID {
		// Mail-Eingang und Spam-Liste: nie über OWUI (wie ADR-0132 §6 für Domains).
		return nil, http.StatusForbidden, verboten
	}
	aclRolle, gefunden, err := z.owuiACL.GetRole(ctx, board, email)
	if err != nil {
		return nil, http.StatusInternalServerError, "role check failed"
	}
	rolle := ""
	if gefunden {
		rolle = storage.KappeAgentRolle(t.Role, aclRolle)
	}
	if rolle == "" {
		return nil, http.StatusForbidden, verboten
	}
	return &AgentPrincipal{TokenID: t.ID, Name: t.Name, BoardID: board, Role: rolle, OnBehalfOf: email,
		Delegiert: true, delegiertBis: ablauf.Add(owuiUhrToleranz), tokenRolle: t.Role}, 0, ""
}

// owuiPersonStill: die E-Mail aus einem gültigen OWUI-JWT, sonst "". Dieselbe
// Signatur- und Altersprüfung wie delegierterPrincipalFuer, aber ohne Log —
// für den Schlüssel der Listen-Ratenbremse am MCP-Eingang (Verify 08.10.,
// W-1). Entscheidet über keine Rechte.
func (z *AgentZugang) owuiPersonStill(jwt string, anzahl int) string {
	if z.owuiGeheimnis == nil || anzahl != 1 || jwt == "" {
		return ""
	}
	alter := z.owuiHoechstalter
	if alter == 0 {
		alter = owuiHoechstalter
	}
	email, _, err := pruefeOWUIJWTMitAlter(jwt, z.owuiGeheimnis, z.jetzt(), alter)
	if err != nil {
		return ""
	}
	return email
}

// agentBoardAusPfad: das Board einer Werkzeug-Anfrage —
// `/werkzeuge/boards/{id}/…` (Karte #671). Andere Pfade haben keines; die
// rohen `/boards/{id}/…`-Wege erreicht ein delegierendes Token nicht
// (Kritiker Sprint 82, K1-1).
func agentBoardAusPfad(segs []string) (string, bool) {
	if len(segs) >= 3 && segs[0] == werkzeugSegment && segs[1] == "boards" {
		return segs[2], true
	}
	return "", false
}

// delegiertNochBerechtigt: Die Wache fragt das bei offenen Verbindungen
// zusätzlich zum Token ab — entzieht der Owner der Person das Board, endet
// auch die Verbindung, nicht erst die nächste Anfrage.
func (z *AgentZugang) delegiertNochBerechtigt(ctx context.Context, p *AgentPrincipal, tokenRolle string) (bool, error) {
	aclRolle, gefunden, err := z.owuiACL.GetRole(ctx, p.BoardID, p.OnBehalfOf)
	if err != nil {
		return false, err
	}
	return gefunden && storage.KappeAgentRolle(tokenRolle, aclRolle) != "", nil
}

// owuiLog ist log.Printf — als Variable, damit ein Test die Meldung sieht.
// Geloggt wird nur der Grund und der Token-Name, nie das JWT oder die E-Mail.
var owuiLog = log.Printf
