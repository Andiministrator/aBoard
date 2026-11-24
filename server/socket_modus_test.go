package main

// socket_modus_test.go — Karte #577: ABOARD_UNIX_SOCKET_MODE. Bis v1.42.0
// stand 0660 fest im Code; in einem Team-Betrieb laufen Proxy und aBoard in verschiedenen
// ID-Bereichen (UserNS=auto), dort reicht Besitzer+Gruppe nicht.

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestUnixSocketModus_Werte(t *testing.T) {
	faelle := []struct {
		roh           string
		modus         os.FileMode
		ausdruecklich bool
		fehler        bool
	}{
		{"", 0o660, false, false},
		{"   ", 0o660, false, false},
		{"0600", 0o600, true, false},
		{"660", 0o660, true, false},
		{"0666", 0o666, true, false},
		{" 0666 ", 0o666, true, false},
		{"0644", 0, false, true},
		{"0777", 0, false, true},
		{"rw", 0, false, true},
		{"0o666", 0, false, true},
	}
	for _, f := range faelle {
		t.Run(f.roh, func(t *testing.T) {
			t.Setenv("ABOARD_UNIX_SOCKET_MODE", f.roh)
			m, err := unixSocketModus()
			if f.fehler {
				if err == nil {
					t.Fatalf("%q angenommen, soll den Start abbrechen", f.roh)
				}
				if !strings.Contains(err.Error(), "ABOARD_UNIX_SOCKET_MODE") {
					t.Fatalf("Fehlertext nennt die Variable nicht: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.modus != f.modus || m.ausdruecklich != f.ausdruecklich {
				t.Fatalf("= %04o/%v, soll %04o/%v", m.modus, m.ausdruecklich, f.modus, f.ausdruecklich)
			}
		})
	}
}

func kurzesSocketVerzeichnis(t *testing.T) string {
	t.Helper()
	// Wie TestStarteAgentListener_UnixSocket: kurzer Pfad (AF_UNIX ≤ 108
	// Byte), nicht nach /tmp, sondern ins gitignorierte tmp/ der Repo-Wurzel.
	basis, err := filepath.Abs(filepath.Join("..", "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(basis, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(basis, "sm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Gegenprobe: vor #577 setzte der Agenten-Listener immer 0660.
func TestStarteAgentListener_SetztSocketModus(t *testing.T) {
	for _, modus := range []os.FileMode{0o600, 0o666} {
		sock := filepath.Join(kurzesSocketVerzeichnis(t), "a.sock")
		t.Setenv("ABOARD_AGENT_LISTEN", "")
		t.Setenv("ABOARD_AGENT_UNIX_SOCKET", sock)
		stop, err := starteAgentListener(http.NewServeMux(), api.NewAgentZugang(storage.NewAgentTokenRepo(nil)),
			":8080", "", socketModus{modus: modus, ausdruecklich: true}, make(chan error, 1))
		if err != nil || stop == nil {
			t.Fatalf("Start: %v", err)
		}
		info, err := os.Stat(sock)
		stop(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if ist := info.Mode().Perm(); ist != modus {
			t.Fatalf("Socket-Modus %04o, soll %04o", ist, modus)
		}
	}
}

func TestSocketModusAnwenden_FehlerNurBeiAusdruecklich(t *testing.T) {
	fehlt := filepath.Join(kurzesSocketVerzeichnis(t), "fehlt.sock")
	if err := (socketModus{modus: 0o660}).anwenden(fehlt); err != nil {
		t.Fatalf("Voreinstellung: %v — soll nur warnen wie bis v1.42.0", err)
	}
	if err := (socketModus{modus: 0o666, ausdruecklich: true}).anwenden(fehlt); err == nil {
		t.Fatal("ausdrücklich gesetzter Modus scheitert still")
	}
}

// Quelltext-Prüfung wie TestRun_RuftRunPreflightAuf: run() liest den Modus
// und bricht bei Fehler ab; kein Socket bekommt mehr einen festen Modus.
func TestRun_VerdrahtetSocketModus(t *testing.T) {
	ohneKommentare := func(datei string) string {
		q, err := os.ReadFile(datei)
		if err != nil {
			t.Fatal(err)
		}
		return regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(q), "")
	}
	main := ohneKommentare("main.go")
	if !regexp.MustCompile(`sockModus, err := unixSocketModus\(\)\s*if err != nil \{\s*return err`).MatchString(main) {
		t.Fatal("run() liest ABOARD_UNIX_SOCKET_MODE nicht (mehr) oder ignoriert den Fehler")
	}
	for _, datei := range []string{"main.go", "main_agent.go"} {
		if strings.Contains(ohneKommentare(datei), "os.Chmod(") {
			t.Fatalf("%s setzt einen Socket-Modus an socketModus.anwenden vorbei", datei)
		}
	}
	if !strings.Contains(main, "sockModus.anwenden(unixSocket)") {
		t.Fatal("Menschen-Socket bekommt den konfigurierten Modus nicht")
	}
	// Kritiker Sprint 74: anwenden steht seit F-1181 in starteMenschListener;
	// ohne diese Probe bliebe ein run(), das dort socketModus{} statt
	// sockModus übergibt, grün.
	if !regexp.MustCompile(`starteMenschListener\(srv, listenAddr, unixSocket, sockModus,`).MatchString(main) {
		t.Fatal("run() reicht den konfigurierten Socket-Modus nicht an starteMenschListener weiter")
	}
}

// Kritiker Sprint 73: Scheitert das chmod bei ausdrücklich gesetztem Modus,
// bricht der Start ab — und die Socket-Datei bleibt nicht liegen. Go entfernt
// eine per net.Listen("unix") angelegte Datei beim Close selbst; der Test
// hält fest, dass der Fehlerpfad dieses Close auch erreicht.
func TestStarteAgentListener_ChmodFehlerRaeumtSocketAb(t *testing.T) {
	alt := chmodSocket
	chmodSocket = func(string, os.FileMode) error { return os.ErrPermission }
	t.Cleanup(func() { chmodSocket = alt })

	sock := filepath.Join(kurzesSocketVerzeichnis(t), "a.sock")
	t.Setenv("ABOARD_AGENT_LISTEN", "")
	t.Setenv("ABOARD_AGENT_UNIX_SOCKET", sock)
	stop, err := starteAgentListener(http.NewServeMux(), api.NewAgentZugang(storage.NewAgentTokenRepo(nil)),
		":8080", "", socketModus{modus: 0o666, ausdruecklich: true}, make(chan error, 1))
	if err == nil {
		if stop != nil {
			stop(context.Background())
		}
		t.Fatal("chmod-Fehler bei ausdrücklichem Modus liess den Start zu")
	}
	if _, serr := os.Stat(sock); !os.IsNotExist(serr) {
		t.Fatalf("Socket-Datei nach gescheitertem Start noch da: %v", serr)
	}
}

// F-1181: derselbe Fehlerpfad am Menschen-Socket, zur Laufzeit statt nur als
// Quelltext-Prüfung. Scheitert das chmod bei ausdrücklich gesetztem Modus,
// startet nichts und die Socket-Datei bleibt nicht liegen.
//
// GEGENPROBE (Karte #584): Ohne `_ = ln.Close()` im chmod-Fehlerzweig von
// starteMenschListener bleibt die Socket-Datei stehen, der Test wird rot.
func TestStarteMenschListener_ChmodFehlerRaeumtSocketAb(t *testing.T) {
	alt := chmodSocket
	chmodSocket = func(string, os.FileMode) error { return os.ErrPermission }
	t.Cleanup(func() { chmodSocket = alt })

	sock := filepath.Join(kurzesSocketVerzeichnis(t), "m.sock")
	srv := &http.Server{Handler: http.NewServeMux()}
	err := starteMenschListener(srv, ":8080", sock, socketModus{modus: 0o666, ausdruecklich: true}, "test", make(chan error, 1))
	if err == nil {
		_ = srv.Close()
		t.Fatal("chmod-Fehler bei ausdrücklichem Modus liess den Start zu")
	}
	if _, serr := os.Stat(sock); !os.IsNotExist(serr) {
		t.Fatalf("Socket-Datei nach gescheitertem Start noch da: %v", serr)
	}
}

// F-1181, Normalfall: Der Socket entsteht mit dem konfigurierten Modus, der
// Server antwortet darauf, und nach Shutdown meldet die Goroutine nil.
func TestStarteMenschListener_SocketMitModus(t *testing.T) {
	sock := filepath.Join(kurzesSocketVerzeichnis(t), "m.sock")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	srv := &http.Server{Handler: mux}
	fehler := make(chan error, 1)
	if err := starteMenschListener(srv, ":8080", sock, socketModus{modus: 0o600, ausdruecklich: true}, "test", fehler); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("Socket fehlt: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("Modus %v, erwartet 0600", fi.Mode().Perm())
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	resp, err := client.Get("http://sock/healthz")
	if err != nil {
		t.Fatalf("Anfrage über den Socket: %v", err)
	}
	_ = resp.Body.Close()
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-fehler; err != nil {
		t.Fatalf("Serve endete mit %v, erwartet nil", err)
	}
}
