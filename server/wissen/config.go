package wissen

// config.go — Einstellungen `wissen.*` in server_config (ADR-0137 §2).
//
//   wissen.enabled         "true" schaltet ein; alles andere heisst aus (Standard)
//   wissen.dir             Ordner, nur unter ErlaubterPraefix (/data/)
//   wissen.name            Anzeigename, Standard „Wissen“
//   wissen.sichtbar        "alle" (Standard) | "admins" | "gruppe:<name>" (F-1863)
//   wissen.bearbeiten_url  optional, https-Vorlage mit {pfad}
//
// Voreinstellung AUS ist eine Zusage an einen Team-Betrieb (Absprache mit einem
// anderen Projekt, dortige ADR-0045 Punkt 7): Auf dem Team-Board bleibt die
// Lesesicht aus, die Team-Konfiguration setzt den Schalter nicht. Wer die
// Voreinstellung ändert, sagt dem anderen Projekt vorher Bescheid — sonst
// kippt deren Stand still.
//
// Die Prüffunktionen laufen zweimal: beim Speichern im Admin-Dialog (400 mit
// Grund) und beim Lesen (ein Wert, der am Speichern vorbei in die Tabelle
// kam, schaltet die Funktion ab statt zu wirken).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	KeyEnabled       = "wissen.enabled"
	KeyDir           = "wissen.dir"
	KeyName          = "wissen.name"
	KeySichtbar      = "wissen.sichtbar"
	KeyBearbeitenURL = "wissen.bearbeiten_url"

	SichtbarAlle   = "alle"
	SichtbarAdmins = "admins"
	// SichtbarGruppe + Name: nur Personen, deren Anmeldedienst die Gruppe in
	// Remote-Groups meldet, dazu Admins (F-1863, ADR-0137 §5 Nachtrag).
	SichtbarGruppe = "gruppe:"

	// ErlaubterPraefix: wissen.dir muss darunter liegen (ADR-0137 §2). Ein
	// Admin kann so nicht `/` oder `/data` selbst freigeben.
	ErlaubterPraefix = "/data/"

	// PfadPlatzhalter steht in wissen.bearbeiten_url für den Seitenpfad.
	PfadPlatzhalter = "{pfad}"

	standardName = "Wissen"
	maxName      = 80
)

// Config ist die gelesene Sektion.
type Config struct {
	Enabled       bool
	Dir           string
	Name          string
	Sichtbar      string
	BearbeitenURL string
}

