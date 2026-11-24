package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Agenten-Tokens (ADR-0129 §3, Karte #553).
//
// Ein Agent ist KEIN Nutzer (K-1). Er hat keinen users-Eintrag, keinen
// ACL-Eintrag und läuft nie durch BootstrapNewUser. Board und Rolle stehen am
// Token. Gespeichert wird nur der Hash; der Klartext verlässt Create genau
// einmal.

// AgentTokenPraefix leitet jeden Klartext ein. Er macht ein Token in Logs,
// Konfigurationsdateien und Fehlermeldungen erkennbar (logredact) und lässt
// den Server einen falsch eingefügten Wert von einem Tippfehler unterscheiden.
const AgentTokenPraefix = "abt_"

// Rollen, die ein Agent haben kann. owner gibt es für Agenten nicht.
const (
	AgentRolleEditor = RoleEditor
	AgentRolleViewer = RoleViewer
	// AgentRolleVorschlag (ADR-0133 §6, Karte #670): darf auf seinem Board nur
	// neue Karten anlegen, die der Server in die Swimlane „Vorschläge“ lenkt —
	// kein Lesen, kein Ändern, kein Löschen. Nur als Projekt-Token (ohne
	// Person, nie delegierend); keine ACL-Rolle, deshalb rolleRang 0.
	AgentRolleVorschlag = "vorschlag"
	// AgentRolleIntern (ADR-0140 §8, Karte #783): der interne Agent auf dem
	// externen Server des Board-Paars. Er liest über fünf feste Wege, ändert
	// sofort nur Spalte und Priorität und schlägt alles andere vor
	// (api/vorschlaege.go). Nur als Projekt-Token für genau ein Board, wie
	// vorschlag; keine ACL-Rolle, deshalb rolleRang 0. Ob die Instanz die
	// Seite „außen“ ist, prüft die Anlage in API und CLI und jeder Weg der
	// Rolle — die Speicherschicht kennt die Seite nicht.
	AgentRolleIntern = "intern"
)

var (
	ErrAgentTokenUnbekannt = errors.New("storage: agent-token unbekannt oder widerrufen")
	// ErrAgentNameMehrdeutig: Mehrere aktive Tokens tragen den Namen — der
	// Widerruf nach Namen träfe ein beliebiges (F-1434); dann nur per id.
	ErrAgentNameMehrdeutig = errors.New("storage: mehrere aktive agent-tokens mit diesem Namen, bitte per id widerrufen")
	// ErrAgentNameVergeben: Der Name hat ein aktives Token — oder er gehörte
	// je einer anderen Person (F-1381: Namen sind dauerhaft an ihre Person
	// gebunden, auch nach Widerruf und Ablauf).
	ErrAgentNameVergeben = errors.New("storage: dieser agent-name ist vergeben (aktiv oder an eine andere person gebunden)")
	ErrAgentEingabe      = errors.New("storage: ungültige agent-angabe")
	// ErrAgentKeinZugang: Die Person darf für dieses Board keinen Agenten-Zugang
	// anlegen — keine Rolle ≥ editor, oder die Selbstbedienung ist aus (Karte #608).
	ErrAgentKeinZugang = errors.New("storage: kein recht, für dieses board einen agenten-zugang anzulegen")
	// ErrAgentSelbstbedienungAus: Der Admin-Schalter des Boards ist aus.
	ErrAgentSelbstbedienungAus = errors.New("storage: selbstbedienung für agenten-zugänge ist auf diesem board aus")
)

// Ablaufzeit (ADR-0130 §6, Leitplanken der Selbstbedienung): Voreinstellung
// 90 Tage, höchstens 365. Gespeichert in fester Breite, damit der
// Textvergleich in SQL stimmt — nowISO (RFC3339Nano) kürzt Nullen weg.
const (
	AgentTokenGueltigStandard = 90
	AgentTokenGueltigMax      = 365
	agentAblaufFormat         = "2006-01-02T15:04:05Z"
)

// Neues Token-Format (Karte #608): abt_<kennung>_<geheimnis>. Die Kennung ist
// nicht geheim — sie steht in Listen und trennt in der Fehlversuch-Bremse die
// Tokens hinter derselben Adresse. Das Altformat abt_<geheimnis> gilt weiter.
var (
	agentTokenNeuForm = regexp.MustCompile(`^abt_([a-z2-7]{10})_[A-Za-z0-9_-]{43}$`)
	kennungAlphabet   = "abcdefghijklmnopqrstuvwxyz234567"
)

// AgentTokenKennung liefert die Kennung eines Klartexts im neuen Format, sonst
// "". Prüft nichts gegen die Datenbank — ein erfundenes Token hat auch eine.
func AgentTokenKennung(klartext string) string {
	if m := agentTokenNeuForm.FindStringSubmatch(klartext); m != nil {
		return m[1]
	}
	return ""
}

func neueKennung() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("agent-token: kennung: %w", err)
	}
	out := make([]byte, 10)
	for i, x := range b {
		out[i] = kennungAlphabet[int(x)%len(kennungAlphabet)]
	}
	return string(out), nil
}

