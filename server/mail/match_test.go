package mail

import "testing"

func TestFindTicketID(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		body    string
		wantID  int
		wantOk  bool
	}{
		{"subject-basic", "[aBoard #42] Bug", "no body", 42, true},
		{"subject-lowercase", "[aboard #7] x", "", 7, true},
		{"subject-whitespace", "Re: [aBoard   #100] forwarded", "", 100, true},
		{"body-fallback", "Re: Generic Subject", "Some context [aBoard #99] here", 99, true},
		{"subject-wins-over-body", "[aBoard #1] hi", "later [aBoard #999]", 1, true},
		{"no-tag", "Just a normal subject", "no tag here either", 0, false},
		{"malformed-no-space", "[aBoard#42]", "", 0, false},
		{"malformed-no-hash", "[aBoard 42]", "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, ok := FindTicketID(c.subject, c.body)
			if id != c.wantID || ok != c.wantOk {
				t.Errorf("FindTicketID(%q, %q) = (%d, %v), want (%d, %v)",
					c.subject, c.body, id, ok, c.wantID, c.wantOk)
			}
		})
	}
}

func TestFindCardInState(t *testing.T) {
	state := []byte(`{
		"version":"2","board":{"id":"b1","sprints":[
			{"id":"s1","cards":[
				{"id":"c-a","displayId":1,"title":"erste"},
				{"id":"c-b","displayId":42,"title":"Treffer"}
			]},
			{"id":"s2","cards":[
				{"id":"c-c","displayId":99,"title":"woanders"}
			]}
		]}
	}`)

	loc, found := findCardInState(state, 42, "b1")
	if !found {
		t.Fatalf("Karte #42 nicht gefunden")
	}
	if loc.CardID != "c-b" || loc.SprintID != "s1" || loc.Title != "Treffer" {
		t.Errorf("unerwarteter Location: %+v", loc)
	}

	if _, found := findCardInState(state, 12345, "b1"); found {
		t.Errorf("nicht-existenter displayId sollte nicht gefunden werden")
	}
}

func TestFindCardInState_KaputtesJSON(t *testing.T) {
	if _, found := findCardInState([]byte("{garbage"), 1, "b1"); found {
		t.Errorf("kaputtes JSON sollte not-found liefern, kein Crash")
	}
}
