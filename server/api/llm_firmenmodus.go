package api

// llm_firmenmodus.go — Firmenmodus in den LLM-Handlern (Karte #664,
// ADR-0133 §2 + Nachtrag 2026-10-04 abends).
//
// Ohne Firmenmodus (firmenmodus == nil, pii == "" oder "nutzer") ändern die
// Helfer hier nichts am bisherigen Verhalten: sendeSchluessel liest wie vorher
// den Nutzerschlüssel, pruefeBetreiberVorgaben lässt alles durch,
// anbieterNutzerFeld liefert "" (kein `user`-Feld im Body).
//
// Im Firmenmodus:
//   - Der Serverschlüssel ersetzt die Nutzerschlüssel; gespeicherte werden
//     weder gelesen noch gelöscht.
//   - Nur Modelle aus ABOARD_LLM_OPENAI_MODELLE (Nachtrag A1/A2).
//   - Die E-Mail der Person reist als OpenAI-`user`-Feld mit.
//   - Keine Antwort als Kartenkommentar, kein llm-sent-Aktivitätseintrag
//     (Karte #674, A5).
//
// Der Schlüssel erscheint in keiner Antwort und keinem Log: Keine Fehlermeldung
// hier nennt ihn, und llm.Firmenmodus schwärzt sich bei %v/%#v selbst.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// KindVomBetreiber — Fehlerart, wenn ein Nutzer im Firmenmodus einen eigenen
// Schlüssel speichern oder testen will.
const KindVomBetreiber = "vom-betreiber-eingerichtet"

// KindModellNichtErlaubt — Modell steht nicht in ABOARD_LLM_OPENAI_MODELLE.
const KindModellNichtErlaubt = "model-not-allowed"

// KindPIIPflicht — Betreiber verlangt den PII-Filter, der Client meldet ihn
// nicht als aktiv.
const KindPIIPflicht = "pii-pflicht"

// WithFirmenmodus trägt Firmenmodus (nil = aus) und PII-Vorgabe ein.
func (a *LLMSendAPI) WithFirmenmodus(f *llm.Firmenmodus, pii llm.PIIVorgabe) *LLMSendAPI {
	a.firmenmodus = f
	a.pii = pii
	return a
}

// WithFirmenmodus trägt Firmenmodus (nil = aus) und PII-Vorgabe ein. Die
// Oberfläche erfährt beides über GET /users/me/llm-keys.
func (a *LLMKeysAPI) WithFirmenmodus(f *llm.Firmenmodus, pii llm.PIIVorgabe) *LLMKeysAPI {
	a.firmenmodus = f
	a.pii = pii
	return a
}

// pruefeBetreiberVorgaben prüft Modell-Allowlist und PII-Pflicht. false →
// die Fehlerantwort ist geschrieben. Läuft VOR Kartensperre, Schlüssel und
// Anbieter-Aufruf.
func (a *LLMSendAPI) pruefeBetreiberVorgaben(w http.ResponseWriter, modell string, piiFilterGemeldet bool) bool {
	if !a.firmenmodus.ModellErlaubt(modell) {
		writeSendError(w, http.StatusBadRequest, KindModellNichtErlaubt,
			fmt.Sprintf("model %q is not enabled by the operator", modell))
		return false
	}
	// „pflicht“: Der Server kann den Filter nicht selbst ausführen (er läuft
	// im Browser). Er kann aber einen Client abweisen, der nicht filtert —
	// etwa eine alte, zwischengespeicherte Oberfläche, die die Vorgabe nicht
	// kennt.
	if a.pii == llm.PIIPflicht && !piiFilterGemeldet {
		writeSendError(w, http.StatusBadRequest, KindPIIPflicht,
			"the operator requires the PII filter for every send")
		return false
	}
	return true
}

// sendeSchluessel liefert den Schlüssel für diesen Send. false → die
// Fehlerantwort ist geschrieben.
func (a *LLMSendAPI) sendeSchluessel(w http.ResponseWriter, r *http.Request, userID, provider string) (string, bool) {
	if a.firmenmodus.Aktiv() {
		return a.firmenmodus.Schluessel(), true
	}
	apiKey, err := a.keys.Get(r.Context(), userID, provider)
	if errors.Is(err, storage.ErrLLMKeyNotFound) {
		writeSendError(w, http.StatusPreconditionFailed, "invalid-key", "no api key stored for provider")
		return "", false
	}
	if err != nil {
		writeSendError(w, http.StatusInternalServerError, "internal", "llm-keys read failed")
		return "", false
	}
	return apiKey, true
}

// markiereSchluesselBenutzt setzt last_used_at — nur für Nutzerschlüssel.
func (a *LLMSendAPI) markiereSchluesselBenutzt(ctx context.Context, userID, provider string) {
	if a.firmenmodus.Aktiv() || a.keys == nil {
		return
	}
	_ = a.keys.MarkUsed(ctx, userID, provider)
}

// antwortAlsKommentar meldet, ob die KI-Antwort als Kartenkommentar samt
// llm-sent-Aktivitätseintrag gespeichert wird (ADR-0095). Im Firmenmodus
// nicht (Karte #674, ADR-0133 Nachtrag 2 A5): Kommentar und Aktivität sähen
// alle mit Zugriff auf das Board, auch über den Ereignisstrom. Die Antwort
// lebt dann nur im Stream und im privaten Verlauf.
func (a *LLMSendAPI) antwortAlsKommentar() bool {
	return !a.firmenmodus.Aktiv()
}

// anbieterNutzerFeld liefert die E-Mail fürs OpenAI-`user`-Feld — nur im
// Firmenmodus (ADR-0133 §2: fürs Protokoll beim Anbieter, keine Berechtigung).
func (a *LLMSendAPI) anbieterNutzerFeld(email string) string {
	if !a.firmenmodus.Aktiv() {
		return ""
	}
	return email
}

// firmenmodusAntwort ist der Teil von GET /users/me/llm-keys, der der
// Oberfläche den Firmenmodus meldet: Ziel (Host), Anbieter und Modelle.
func (a *LLMKeysAPI) firmenmodusAntwort() map[string]any {
	ziel := "api.openai.com"
	if a.openAIBasis != "" {
		ziel = hostVon(a.openAIBasis)
	}
	return map[string]any{
		"ziel":     ziel,
		"anbieter": []string{llm.FirmenmodusAnbieter},
		"modelle":  a.firmenmodus.Modelle(),
	}
}

// hostVon liefert nur den Host einer (geprüften) Basis-URL — mehr braucht die
// Oberfläche nicht. Unlesbar → die Basis selbst (sie ist ohne Zugangsdaten
// geprüft, siehe llm.ValidateOpenAIBaseURL).
func hostVon(basis string) string {
	if u, err := url.Parse(basis); err == nil && u.Host != "" {
		return u.Host
	}
	return basis
}
