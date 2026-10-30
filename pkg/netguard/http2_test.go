package netguard

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// h2Server is a TLS server offering HTTP/2 that counts the connections
// opened to it and records the protocol of each request, with a client
// from policy that trusts its certificate.
func h2Server(t *testing.T, policy Policy) (srv *httptest.Server, client *http.Client, conns *atomic.Int32, protos chan int) {
	t.Helper()
	conns = &atomic.Int32{}
	protos = make(chan int, 64)
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protos <- r.ProtoMajor
		_, _ = io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client = policy.Client(0)
	BaseTransport(client).TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	return srv, client, conns, protos
}

// A server offering HTTP/2 is spoken to over it, and concurrent requests
// share one connection. The custom dialer had switched HTTP/2 off, so
// each request in flight opened, and handshook, a connection of its own.
func TestClientSpeaksHTTP2AndMultiplexes(t *testing.T) {
	srv, client, conns, protos := h2Server(t, Policy{AllowLoopback: true})

	// One request first, so the connection exists before the burst.
	get := func() error {
		resp, err := client.Get(srv.URL)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := get(); err != nil {
		t.Fatal(err)
	}
	const burst = 8
	var wg sync.WaitGroup
	errs := make(chan error, burst)
	for range burst {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- get() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	close(protos)
	for p := range protos {
		if p != 2 {
			t.Fatalf("request arrived over HTTP/%d, want HTTP/2", p)
		}
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("%d connections for %d requests, want 1 multiplexed connection", n, burst+1)
	}
}

// HTTP/2 must not get round the policy: its connections are dialed
// through the same check, so a blocked target stays blocked.
func TestHTTP2StillDialsThroughThePolicy(t *testing.T) {
	srv, client, conns, _ := h2Server(t, Policy{AllowLoopback: false})
	_, err := client.Get(srv.URL)
	if !errors.Is(err, ErrBlockedTarget) {
		t.Errorf("err = %v, want the loopback server refused by the policy", err)
	}
	if n := conns.Load(); n != 0 {
		t.Errorf("%d connection(s) reached the blocked server", n)
	}
}