// rolleRang ordnet ACL- und Token-Rollen; unbekannt = 0.
func rolleRang(r string) int {
	switch r {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// wirksameRolle: das Minimum aus Token-Rolle und ACL-Rolle der Person
// (ADR-0130 §6). Agenten sind höchstens editor.
func wirksameRolle(token, acl string) string {
	if rolleRang(acl) < rolleRang(token) {
		if acl == RoleOwner {
			return RoleEditor
		}
		return acl
	}
	return token
}

// KappeAgentRolle ist wirksameRolle für den delegierenden Weg (ADR-0133 §4):
// Rolle der Person (ACL inkl. Domain-Freigabe), gekappt auf die Token-Rolle
// und damit höchstens editor. Keine oder eine unbekannte ACL-Rolle ergibt "".
func KappeAgentRolle(token, acl string) string {
	if rolleRang(acl) == 0 || rolleRang(token) == 0 {
		return ""
	}
	return wirksameRolle(token, acl)
}

// agentName: Kleinbuchstaben, Ziffern, Bindestrich; beginnt alphanumerisch.
// Der Name landet als `agent:<name>` im Akteur jeder Op — keine Leerzeichen,
// kein @, damit er nie mit einer Mail-Adresse verwechselt wird.
var agentName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// uuidFoermig: Ein Name in der Form einer Token-id wäre im Admin-Widerruf
// (`DELETE /admin/agent-tokens/{idOderName}` liest zuerst als id) nicht von
// der id eines ANDEREN Tokens zu unterscheiden — ein Skript, das per Namen
// widerruft, träfe dann ein fremdes Token (Verify Sprint 80, V-3).
var uuidFoermig = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// AgentToken ist ein gespeichertes Token ohne Klartext.
type AgentToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BoardID   string `json:"board_id"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by"`
	RevokedAt string `json:"revoked_at,omitempty"`
	// Karte #608: Person (leer = Projekt-Token), Ablauf (leer = unbefristet,
	// nur Altbestand), Kennung (leer = Altformat).
	UserID    string `json:"user_id,omitempty"`
	UserEmail string `json:"user_email,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Kennung   string `json:"kennung,omitempty"`
	// Delegierend (ADR-0133 §4, Karte #669): Dienst-Token ohne Board und
	// Person. Es berechtigt allein zu nichts; Person und Board kommen je
	// Anfrage aus dem signierten OWUI-JWT bzw. dem Pfad (api/agent_owui.go).
	Delegierend bool `json:"delegierend,omitempty"`
	// Mehrere (ADR-0139 §5, Karte #754): Personen-Token für eine feste Liste
	// von Boards (Boards). BoardID ist dann leer; die wirksame Rolle gibt es
	// nur je Board (RolleAufBoard), WirksameRolle bleibt leer.
	Mehrere bool     `json:"mehrere,omitempty"`
	Boards  []string `json:"boards,omitempty"`
	// Anbieter und Beleg (ADR-0140 §8, Karte #799): wer den Agenten betreibt
	// und wo der vertragliche Trainingsausschluss belegt ist. Dokumentation,
	// keine Prüfung — auf der Seite außen verlangen die Anlage-Wege beides,
	// Bestandstokens können leer sein.
	Anbieter string `json:"anbieter,omitempty"`
	Beleg    string `json:"beleg,omitempty"`
	// WirksameRolle: nur bei Lookup gefüllt — min(Role, ACL-Rolle der Person).
	WirksameRolle string `json:"-"`
}

// AgentTokenAnlage beschreibt ein neues Token.
type AgentTokenAnlage struct {
	Name, BoardID, Role, CreatedBy string
	// UserID: die Person, für die der Agent handelt; leer = Projekt-Token.
	UserID string
	// GueltigTage: 0 = Voreinstellung (90); mehr als 365 ist ein Fehler.
	GueltigTage int
	// Selbstbedienung: Die Person legt das Token selbst an. Dann muss der
	// Admin-Schalter des Boards an sein. Unabhängig davon braucht jedes Token
	// mit Person deren Rolle ≥ editor auf dem Board.
	Selbstbedienung bool
	// Delegierend: Dienst-Token für Open WebUI (ADR-0133 §4). Verlangt leeres
	// BoardID und UserID und ist nie Selbstbedienung — nur Betreiber (CLI).
	Delegierend bool
	// BoardIDs (ADR-0139 §5, Karte #754): Personen-Token für mehrere Boards.
	// Verlangt leeres BoardID und eine Person. Jedes Board braucht einen
	// PERSÖNLICHEN ACL-Eintrag der Person mit mindestens editor (ADR-0132 §6,
	// die Domain-Freigabe zählt nicht), bei Selbstbedienung zusätzlich den
	// eingeschalteten Schalter. Die Liste ist fest: später freigegebene Boards
	// kommen nie von selbst dazu.
	BoardIDs []string
	// AlleMeineBoards: statt BoardIDs alle Boards, die die Bedingungen oben
	// JETZT erfüllen („alle meine Boards“ = Stand bei der Anlage).
	AlleMeineBoards bool
	// Anbieter, Beleg (Karte #799): schon geprüft und getrimmt vom Anlage-Weg
	// (api.PruefeAgentHerkunft); die Speicherschicht kennt die Seite nicht und
	// speichert, was sie bekommt.
	Anbieter, Beleg string
}

// AgentTokenMaxBoards begrenzt die Liste eines Mehr-Board-Tokens. Mehr Boards
// hat heute keine Person; die Grenze hält Anlage und Audit klein.
const AgentTokenMaxBoards = 200

// AgentTokenRepo verwaltet agent_tokens und schreibt Anlage und Widerruf
// in server_config_audit (K-13).
type AgentTokenRepo struct {
	db *sql.DB
}

func NewAgentTokenRepo(db *sql.DB) *AgentTokenRepo {
	return &AgentTokenRepo{db: db}
}

// Create legt ein Projekt-Token (ohne Person) mit Voreinstellungs-Ablauf an.
func (r *AgentTokenRepo) Create(ctx context.Context, name, boardID, role, createdBy string) (string, *AgentToken, error) {
	return r.Anlegen(ctx, AgentTokenAnlage{Name: name, BoardID: boardID, Role: role, CreatedBy: createdBy})
}

// Anlegen legt ein Token an und liefert den Klartext — der einzige Moment, in
// dem er existiert. CreatedBy ist die Nutzer-/Admin-ID bzw. „cli“.
func (r *AgentTokenRepo) Anlegen(ctx context.Context, a AgentTokenAnlage) (string, *AgentToken, error) {
	name, boardID, role, createdBy := a.Name, a.BoardID, a.Role, a.CreatedBy
	if !agentName.MatchString(name) {
		return "", nil, fmt.Errorf("%w: name %q (erlaubt: a-z, 0-9, '-', max. 63 Zeichen)", ErrAgentEingabe, name)
	}
	if uuidFoermig.MatchString(name) {
		return "", nil, fmt.Errorf("%w: name %q hat die Form einer Token-id — bitte einen sprechenden Namen", ErrAgentEingabe, name)
	}
	if role != AgentRolleEditor && role != AgentRolleViewer && role != AgentRolleVorschlag && role != AgentRolleIntern {
		return "", nil, fmt.Errorf("%w: rolle %q (erlaubt: editor, viewer, vorschlag, intern)", ErrAgentEingabe, role)
	}
	if role == AgentRolleIntern && (a.UserID != "" || a.Delegierend || a.Selbstbedienung || len(a.BoardIDs) > 0 || a.AlleMeineBoards || boardID == "") {
		// ADR-0140 §8: nur als Projekt-Token für genau ein Board. Der CHECK aus
		// Migration 0038 hält dasselbe in der Tabelle fest.
		return "", nil, fmt.Errorf("%w: die rolle intern gibt es nur als projekt-token für genau ein board (ohne person, nicht delegierend)", ErrAgentEingabe)
	}
	if role == AgentRolleIntern && boardID == ServiceBoardID {
		return "", nil, fmt.Errorf("%w: kein intern-token für das service-board", ErrAgentEingabe)
	}
	if role == AgentRolleVorschlag && (a.UserID != "" || a.Delegierend || a.Selbstbedienung) {
		// ADR-0133 §6: nur als Projekt-Token. Mit Person hätte das Token deren
		// Leserecht im Rücken (und wirksameRolle kennt kein Minimum mit
		// „vorschlag“); delegierend nähme es die Rolle der Person aus dem JWT.
		// Selbstbedienung legt immer Tokens mit Person an. Der CHECK aus
		// Migration 0025 hält dasselbe in der Tabelle fest.
		return "", nil, fmt.Errorf("%w: die rolle vorschlag gibt es nur als projekt-token (ohne person, nicht delegierend)", ErrAgentEingabe)
	}
	if role == AgentRolleVorschlag && boardID == ServiceBoardID {
		// Mail-Eingang: dort gehören Karten den Mails, nicht einer Recherche
		// (der delegierende Weg sperrt das Service-Board ebenso).
		return "", nil, fmt.Errorf("%w: kein vorschlags-token für das service-board", ErrAgentEingabe)
	}
	tage := a.GueltigTage
	if tage == 0 {
		tage = AgentTokenGueltigStandard
	}
	if tage < 1 || tage > AgentTokenGueltigMax {
		return "", nil, fmt.Errorf("%w: gültigkeit %d tage (erlaubt: 1..%d)", ErrAgentEingabe, tage, AgentTokenGueltigMax)
	}
	if a.Delegierend && (boardID != "" || a.UserID != "" || a.Selbstbedienung) {
		// ADR-0133 §4: Board und Person kommen je Anfrage, nie vom Token. Ein
		// delegierendes Token MIT Board wäre eine dritte, ungeregelte Form.
		return "", nil, fmt.Errorf("%w: ein delegierendes token hat kein board und keine person", ErrAgentEingabe)
	}
	mehrere := len(a.BoardIDs) > 0 || a.AlleMeineBoards
	if mehrere {
		// ADR-0139 §5: nur als Personen-Token, nie delegierend, nie vorschlag,
		// und nicht zugleich mit einem einzelnen Board — die rohen Ops bleiben
		// bei einem Board je Token, ein Mehr-Board-Token hat keine.
		if a.UserID == "" || a.Delegierend || boardID != "" || role == AgentRolleVorschlag {
			return "", nil, fmt.Errorf("%w: ein token für mehrere boards braucht eine person und hat weder ein einzelnes board noch delegation oder die rolle vorschlag", ErrAgentEingabe)
		}
		if len(a.BoardIDs) > 0 && a.AlleMeineBoards {
			return "", nil, fmt.Errorf("%w: entweder eine board-liste oder „alle meine boards“", ErrAgentEingabe)
		}
		if len(a.BoardIDs) > AgentTokenMaxBoards {
			return "", nil, fmt.Errorf("%w: höchstens %d boards je token", ErrAgentEingabe, AgentTokenMaxBoards)
		}
	}
	zufall, err := newToken()
	if err != nil {
		return "", nil, err
	}
	kennung, err := neueKennung()
	if err != nil {
		return "", nil, err
	}
	klartext := AgentTokenPraefix + kennung + "_" + zufall
	jetzt := timeNow().UTC()
	t := &AgentToken{
		ID: uuid.NewString(), Name: name, BoardID: boardID, Role: role,
		CreatedAt: nowISO(), CreatedBy: createdBy, UserID: a.UserID, Kennung: kennung,
		ExpiresAt:   jetzt.Add(time.Duration(tage) * 24 * time.Hour).Format(agentAblaufFormat),
		Delegierend: a.Delegierend, Mehrere: mehrere, Anbieter: a.Anbieter, Beleg: a.Beleg,
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, fmt.Errorf("agent_tokens.Create begin: %w", err)
	}
	defer tx.Rollback()

	if mehrere {
		// Rechte VOR Namen (F-1335), wie beim einzelnen Board unten.
		liste, err := mehrBoardListeIn(ctx, tx, a)
		if err != nil {
			return "", nil, err
		}
		t.Boards = liste
	} else if !a.Delegierend {
		var gibtEs int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM boards WHERE id = ?`, boardID).Scan(&gibtEs); err != nil {
			return "", nil, fmt.Errorf("agent_tokens.Create board: %w", err)
		}
		if gibtEs == 0 {
			return "", nil, ErrBoardNotFound
		}
	}
	// Karte #638 (F-1335): Rechte VOR Namen. Wer auf diesem Board keinen
	// Agenten anlegen darf, bekommt die Rechte-Absage — unabhängig davon,
	// ob der Name belegt ist. Sonst wäre die Anlage ein Namens-Orakel für
	// jede angemeldete Person.
	if a.UserID != "" && !mehrere {
		// Rolle der Person im selben Lesezustand wie das INSERT: Wer nicht
		// mindestens editor ist, bekommt keinen Agenten (auch nicht vom Admin
		// — ein Token mit Person, der die Rolle fehlt, gälte ohnehin nicht).
		var aclRolle string
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(a.role, '') FROM users u LEFT JOIN acl a ON a.board_id = ? AND a.user_email = u.email WHERE u.id = ?`,
			boardID, a.UserID).Scan(&aclRolle)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, ErrUserNotFound
		}
		if err != nil {
			return "", nil, fmt.Errorf("agent_tokens.Anlegen acl: %w", err)
		}
		if rolleRang(aclRolle) < rolleRang(RoleEditor) {
			return "", nil, ErrAgentKeinZugang
		}
		if a.Selbstbedienung {
			var an int
			if err := tx.QueryRowContext(ctx, `SELECT agent_selbstbedienung FROM boards WHERE id = ?`, boardID).Scan(&an); err != nil {
				return "", nil, fmt.Errorf("agent_tokens.Anlegen schalter: %w", err)
			}
			if an != 1 {
				return "", nil, ErrAgentSelbstbedienungAus
			}
		}
	}
	// Kritiker Sprint 78 (F-1381): Ein Agentenname bleibt dauerhaft an die
	// Person gebunden, die ihn zuerst benutzt hat — auch nach Widerruf oder
	// Ablauf. Alle Ops eines Agenten tragen den Akteur `agent:<name>`, und
	// daran hängen Urheberschaft (comment.edit „fremder-kommentar“) und
	// Übernahmen (claim.by). Gäbe der Unique-Index (nur aktive Tokens) einen
	// Namen an eine andere Person weiter, erbte sie beides: Sie könnte die
	// „für A“ gestempelten Kommentare umschreiben und A's Übernahmen lösen.
	// Projekt-Tokens ohne Person (user_id NULL) zählen als eigene „Person“.
	// Geprüft wird VOR dem Widerruf abgelaufener Tokens, damit ein
	// abgelehnter Versuch nichts verändert.
	var fremdGebunden int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_tokens WHERE name = ? AND COALESCE(user_id, '') <> ?`,
		name, a.UserID).Scan(&fremdGebunden); err != nil {
		return "", nil, fmt.Errorf("agent_tokens.Anlegen namensbindung: %w", err)
	}
	if fremdGebunden > 0 {
		return "", nil, ErrAgentNameVergeben
	}
	// Session-Kritiker Sprint 78, K-1: Ein abgelaufenes, nicht widerrufenes
	// Token belegte seinen Namen weiter (der Unique-Index kennt nur
	// revoked_at) — nach dem Ablauf liess sich kein Nachfolger gleichen Namens
	// anlegen. Abgelaufene Tokens dieses Namens werden hier widerrufen, mit
	// Audit; ein noch gültiges bleibt und ergibt weiter ErrAgentNameVergeben.
	if err := widerrufeAbgelaufeneIn(ctx, tx, name, createdBy); err != nil {
		return "", nil, err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, created_by, user_id, expires_at, kennung, delegierend, mehrere, anbieter, beleg)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, nullableString(t.BoardID), t.Role, hashToken(klartext), t.CreatedAt, t.CreatedBy,
		nullableString(t.UserID), t.ExpiresAt, t.Kennung, boolToInt(t.Delegierend), boolToInt(t.Mehrere), t.Anbieter, t.Beleg)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return "", nil, ErrAgentNameVergeben
		}
		return "", nil, fmt.Errorf("agent_tokens.Create insert: %w", err)
	}
	for _, b := range t.Boards {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_token_boards (token_id, board_id) VALUES (?, ?)`, t.ID, b); err != nil {
			return "", nil, fmt.Errorf("agent_tokens.Anlegen boards: %w", err)
		}
	}
	neu := "board=" + boardID + ";role=" + role + ";expires=" + t.ExpiresAt
	if t.Delegierend {
		neu = "delegierend;role=" + role + ";expires=" + t.ExpiresAt
	}
	if t.Mehrere {
		neu = "boards=" + strings.Join(t.Boards, ",") + ";role=" + role + ";expires=" + t.ExpiresAt
	}
	if t.UserID != "" {
		neu += ";user=" + t.UserID
	}
	// Karte #799: Anbieter und Beleg stehen auch im Audit — wird das Board
	// gelöscht, fallen die Token-Zeilen weg (Trigger aus Migration 0018), der
	// Nachweis des Trainingsausschlusses bliebe sonst nirgends. Die Werte sind
	// vom Anlage-Weg geprüft (keine Steuer- oder unsichtbaren Zeichen).
	if t.Anbieter != "" {
		neu += ";anbieter=" + t.Anbieter
	}
	if t.Beleg != "" {
		neu += ";beleg=" + t.Beleg
	}
	if err := agentAudit(ctx, tx, name, createdBy, "agent-token-create", neu, t.CreatedAt); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("agent_tokens.Create commit: %w", err)
	}
	return klartext, t, nil
}

// aktivAbfrage liest ein Token samt Person und deren ACL-Rolle — nur, wenn
// es nicht widerrufen und nicht abgelaufen ist. Die Bedingung „Person hat
// noch eine Rolle“ prüft ladeAktiv, damit Lookup und IstAktiv dieselbe Regel
// haben (ein Aufschrieb, knowledge §30).
const aktivAbfrage = `SELECT t.id, t.name, COALESCE(t.board_id, ''), t.role, t.created_at, t.created_by,
       COALESCE(t.user_id, ''), COALESCE(t.expires_at, ''), COALESCE(t.kennung, ''),
       COALESCE(u.email, ''), COALESCE(a.role, ''), t.delegierend, t.mehrere
  FROM agent_tokens t
  LEFT JOIN users u ON u.id = t.user_id
  LEFT JOIN acl a ON a.board_id = t.board_id AND a.user_email = u.email
 WHERE t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > ?) AND `

func (r *AgentTokenRepo) ladeAktiv(ctx context.Context, bedingung string, arg any) (*AgentToken, error) {
	t := &AgentToken{}
	var aclRolle string
	var delegierend, mehrere int
	jetzt := timeNow().UTC().Format(agentAblaufFormat)
	err := r.db.QueryRowContext(ctx, aktivAbfrage+bedingung, jetzt, arg).
		Scan(&t.ID, &t.Name, &t.BoardID, &t.Role, &t.CreatedAt, &t.CreatedBy,
			&t.UserID, &t.ExpiresAt, &t.Kennung, &t.UserEmail, &aclRolle, &delegierend, &mehrere)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAgentTokenUnbekannt
	}
	if err != nil {
		return nil, err
	}
	t.Delegierend = delegierend == 1
	t.Mehrere = mehrere == 1
	t.WirksameRolle = t.Role
	if t.Mehrere {
		// ADR-0139 §5: Die Rolle gibt es nur je Board (RolleAufBoard); hier
		// leer, damit niemand die Token-Rolle für eine Erlaubnis hält. Gültig
		// ist das Token, solange es die Person gibt — ob sie auf einem Board
		// noch eine Rolle hat, entscheidet jeder Aufruf für sein Board.
		if t.UserID == "" || t.UserEmail == "" || t.BoardID != "" || t.Delegierend {
			return nil, ErrAgentTokenUnbekannt
		}
		t.WirksameRolle = ""
		return t, nil
	}
	if t.Delegierend {
		// ADR-0133 §4: Ohne Person und Board gibt es noch keine wirksame Rolle.
		// Leer gelassen, damit niemand die Token-Rolle für eine Erlaubnis hält;
		// die Middleware ermittelt sie je Anfrage aus JWT und Pfad.
		t.WirksameRolle = ""
	} else if t.BoardID == "" {
		// Der CHECK aus Migration 0024 schliesst das aus. Falls doch: ein
		// boardloses Token ohne Delegation gilt nicht (Ausfall zu).
		return nil, ErrAgentTokenUnbekannt
	}
	if t.UserID != "" {
		// ADR-0130 §6, Kritiker K-6: keine ACL-Rolle heisst kein Zugriff —
		// auch wenn der Nutzer weiter existiert und nur von DIESEM Board
		// entfernt wurde. Fehlt die Person ganz, ebenso.
		if t.UserEmail == "" || aclRolle == "" {
			return nil, ErrAgentTokenUnbekannt
		}
		t.WirksameRolle = wirksameRolle(t.Role, aclRolle)
	}
	return t, nil
}

// Lookup liefert das AKTIVE Token zu einem Klartext. Unbekannt, widerrufen,
// abgelaufen, leer und „Person ohne Rolle“ ergeben gleichermassen
// ErrAgentTokenUnbekannt — der Aufrufer soll die Fälle nach aussen nicht
// unterscheiden.
func (r *AgentTokenRepo) Lookup(ctx context.Context, klartext string) (*AgentToken, error) {
	if !strings.HasPrefix(klartext, AgentTokenPraefix) {
		return nil, ErrAgentTokenUnbekannt
	}
	t, err := r.ladeAktiv(ctx, "t.token_hash = ?", hashToken(klartext))
	if err != nil && !errors.Is(err, ErrAgentTokenUnbekannt) {
		return nil, fmt.Errorf("agent_tokens.Lookup: %w", err)
	}
	return t, err
}

// IstAktiv meldet, ob das Token mit dieser ID noch gilt — nach derselben
// Regel wie Lookup (Widerruf, Ablauf, Rolle der Person). Offene Verbindungen
// fragen das regelmässig ab — so wirkt auch ein Widerruf über die CLI, die in
// einem anderen Prozess läuft (K-4), und der Entzug der Board-Rolle.
func (r *AgentTokenRepo) IstAktiv(ctx context.Context, id string) (bool, error) {
	_, err := r.ladeAktiv(ctx, "t.id = ?", id)
	if errors.Is(err, ErrAgentTokenUnbekannt) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("agent_tokens.IstAktiv: %w", err)
	}
	return true, nil
}

// Revoke widerruft das aktive Token eines Agenten und liefert dessen ID —
// der Aufrufer schliesst damit die offenen Verbindungen (K-4).
func (r *AgentTokenRepo) Revoke(ctx context.Context, name, by string) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("agent_tokens.Revoke begin: %w", err)
	}
	defer tx.Rollback()
	var id string
	var anzahl int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(id), '') FROM agent_tokens WHERE name = ? AND revoked_at IS NULL`, name).Scan(&anzahl, &id)
	if err != nil {
		return "", fmt.Errorf("agent_tokens.Revoke: %w", err)
	}
	if anzahl == 0 {
		return "", ErrAgentTokenUnbekannt
	}
	if anzahl > 1 {
		return "", ErrAgentNameMehrdeutig
	}
	jetzt := nowISO()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE id = ?`, jetzt, id); err != nil {
		return "", fmt.Errorf("agent_tokens.Revoke: %w", err)
	}
	if err := agentAudit(ctx, tx, name, by, "agent-token-revoke", "", jetzt); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("agent_tokens.Revoke commit: %w", err)
	}
	return id, nil
}

// RevokeID widerruft ein aktives Token über seine ID. nurFuer != "" begrenzt
// auf Tokens dieser Person (Selbstbedienung: niemand widerruft fremde).
// Liefert den Namen; unbekannt/fremd/schon widerrufen → ErrAgentTokenUnbekannt.
func (r *AgentTokenRepo) RevokeID(ctx context.Context, id, nurFuer, by string) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("agent_tokens.RevokeID begin: %w", err)
	}
	defer tx.Rollback()
	var name, userID string
	err = tx.QueryRowContext(ctx, `SELECT name, COALESCE(user_id, '') FROM agent_tokens WHERE id = ? AND revoked_at IS NULL`, id).Scan(&name, &userID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && nurFuer != "" && userID != nurFuer) {
		return "", ErrAgentTokenUnbekannt
	}
	if err != nil {
		return "", fmt.Errorf("agent_tokens.RevokeID: %w", err)
	}
	jetzt := nowISO()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE id = ?`, jetzt, id); err != nil {
		return "", fmt.Errorf("agent_tokens.RevokeID: %w", err)
	}
	if err := agentAudit(ctx, tx, name, by, "agent-token-revoke", "", jetzt); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("agent_tokens.RevokeID commit: %w", err)
	}
	return name, nil
}

