package api

// helpers_internal_test.go — Whitebox-Tests für unexportierte Helfer in
// helpers.go. Bewusst `package api` (nicht `api_test` wie die übrigen Tests
// im Paket) — writeError ist nicht exportiert und nur so direkt testbar.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWriteError_LogtRohfehlerAberNichtInAntwort sichert F-252 ab: der
// zugrundeliegende Fehler landet im Server-Log, die HTTP-Antwort bleibt bei
// der generischen msg — der Client bekommt NIE den Roh-Fehler zu sehen.
//
// GEGENPROBE (Karte #411): Entfernt man den log.Printf-Aufruf in writeError
// (bzw. das `if len(err) > 0 && err[0] != nil`-Gate), wird dieser Test rot,
// weil der Log-Buffer den Such-String nicht mehr enthält — die Antwort-
// Assertions blieben davon unberührt, das ist bewusst zwei getrennte Prüfungen.
func TestWriteError_LogtRohfehlerAberNichtInAntwort(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	rec := httptest.NewRecorder()
	rawErr := errors.New("sqlite: disk I/O error auf /var/lib/aboard/geheimer-pfad")
	writeError(rec, 500, "create failed", rawErr)

	// 1) Response bleibt generisch — der Roh-Fehler darf NICHT im Body landen.
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response nicht JSON: %v (body=%q)", err, rec.Body.String())
	}
	if body["error"] != "create failed" {
		t.Fatalf("response error = %q, erwartet %q", body["error"], "create failed")
	}
	if strings.Contains(rec.Body.String(), "disk I/O") || strings.Contains(rec.Body.String(), "geheimer-pfad") {
		t.Fatalf("Roh-Fehler ist in die HTTP-Antwort durchgesickert: %s", rec.Body.String())
	}
	if rec.Code != 500 {
		t.Fatalf("status = %d, erwartet 500", rec.Code)
	}

	// 2) Server-Log MUSS den Roh-Fehler enthalten — das ist der eigentliche Fix.
	if !strings.Contains(buf.String(), "disk I/O error") {
		t.Fatalf("Server-Log enthält den Roh-Fehler nicht: %q", buf.String())
	}
}

// TestWriteError_OhneErrLoggtNichts: die dreiargumentige Bestandsform (kein
// Roh-Fehler übergeben) darf KEIN Log-Rauschen erzeugen — sonst würde jede
// simple Validierungs-/Berechtigungsantwort (400/403 ohne Go-error dahinter)
// unnötig geloggt.
func TestWriteError_OhneErrLoggtNichts(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	rec := httptest.NewRecorder()
	writeError(rec, 400, "missing board id")

	if buf.Len() != 0 {
		t.Fatalf("Log sollte bei fehlendem err-Argument leer bleiben, ist aber: %q", buf.String())
	}
}

// TestWriteError_NilErrLoggtNichts: explizit ein nil-error übergeben (z. B.
// weil ein Aufrufer defensiv immer 4 Argumente reicht) darf ebenfalls nicht
// loggen — sonst stünde in jedem Log ein "<nil>"-Eintrag.
func TestWriteError_NilErrLoggtNichts(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	rec := httptest.NewRecorder()
	var nilErr error
	writeError(rec, 500, "get failed", nilErr)

	if buf.Len() != 0 {
		t.Fatalf("Log sollte bei nil-err leer bleiben, ist aber: %q", buf.String())
	}
}
