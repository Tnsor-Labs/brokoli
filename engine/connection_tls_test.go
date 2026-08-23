package engine

import (
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// parsePGEnv turns BROKOLI_TEST_POSTGRES_URL into the connection-record fields
// an operator would type into the UI, so the test exercises the same path a
// stored connection takes rather than a hand-written DSN.
func pgConnRecord(t *testing.T) *models.Connection {
	t.Helper()
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("BROKOLI_TEST_POSTGRES_URL is not a URI: %v", err)
	}
	port := 5432
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	pw, _ := u.User.Password()
	return &models.Connection{
		Type:     models.ConnTypePostgres,
		Host:     u.Hostname(),
		Port:     port,
		Schema:   strings.TrimPrefix(u.Path, "/"),
		Login:    u.User.Username(),
		Password: pw,
	}
}

// Whether a connection is encrypted is a property of the live session, not of
// the DSN string, so ask the server. A unit test on the URI cannot tell the
// difference between "sslmode=require was written" and "sslmode=require was
// honoured"; against a server with TLS switched off those two have opposite
// outcomes, which is exactly the bug this closes.
func TestSSLModeIsHonouredByTheDriver(t *testing.T) {
	base := pgConnRecord(t)

	serverHasTLS := false
	{
		db, err := sql.Open("pgx", base.BuildURI())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var on string
		if err := db.QueryRow("SHOW ssl").Scan(&on); err != nil {
			t.Fatalf("connecting with the record as built: %v", err)
		}
		serverHasTLS = on == "on"
		t.Logf("server ssl = %s", on)
	}

	encrypted := func(t *testing.T, extra string) (bool, error) {
		c := *base
		c.Extra = extra
		db, err := sql.Open("pgx", c.BuildURI())
		if err != nil {
			return false, err
		}
		defer db.Close()
		var ssl sql.NullBool
		err = db.QueryRow("SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()").Scan(&ssl)
		return ssl.Bool, err
	}

	t.Run("no sslmode leaves it to the driver default", func(t *testing.T) {
		got, err := encrypted(t, "")
		if err != nil {
			t.Fatalf("a connection record with no options must still connect: %v", err)
		}
		t.Logf("default: encrypted=%v", got)
	})

	t.Run("sslmode=disable is honoured", func(t *testing.T) {
		got, err := encrypted(t, `{"sslmode":"disable"}`)
		if err != nil {
			t.Fatalf("sslmode=disable should connect: %v", err)
		}
		if got {
			t.Error("asked for sslmode=disable and got an encrypted session")
		}
	})

	t.Run("sslmode=require reaches the driver", func(t *testing.T) {
		got, err := encrypted(t, `{"sslmode":"require"}`)
		if serverHasTLS {
			if err != nil {
				t.Fatalf("server supports TLS but sslmode=require failed: %v", err)
			}
			if !got {
				t.Error("asked for sslmode=require and got an unencrypted session")
			}
			return
		}
		// The server refuses TLS. Before this change an operator had no way to
		// say "refuse to connect unencrypted", and the session came up in the
		// clear with no signal. Now the demand is expressible and the
		// connection fails instead of silently downgrading.
		if err == nil {
			t.Fatal("server has TLS off, yet sslmode=require connected anyway — the option is being dropped")
		}
		if !strings.Contains(err.Error(), "TLS") && !strings.Contains(err.Error(), "tls") {
			t.Fatalf("expected a TLS refusal, got: %v", err)
		}
		t.Logf("server has TLS off; sslmode=require correctly refused: %v", err)
	})

	t.Run("a bad sslmode fails loudly instead of downgrading", func(t *testing.T) {
		_, err := encrypted(t, `{"sslmode":"requrie"}`)
		if err == nil {
			t.Fatal("a misspelled sslmode connected anyway — the value was dropped, which is a silent downgrade")
		}
		t.Logf("misspelled sslmode rejected: %v", err)
	})
}