// RevokeFuerPerson widerruft alle aktiven Tokens einer Person (Admin-Weg,
// Team-Austritt, ADR-0130 §6) und liefert ihre IDs — der Aufrufer schliesst
// damit die offenen Verbindungen.
func (r *AgentTokenRepo) RevokeFuerPerson(ctx context.Context, userID, by string) ([]string, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("%w: person fehlt", ErrAgentEingabe)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("agent_tokens.RevokeFuerPerson begin: %w", err)
	}
	defer tx.Rollback()
	ids, err := widerrufeFuerPersonIn(ctx, tx, userID, by, nowISO(), "user="+userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("agent_tokens.RevokeFuerPerson commit: %w", err)
	}
	return ids, nil
}

// abfrager: das Gemeinsame von *sql.Tx und *sql.Conn, das die Widerrufe
// brauchen — so läuft derselbe Code in der Nutzer-Löschung (Conn mit BEGIN
// IMMEDIATE) und im Admin-Weg (Tx).
type abfrager interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// widerrufeFuerPersonIn widerruft in der laufenden Transaktion alle aktiven
// Tokens der Person, je mit Audit-Eintrag (Wert: vermerk), und liefert ihre
// IDs. Die Nutzer-Löschung übergibt als vermerk das Pseudonym, nicht die ID.
func widerrufeFuerPersonIn(ctx context.Context, ex abfrager, userID, by, ts, vermerk string) ([]string, error) {
	rows, err := ex.QueryContext(ctx, `SELECT id, name FROM agent_tokens WHERE user_id = ? AND revoked_at IS NULL ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("agent_tokens widerruf person: %w", err)
	}
	type tok struct{ id, name string }
	var alle []tok
	for rows.Next() {
		var t tok
		if err := rows.Scan(&t.id, &t.name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("agent_tokens widerruf person scan: %w", err)
		}
		alle = append(alle, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent_tokens widerruf person rows: %w", err)
	}
	ids := make([]string, 0, len(alle))
	for _, t := range alle {
		if _, err := ex.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE id = ?`, ts, t.id); err != nil {
			return nil, fmt.Errorf("agent_tokens widerruf person: %w", err)
		}
		if err := agentAudit(ctx, ex, t.name, by, "agent-token-revoke-person", vermerk, ts); err != nil {
			return nil, err
		}
		ids = append(ids, t.id)
	}
	return ids, nil
}