// LoadConfig liest die Sektion. praefix ist im Betrieb ErlaubterPraefix;
// Tests geben ein eigenes Verzeichnis an. Ein ungültiger gespeicherter Wert
// ergibt einen Fehler — der Aufrufer behandelt die Funktion dann als aus.
func LoadConfig(ctx context.Context, cfg *storage.ConfigRepo, praefix string) (Config, error) {
	c := Config{Name: standardName, Sichtbar: SichtbarAlle}
	if cfg == nil {
		return c, nil
	}
	get := func(key string) (string, error) {
		v, err := cfg.Get(ctx, key)
		if errors.Is(err, storage.ErrConfigKeyNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return v.Value, nil
	}
	en, err := get(KeyEnabled)
	if err != nil {
		return c, err
	}
	c.Enabled = strings.TrimSpace(en) == "true"
	if !c.Enabled {
		return c, nil
	}
	werte := map[string]string{}
	for _, k := range []string{KeyDir, KeyName, KeySichtbar, KeyBearbeitenURL} {
		v, gerr := get(k)
		if gerr != nil {
			return c, gerr
		}
		werte[k] = v
	}
	if c.Dir, err = PruefeDir(werte[KeyDir], praefix); err != nil {
		return c, err
	}
	if n, nerr := PruefeName(werte[KeyName]); nerr != nil {
		return c, nerr
	} else if n != "" {
		c.Name = n
	}
	if s, serr := PruefeSichtbar(werte[KeySichtbar]); serr != nil {
		return c, serr
	} else if s != "" {
		c.Sichtbar = s
	}
	if c.BearbeitenURL, err = PruefeBearbeitenURL(werte[KeyBearbeitenURL]); err != nil {
		return c, err
	}
	return c, nil
}

// PruefeWert prüft einen einzelnen Schlüssel beim Speichern und liefert die
// Normalform. Unbekannte wissen.*-Schlüssel werden abgelehnt.
func PruefeWert(key, value, praefix string) (string, error) {
	switch key {
	case KeyEnabled:
		v := strings.TrimSpace(value)
		if v != "" && v != "true" && v != "false" {
			return "", fmt.Errorf("%s: erlaubt sind true und false", KeyEnabled)
		}
		return v, nil
	case KeyDir:
		if strings.TrimSpace(value) == "" {
			return "", nil
		}
		return PruefeDir(value, praefix)
	case KeyName:
		return PruefeName(value)
	case KeySichtbar:
		return PruefeSichtbar(value)
	case KeyBearbeitenURL:
		return PruefeBearbeitenURL(value)
	}
	return "", fmt.Errorf("unbekannter Schlüssel %q", key)
}

// PruefeDir verlangt einen absoluten Pfad echt unterhalb von praefix.
func PruefeDir(dir, praefix string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("%s fehlt", KeyDir)
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%s: nur absolute Pfade", KeyDir)
	}
	for _, teil := range strings.Split(dir, "/") {
		if teil == ".." {
			return "", fmt.Errorf("%s: „..“ ist nicht erlaubt", KeyDir)
		}
	}
	sauber := filepath.Clean(dir)
	p := strings.TrimSuffix(filepath.Clean(praefix), "/") + "/"
	if !strings.HasPrefix(sauber+"/", p) || sauber+"/" == p {
		return "", fmt.Errorf("%s: muss unter %s liegen", KeyDir, p)
	}
	return sauber, nil
}

// PruefeName: leer = Standard; sonst druckbar und höchstens maxName Zeichen.
func PruefeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxName {
		return "", fmt.Errorf("%s: höchstens %d Zeichen", KeyName, maxName)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "", fmt.Errorf("%s: enthält Steuerzeichen", KeyName)
		}
	}
	return name, nil
}

// PruefeSichtbar: leer = Standard (alle). "gruppe:<name>" braucht einen Namen
// ohne Komma (Remote-Groups ist kommagetrennt) und ohne Steuerzeichen.
func PruefeSichtbar(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "", SichtbarAlle, SichtbarAdmins:
		return s, nil
	}
	if name, ok := strings.CutPrefix(s, SichtbarGruppe); ok {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 100 || strings.ContainsAny(name, ",\r\n\t") {
			return "", fmt.Errorf("%s: Gruppenname fehlt oder enthält Komma/Steuerzeichen", KeySichtbar)
		}
		return SichtbarGruppe + name, nil
	}
	return "", fmt.Errorf("%s: erlaubt sind %s, %s und %s<name>", KeySichtbar, SichtbarAlle, SichtbarAdmins, SichtbarGruppe)
}

// Gruppe nennt bei sichtbar=gruppe:<name> den Namen, sonst "".
func (c Config) Gruppe() string {
	name, _ := strings.CutPrefix(c.Sichtbar, SichtbarGruppe)
	if name == c.Sichtbar {
		return ""
	}
	return name
}

