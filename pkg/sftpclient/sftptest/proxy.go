package sftptest

import (
	"context"
	"net"
	"sync"
	"testing"
)

// Proxy forwards TCP connections to a target until frozen, then goes
// silent in both directions while keeping the sockets open: a server that
// has stopped responding, as a client sees it.
type Proxy struct {
	Addr   string
	mu     sync.Mutex
	frozen bool
	stop   chan struct{}
	close  func()
}

// StartProxy forwards to target until the test ends.
func StartProxy(t testing.TB, target string) *Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{Addr: ln.Addr().String(), stop: make(chan struct{})}
	var cmu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			cmu.Lock()
			conns = append(conns, c, up)
			cmu.Unlock()
			go p.pipe(up, c)
			go p.pipe(c, up)
		}
	}()
	var once sync.Once
	p.close = func() {
		once.Do(func() {
			close(p.stop)
			_ = ln.Close()
			cmu.Lock()
			for _, c := range conns {
				_ = c.Close()
			}
			cmu.Unlock()
		})
	}
	t.Cleanup(p.Close)
	return p
}

// Close drops every connection through the proxy. A test that gives up
// on a blocked client calls it first, so the client's own cleanup is not
// left waiting on a connection nobody will ever answer.
func (p *Proxy) Close() { p.close() }

// Freeze stops forwarding in both directions, for good.
func (p *Proxy) Freeze() { p.mu.Lock(); p.frozen = true; p.mu.Unlock() }

// Dial connects to the proxy whatever address it is asked for, so a client
// configured for the real server goes through it.
func (p *Proxy) Dial(ctx context.Context, network, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, p.Addr)
}

func (p *Proxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			p.mu.Lock()
			frozen := p.frozen
			p.mu.Unlock()
			if frozen {
				<-p.stop
				return
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
