package mail

import "testing"

func TestFilterBlockedMIME(t *testing.T) {
	atts := []ParsedAttachment{
		{Filename: "harmless.pdf", ContentType: "application/pdf", Data: []byte("PDF")},
		{Filename: "malware.exe", ContentType: "application/octet-stream", Data: []byte("EVIL")},
		{Filename: "evil.SCR", ContentType: "x", Data: []byte("E")},
		{Filename: "script.bat", ContentType: "x", Data: []byte("@echo off")},
		{Filename: "win.dll", ContentType: "x", Data: []byte("D")},
		{Filename: "img.png", ContentType: "image/png", Data: []byte("PNG")},
		{Filename: "by-mime", ContentType: "application/javascript", Data: []byte("alert(1)")},
		{Filename: "ok.txt", ContentType: "text/plain", Data: []byte("hi")},
	}
	kept, dropped := FilterBlockedMIME(atts)
	if len(kept) != 3 {
		t.Errorf("kept=%d, erwartet 3 (pdf, png, txt)", len(kept))
	}
	if len(dropped) != 5 {
		t.Errorf("dropped=%d, erwartet 5", len(dropped))
	}
	for _, a := range dropped {
		if a.Data != nil {
			t.Errorf("dropped %q sollte Data=nil haben", a.Filename)
		}
	}
}

func TestFilterInlineTracking(t *testing.T) {
	tiny := make([]byte, 200) // < 1 KB
	big := make([]byte, 5000) // > 1 KB

	atts := []ParsedAttachment{
		{Filename: "pixel.gif", ContentType: "image/gif", Data: tiny, Inline: true, ContentID: "track1"},
		{Filename: "logo.png", ContentType: "image/png", Data: tiny, Inline: true, ContentID: "logo1"}, // referenziert
		{Filename: "bigchart.png", ContentType: "image/png", Data: big, Inline: true, ContentID: "chart"},
		{Filename: "attachment.pdf", ContentType: "application/pdf", Data: tiny, Inline: false},
	}
	body := "Hier ist <img src=\"cid:logo1\"> mein Logo."
	kept, dropped := FilterInlineTracking(atts, body)

	if len(kept) != 3 {
		t.Errorf("kept=%d, erwartet 3 (logo, big-chart, attachment.pdf)", len(kept))
	}
	if len(dropped) != 1 {
		t.Errorf("dropped=%d, erwartet 1 (pixel.gif)", len(dropped))
	}
	if dropped[0].Filename != "pixel.gif" {
		t.Errorf("dropped should be pixel.gif, got %q", dropped[0].Filename)
	}
}

func TestFilterInlineTracking_NonInlineUnberuehrt(t *testing.T) {
	atts := []ParsedAttachment{
		{Filename: "any.txt", Data: []byte("x"), Inline: false},
	}
	kept, dropped := FilterInlineTracking(atts, "")
	if len(kept) != 1 || len(dropped) != 0 {
		t.Errorf("non-inline darf nicht angefasst werden: kept=%d, dropped=%d", len(kept), len(dropped))
	}
}

func TestDedupAttachments(t *testing.T) {
	data := []byte("identical content")
	atts := []ParsedAttachment{
		{Filename: "logo.png", Data: data},
		{Filename: "logo.png", Data: data},                // duplikat
		{Filename: "other.png", Data: data},               // anderer Filename
		{Filename: "logo.png", Data: []byte("different")}, // selbes Filename, andere Data
	}
	kept, dropped := DedupAttachments(atts)
	if len(kept) != 3 {
		t.Errorf("kept=%d, erwartet 3 (1×logo.png-dup raus, 1×other, 1×diff-logo)", len(kept))
	}
	if len(dropped) != 1 {
		t.Errorf("dropped=%d, erwartet 1", len(dropped))
	}
}

func TestDedupAttachments_NilDataPassesThrough(t *testing.T) {
	atts := []ParsedAttachment{
		{Filename: "verworfen-a.exe", Data: nil},
		{Filename: "verworfen-b.exe", Data: nil},
	}
	kept, dropped := DedupAttachments(atts)
	if len(kept) != 2 || len(dropped) != 0 {
		t.Errorf("Data=nil-Anhänge sollen unverändert passieren: kept=%d, dropped=%d", len(kept), len(dropped))
	}
}