// PruefeBearbeitenURL: leer = kein Link. Sonst nur https, mit Host, ohne
// Zugangsdaten und ohne Fragment, mit genau einem {pfad}.
func PruefeBearbeitenURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.Count(s, PfadPlatzhalter) != 1 {
		return "", fmt.Errorf("%s: muss genau einmal %s enthalten", KeyBearbeitenURL, PfadPlatzhalter)
	}
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("%s: enthält Leerraum", KeyBearbeitenURL)
	}
	// Kritiker #739 K-2: Stünde {pfad} im Host (`https://{pfad}.forge…`,
	// `https://forge.example{pfad}`), bestimmte ein Ordnername im Repo das
	// Linkziel. Mit einer Marke an seiner Stelle muss der Host frei davon sein.
	const marke = "pfadmarke0739"
	u, err := url.Parse(strings.Replace(s, PfadPlatzhalter, marke, 1))
	if err != nil {
		return "", fmt.Errorf("%s: keine gültige Adresse", KeyBearbeitenURL)
	}
	if strings.Contains(strings.ToLower(u.Host), marke) || u.User != nil && strings.Contains(u.User.String(), marke) {
		return "", fmt.Errorf("%s: %s muss hinter dem Host stehen (im Pfad oder in der Abfrage)", KeyBearbeitenURL, PfadPlatzhalter)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("%s: nur https", KeyBearbeitenURL)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("%s: kein Host", KeyBearbeitenURL)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s: keine Zugangsdaten in der Adresse", KeyBearbeitenURL)
	}
	if u.Fragment != "" || strings.Contains(s, "#") {
		return "", fmt.Errorf("%s: kein Fragment", KeyBearbeitenURL)
	}
	return s, nil
}

// pfadKodierer: url.PathEscape lässt `&`, `=`, `+` und `;` stehen — in einem
// Pfad harmlos, in einer Abfrage (`?datei={pfad}`) aber ein Bruch. Die Vorlage
// kann beides sein, also wird nach der strengeren Stelle kodiert.
var pfadKodierer = strings.NewReplacer("&", "%26", "=", "%3D", "+", "%2B", ";", "%3B")

// BearbeitenLink setzt pfad URL-kodiert in die Vorlage ein. Die Schrägstriche
// zwischen den Ordnern bleiben stehen; jeder Teil wird einzeln kodiert.
func BearbeitenLink(vorlage, pfad string) string {
	if vorlage == "" {
		return ""
	}
	teile := strings.Split(pfad, "/")
	for i, t := range teile {
		teile[i] = pfadKodierer.Replace(url.PathEscape(t))
	}
	return strings.Replace(vorlage, PfadPlatzhalter, strings.Join(teile, "/"), 1)
}

// PruefeKeinServerOrdner (F-1887, Sprint-86-Kritiker K-B1): Der Wissens-Ordner
// darf weder ein Datenverzeichnis des Servers sein noch darin liegen noch eines
// enthalten. Sonst läse die Lesesicht etwa hochgeladene Anhänge privater Boards
// für jede Person mit Konto — an der Board-ACL vorbei. Leere Einträge zählen
// nicht. Verify V-1: Beide Seiten werden absolut gemacht und, soweit sie
// existieren, über Symlinks aufgelöst — ein relativer ABOARD_ATTACHMENTS_DIR
// oder ein Symlink /data/wissen → /data/attachments umgeht die Sperre nicht.
func PruefeKeinServerOrdner(dir string, gesperrt []string) error {
	d := echterPfad(dir)
	for _, g := range gesperrt {
		if strings.TrimSpace(g) == "" {
			continue
		}
		g = echterPfad(g)
		if d == g || strings.HasPrefix(d+"/", g+"/") || strings.HasPrefix(g+"/", d+"/") {
			return fmt.Errorf("%s: %s überschneidet sich mit dem Server-Datenordner %s (Datenbank, Anhänge oder Sicherungen) — bitte einen eigenen Ordner wählen", KeyDir, d, g)
		}
	}
	return nil
}

// echterPfad: absolut, bereinigt und — wenn vorhanden — ohne Symlinks.
// Existiert der Pfad (noch) nicht, gilt die absolute Form.
func echterPfad(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if echt, err := filepath.EvalSymlinks(p); err == nil {
		return echt
	}
	return filepath.Clean(p)
}