// listeSpalten: gemeinsame Spaltenliste für List und ListFuerPerson.
const listeSpalten = `SELECT t.id, t.name, COALESCE(t.board_id, ''), t.role, t.created_at, t.created_by, COALESCE(t.revoked_at, ''),
       COALESCE(t.user_id, ''), COALESCE(u.email, ''), COALESCE(t.expires_at, ''), COALESCE(t.kennung, ''), t.delegierend, t.mehrere, t.anbieter, t.beleg
  FROM agent_tokens t LEFT JOIN users u ON u.id = t.user_id `

func scanListe(rows *sql.Rows) ([]AgentToken, error) {
	defer rows.Close()
	out := []AgentToken{}
	for rows.Next() {
		var t AgentToken
		var delegierend, mehrere int
		if err := rows.Scan(&t.ID, &t.Name, &t.BoardID, &t.Role, &t.CreatedAt, &t.CreatedBy, &t.RevokedAt,
			&t.UserID, &t.UserEmail, &t.ExpiresAt, &t.Kennung, &delegierend, &mehrere, &t.Anbieter, &t.Beleg); err != nil {
			return nil, fmt.Errorf("agent_tokens.List scan: %w", err)
		}
		t.Delegierend = delegierend == 1
		t.Mehrere = mehrere == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// List liefert alle Tokens (aktive und widerrufene), neueste zuerst.
func (r *AgentTokenRepo) List(ctx context.Context) ([]AgentToken, error) {
	rows, err := r.db.QueryContext(ctx, listeSpalten+`ORDER BY t.created_at DESC, t.name`)
	if err != nil {
		return nil, fmt.Errorf("agent_tokens.List: %w", err)
	}
	out, err := scanListe(rows)
	if err != nil {
		return nil, err
	}
	return out, r.boardListenFuellen(ctx, out)
}

// ListFuerPerson liefert die Tokens einer Person (Selbstbedienung), neueste zuerst.
func (r *AgentTokenRepo) ListFuerPerson(ctx context.Context, userID string) ([]AgentToken, error) {
	rows, err := r.db.QueryContext(ctx, listeSpalten+`WHERE t.user_id = ? ORDER BY t.created_at DESC, t.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("agent_tokens.ListFuerPerson: %w", err)
	}
	out, err := scanListe(rows)
	if err != nil {
		return nil, err
	}
	return out, r.boardListenFuellen(ctx, out)
}

// Selbstbedienung meldet den Admin-Schalter eines Boards.
func (r *AgentTokenRepo) Selbstbedienung(ctx context.Context, boardID string) (bool, error) {
	var an int
	err := r.db.QueryRowContext(ctx, `SELECT agent_selbstbedienung FROM boards WHERE id = ?`, boardID).Scan(&an)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrBoardNotFound
	}
	if err != nil {
		return false, fmt.Errorf("agent_tokens.Selbstbedienung: %w", err)
	}
	return an == 1, nil
}

// SetzeSelbstbedienung stellt den Admin-Schalter eines Boards, mit Audit.
// Bestehende Tokens bleiben gültig — der Schalter regelt nur die Anlage.
func (r *AgentTokenRepo) SetzeSelbstbedienung(ctx context.Context, boardID string, an bool, by string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agent_tokens.SetzeSelbstbedienung begin: %w", err)
	}
	defer tx.Rollback()
	wert := 0
	if an {
		wert = 1
	}
	res, err := tx.ExecContext(ctx, `UPDATE boards SET agent_selbstbedienung = ? WHERE id = ?`, wert, boardID)
	if err != nil {
		return fmt.Errorf("agent_tokens.SetzeSelbstbedienung: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBoardNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
		 VALUES (?, ?, ?, 'agent-selbstbedienung', NULL, ?, ?)`,
		uuid.NewString(), "agent-selbstbedienung:"+boardID, by, fmt.Sprint(an), nowISO()); err != nil {
		return fmt.Errorf("agent_tokens.SetzeSelbstbedienung audit: %w", err)
	}
	return tx.Commit()
}

// SelbstbedienungsStand ist ein Board mit dem Stand seines Admin-Schalters.
type SelbstbedienungsStand struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	An    bool   `json:"an"`
}

// SelbstbedienungsUebersicht liefert ALLE Boards mit dem Schalterstand —
// für den Admin-Dialog (der Admin sieht über GET /boards nur eigene).
func (r *AgentTokenRepo) SelbstbedienungsUebersicht(ctx context.Context) ([]SelbstbedienungsStand, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, title, agent_selbstbedienung FROM boards ORDER BY title, id`)
	if err != nil {
		return nil, fmt.Errorf("agent_tokens.SelbstbedienungsUebersicht: %w", err)
	}
	defer rows.Close()
	out := []SelbstbedienungsStand{}
	for rows.Next() {
		var b SelbstbedienungsStand
		var an int
		if err := rows.Scan(&b.ID, &b.Title, &an); err != nil {
			return nil, err
		}
		b.An = an == 1
		out = append(out, b)
	}
	return out, rows.Err()
}

// SelbstbedienungsBoard ist ein Board, auf dem eine Person sich selbst einen
// Agenten-Zugang anlegen darf.
type SelbstbedienungsBoard struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Role  string `json:"role"`
}

// SelbstbedienungsBoardsFuer liefert die Boards, auf denen die Person
// (E-Mail) mindestens editor ist und die Selbstbedienung an ist — genau die
// Boards, die Anlegen mit Selbstbedienung annimmt.
func (r *AgentTokenRepo) SelbstbedienungsBoardsFuer(ctx context.Context, email string) ([]SelbstbedienungsBoard, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT b.id, b.title, a.role FROM boards b JOIN acl a ON a.board_id = b.id
		  WHERE a.user_email = ? AND a.role IN ('owner', 'editor') AND b.agent_selbstbedienung = 1
		  ORDER BY b.title, b.id`, NormalizeEmail(email))
	if err != nil {
		return nil, fmt.Errorf("agent_tokens.SelbstbedienungsBoardsFuer: %w", err)
	}
	defer rows.Close()
	out := []SelbstbedienungsBoard{}
	for rows.Next() {
		var b SelbstbedienungsBoard
		if err := rows.Scan(&b.ID, &b.Title, &b.Role); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// KennungBekannt meldet, ob es ein Token (egal welchen Zustands) mit dieser
// Kennung gibt. Die Fehlversuch-Bremse zählt nur bekannte Kennungen getrennt
// — sonst umginge ein Angreifer sie, indem er je Versuch eine neue erfindet.
func (r *AgentTokenRepo) KennungBekannt(ctx context.Context, kennung string) (bool, error) {
	if kennung == "" {
		return false, nil
	}
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_tokens WHERE kennung = ?`, kennung).Scan(&n); err != nil {
		return false, fmt.Errorf("agent_tokens.KennungBekannt: %w", err)
	}
	return n > 0, nil
}

// agentAudit schreibt in server_config_audit (K-13). Der Schlüssel nennt den
// Agenten; Board und Rolle stehen im neuen Wert. Ein Token-Klartext oder
// -Hash landet dort nie.
func agentAudit(ctx context.Context, tx Ausfuehrer, name, userID, action, neu, ts string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
		 VALUES (?, ?, ?, ?, NULL, ?, ?)`,
		uuid.NewString(), "agent-token:"+name, userID, action, nullableString(neu), ts)
	if err != nil {
		return fmt.Errorf("agent_tokens audit: %w", err)
	}
	return nil
}

// widerrufeAbgelaufeneIn widerruft in der laufenden Transaktion die
// abgelaufenen, noch nicht widerrufenen Tokens eines Namens (Audit
// agent-token-revoke-expired). Gilt ein Token dieses Namens noch, bleibt es.
func widerrufeAbgelaufeneIn(ctx context.Context, tx *sql.Tx, name, by string) error {
	jetzt := timeNow().UTC()
	res, err := tx.ExecContext(ctx,
		`UPDATE agent_tokens SET revoked_at = ? WHERE name = ? AND revoked_at IS NULL AND expires_at IS NOT NULL AND expires_at <= ?`,
		jetzt.Format(time.RFC3339Nano), name, jetzt.Format(agentAblaufFormat))
	if err != nil {
		return fmt.Errorf("agent_tokens abgelaufene widerrufen: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return agentAudit(ctx, tx, name, by, "agent-token-revoke-expired", "", jetzt.Format(time.RFC3339Nano))
	}
	return nil
}
