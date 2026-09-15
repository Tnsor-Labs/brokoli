package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/sftpclient/sftptest"
)

// ADR-040 section 10: testing an sftp connection signs in and opens SFTP.
// It used to read the SSH banner and stop, so every case below except the
// unreachable one reported success.

func sftpConnFor(srv *sftptest.Server, extra map[string]interface{}) *models.Connection {
	b, _ := json.Marshal(extra)
	return &models.Connection{
		Type: models.ConnTypeSFTP, Host: srv.Host, Port: srv.Port, Schema: srv.Root,
		Login: srv.User, Password: srv.Password, Extra: string(b),
	}
}

func testSSHFor(c *models.Connection) map[string]interface{} {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return testSSH(ctx, c)
}

func TestSFTPConnectionTest(t *testing.T) {
	defer netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true})()
	srv := sftptest.Start(t, sftptest.Options{})
	good := map[string]interface{}{"host_key": srv.Fingerprint()}

	t.Run("working", func(t *testing.T) {
		res := testSSHFor(sftpConnFor(srv, good))
		if res["success"] != true || res["host_key"] != srv.Fingerprint() {
			t.Fatalf("%v", res)
		}
		if msg, _ := res["message"].(string); !strings.Contains(msg, "host key verified") {
			t.Fatalf("message %q", msg)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		c := sftpConnFor(srv, good)
		c.Password = "not-it"
		if res := testSSHFor(c); res["success"] != false {
			t.Fatalf("a wrong password tested green: %v", res)
		}
	})
	t.Run("unknown host key", func(t *testing.T) {
		res := testSSHFor(sftpConnFor(srv, map[string]interface{}{}))
		errMsg, _ := res["error"].(string)
		if res["success"] != false || res["host_key"] != srv.Fingerprint() || !strings.Contains(errMsg, srv.Fingerprint()) {
			t.Fatalf("an unknown host key must fail and name the presented key: %v", res)
		}
	})
	t.Run("missing base directory", func(t *testing.T) {
		c := sftpConnFor(srv, good)
		c.Schema = srv.Root + "/nope"
		if res := testSSHFor(c); res["success"] != false {
			t.Fatalf("a missing base directory tested green: %v", res)
		}
	})
	t.Run("skipped host key check says so", func(t *testing.T) {
		res := testSSHFor(sftpConnFor(srv, map[string]interface{}{"insecure_skip_host_key_check": true}))
		msg, _ := res["message"].(string)
		if res["success"] != true || !strings.Contains(msg, "NOT checked") {
			t.Fatalf("%v", res)
		}
	})
}

// The connection test honours the same outbound policy a run does.
func TestSFTPConnectionTestHonoursOutboundPolicy(t *testing.T) {
	defer netguard.SetOutboundForTesting(netguard.Policy{})()
	srv := sftptest.Start(t, sftptest.Options{})
	res := testSSHFor(sftpConnFor(srv, map[string]interface{}{"host_key": srv.Fingerprint()}))
	errMsg, _ := res["error"].(string)
	if res["success"] != false || !strings.Contains(errMsg, "blocked") || srv.Accepted() != 0 {
		t.Fatalf("loopback was reached without the operator's opt-in: %v (accepted %d)", res, srv.Accepted())
	}
}
