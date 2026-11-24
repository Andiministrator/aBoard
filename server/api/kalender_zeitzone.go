package api

// kalender_zeitzone.go — Instanz-Zeitzone für Uhrzeiten im Abo-Feed
// (ADR-0138 §2, Karte #742).
//
// Uhrzeiten an Karten sind Ortszeit der Instanz. Nur der Feed rechnet um: Datum
// und Uhrzeit in dieser Zone → UTC-DATE-TIME. Die Einstellung liegt in
// server_config und wirkt beim nächsten Feed-Abruf, ohne Neustart.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// KonfigKalenderZeitzone ist der server_config-Schlüssel (IANA-Name).
	KonfigKalenderZeitzone = "kalender.zeitzone"
	// StandardKalenderZeitzone gilt, solange nichts eingetragen ist.
	StandardKalenderZeitzone = "Europe/Berlin"
)

// ladeZeitzone löst einen IANA-Namen auf. Leer → Standard. Fehlt die Zone in
// den Zeitzonendaten oder ist der Name unbekannt, kommt UTC zurück und der
// Fehler nennt den Namen — der Feed läuft dann weiter, nur eben in UTC.
func ladeZeitzone(name string) (*time.Location, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = StandardKalenderZeitzone
	}
	// "Local" hiesse: die Zone des Containers — die ist nicht eingestellt,
	// sondern zufällig. Nicht zulassen.
	if name == "Local" {
		return time.UTC, "UTC", fmt.Errorf("zeitzone %q ist keine IANA-Zone (z. B. Europe/Berlin) — der Feed rechnet in UTC", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC, "UTC", fmt.Errorf("zeitzone %q unbekannt — der Feed rechnet in UTC: %w", name, err)
	}
	return loc, name, nil
}

// KalenderZeitzone liest die Instanz-Zeitzone aus server_config. cfg == nil
// (Tests, Aufbau ohne Konfig-Store) → Standard.
func KalenderZeitzone(ctx context.Context, cfg *storage.ConfigRepo) (*time.Location, string, error) {
	if cfg == nil {
		return ladeZeitzone("")
	}
	v, err := cfg.Get(ctx, KonfigKalenderZeitzone)
	if err != nil {
		if errors.Is(err, storage.ErrConfigKeyNotFound) {
			return ladeZeitzone("")
		}
		// Lesefehler der DB: nicht auf einen Standard ausweichen, der still
		// falsche Uhrzeiten liefert — UTC und melden.
		return time.UTC, "UTC", fmt.Errorf("zeitzone nicht lesbar — der Feed rechnet in UTC: %w", err)
	}
	return ladeZeitzone(v.Value)
}

// KalenderZeitzonenTest ist der Test-Hook der Admin-Sektion „kalender“: prüft
// den eingetragenen (oder im Formular gespeicherten) Namen und nennt die
// aktuelle Ortszeit dort, damit ein Tippfehler sofort auffällt.
func KalenderZeitzonenTest(_ context.Context, cfg map[string]string) (any, error) {
	loc, name, err := ladeZeitzone(cfg[KonfigKalenderZeitzone])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"zeitzone": name,
		"ortszeit": time.Now().In(loc).Format("2006-01-02 15:04 MST"),
	}, nil
}
