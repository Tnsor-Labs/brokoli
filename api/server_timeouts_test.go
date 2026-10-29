package api

import (
	"net/http"
	"testing"
)

// IdleTimeout falls back to ReadTimeout, and neither was set, so a
// kept-alive connection that sent nothing more stayed open for ever.
func TestTheServerClosesIdleConnections(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())
	if srv.IdleTimeout <= 0 {
		t.Error("no idle timeout: idle keep-alive connections are never closed")
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("no header timeout: a client can hold a connection by sending headers slowly")
	}
}

// A read or write deadline would cut off long uploads, streamed downloads
// and event streams, so neither is set.
func TestTheServerSetsNoReadOrWriteDeadline(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Errorf("ReadTimeout %s, WriteTimeout %s: both must stay zero", srv.ReadTimeout, srv.WriteTimeout)
	}
}
