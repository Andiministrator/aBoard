package schleuse

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ADR-0147 (A-1): Die Ausnahme des Dialers gilt für Host UND Port.
func TestVersandTransport_InternerHostNurMitPort(t *testing.T) {
	var treffer atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { treffer.Add(1) }))
	defer srv.Close()
	basis := srv.Client().Transport.(*http.Transport).Clone()
	basis.Proxy = nil
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	senden := func(opt VersandOption) error {
		rt, err := VersandTransport(srv.URL, basis, 5*time.Second, opt)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := rt.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := senden(ErlaubeInternenHost(host, "1")); err == nil || treffer.Load() != 0 {
		t.Fatalf("falscher Port: Fehler %v, Treffer %d", err, treffer.Load())
	}
	if err := senden(ErlaubeInternenHost(host, port)); err != nil || treffer.Load() != 1 {
		t.Fatalf("passender Port: Fehler %v, Treffer %d", err, treffer.Load())
	}
}
