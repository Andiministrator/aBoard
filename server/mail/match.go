package mail

// match.go — Ticket-Tag-Erkennung + Server-weite Karten-Suche (#295, ADR-0093 §4).
//
// Erkennung läuft in zwei Stufen:
//   1. Subject-Match auf `[aBoard #NNN]` (case-insensitive)
//   2. Body-Fallback (erste Übereinstimmung im Body)
//
// Karten-Lookup iteriert alle Boards (BoardRepo.List) und parst deren
// state_json — die erste Karte mit `displayId === N` gewinnt. Eindeutigkeits-
// Caveat akzeptiert (ADR-0093 §4, Maintainer-Entscheid 2026-05-20).

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/Andiministrator/aBoard/server/storage"
)

// ticketTagRE matcht Ticket-Tags der Form `[aBoard #NNN]` (case-insensitive,
// Whitespace toleriert).
var ticketTagRE = regexp.MustCompile(`(?i)\[aBoard\s+#(\d+)\]`)

// FindTicketID sucht im Subject und (Fallback) im Body nach einem Ticket-Tag.
// Liefert (displayId, true) bei Treffer, sonst (0, false).
func FindTicketID(subject, body string) (int, bool) {
	if m := ticketTagRE.FindStringSubmatch(subject); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n, true
		}
	}
	if m := ticketTagRE.FindStringSubmatch(body); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n, true
		}
	}
	return 0, false
}

// CardLocation identifiziert eine Karte server-weit.
type CardLocation struct {
	BoardID   string
	SprintID  string
	CardID    string
	DisplayID int
	Title     string
}

// FindCardByDisplayID iteriert alle Boards und liefert die erste Karte mit
// passender displayId. Bei nichts Gefundenem: nil, nil (kein Fehler — der
// Caller routet die Mail dann ins Service-Board).
//
// Eindeutigkeits-Caveat (ADR-0093 §4): displayIds sind pro Board fortlaufend,
// nicht server-eindeutig. Bei Doppelvergabe gewinnt die alphabetisch erste
// Board-ID (BoardRepo.List sortiert nach updated_at desc — wir resortieren
// stable nach ID für deterministisches Verhalten).
func FindCardByDisplayID(ctx context.Context, boards *storage.BoardRepo, displayID int) (*CardLocation, error) {
	summaries, err := boards.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("match.FindCardByDisplayID: list: %w", err)
	}
	// Stable-Sort nach Board-ID für Determinismus
	sortedIDs := make([]string, 0, len(summaries))
	for _, s := range summaries {
		sortedIDs = append(sortedIDs, s.ID)
	}
	// In-place sort (klein genug, kein algorithmus-Aufwand-relevant)
	for i := 1; i < len(sortedIDs); i++ {
		for j := i; j > 0 && sortedIDs[j-1] > sortedIDs[j]; j-- {
			sortedIDs[j-1], sortedIDs[j] = sortedIDs[j], sortedIDs[j-1]
		}
	}

	for _, boardID := range sortedIDs {
		b, err := boards.Get(ctx, boardID)
		if err != nil {
			continue // Toleriert: Lookup-Fehler stoppt nicht den Worker
		}
		loc, found := findCardInState(b.StateJSON, displayID, boardID)
		if found {
			return loc, nil
		}
	}
	return nil, nil
}

// findCardInState parst state_json und sucht nach der Karte. Reine Funktion,
// damit testbar ohne DB.
func findCardInState(stateJSON []byte, displayID int, boardID string) (*CardLocation, bool) {
	var root struct {
		Board struct {
			Sprints []struct {
				ID    string `json:"id"`
				Cards []struct {
					ID        string  `json:"id"`
					DisplayID float64 `json:"displayId"`
					Title     string  `json:"title"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(stateJSON, &root); err != nil {
		return nil, false
	}
	for _, sp := range root.Board.Sprints {
		for _, c := range sp.Cards {
			if int(c.DisplayID) == displayID {
				return &CardLocation{
					BoardID:   boardID,
					SprintID:  sp.ID,
					CardID:    c.ID,
					DisplayID: displayID,
					Title:     c.Title,
				}, true
			}
		}
	}
	return nil, false
}
