package sodp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// TestIsSODPVersionSupported exercises the version-comparison table
// directly, mirroring pkg/plugins/protocol_test.go's coverage of
// IsProtocolVersionSupported.
func TestIsSODPVersionSupported(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{ProtocolVersion, true},
		{"0.1", true},
		{"0.2", false},
		{"1.0", false},
		{"", false},
		{"not-a-version", false},
	}
	for _, tc := range cases {
		if got := IsSODPVersionSupported(tc.version); got != tc.want {
			t.Errorf("IsSODPVersionSupported(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

// TestHandleWS_UnsupportedVersionRejected verifies a client that declares
// an unsupported SODP protocol version via the sodp_version query
// parameter is rejected loudly — the WebSocket upgrade must fail outright
// with a clear error, not silently succeed regardless of version (the
// behavior before Tnsor-Labs/brokoli#11).
func TestHandleWS_UnsupportedVersionRejected(t *testing.T) {
	sodpSrv := NewServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws?" + SODPVersionParam + "=99.9"
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		conn.Close()
		t.Fatal("expected dial to fail for an unsupported declared sodp_version, it succeeded")
	}
	if resp == nil {
		t.Fatalf("expected an HTTP response accompanying the dial failure, got none (err: %v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("status = %d, want %d (StatusUpgradeRequired)", resp.StatusCode, http.StatusUpgradeRequired)
	}
	// The connection must never have been upgraded — no active session
	// should have been registered for a rejected handshake.
	if sodpSrv.sessionCount.Load() != 0 {
		t.Errorf("sessionCount = %d, want 0 — a rejected handshake must not register a session", sodpSrv.sessionCount.Load())
	}
}

// TestHandleWS_SupportedVersionAccepted verifies a client that explicitly
// declares a supported version connects normally and receives HELLO,
// exactly like a client that declares no version at all
// (TestIntegration_HelloOnConnect covers the latter).
func TestHandleWS_SupportedVersionAccepted(t *testing.T) {
	sodpSrv := NewServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws?" + SODPVersionParam + "=" + ProtocolVersion
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial with supported declared version: %v", err)
	}
	defer conn.Close()

	f := readFrame(t, conn)
	if f.Type != FrameHello {
		t.Fatalf("expected HELLO (0x01), got 0x%02x", f.Type)
	}
	m := bodyMap(t, f.Body)
	if m["version"] != ProtocolVersion {
		t.Errorf("HELLO version = %v, want %v", m["version"], ProtocolVersion)
	}
}
