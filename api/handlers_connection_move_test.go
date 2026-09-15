package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// A stored secret belongs to the server it was entered for. An editor
// cannot read it back, and must not be able to repoint the connection at
// a server of their own and have the next test or run send it there.
func TestMovingAConnectionRequiresItsSecretsAgain(t *testing.T) {
	h, s := connRoundTripEnv(t)
	r := routeConn(h)
	orig := models.Connection{
		ConnID: "partner", Type: models.ConnTypeSFTP, Host: "sftp.partner.example", Port: 22,
		Login: "u", Password: "s3cret", Extra: `{"host_key": "SHA256:abc"}`,
	}
	if w := doJSON(t, r, http.MethodPost, "/api/connections", orig); w.Code >= 300 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	put := func(c models.Connection) (int, string) {
		w := doJSON(t, r, http.MethodPut, "/api/connections/partner", c)
		return w.Code, w.Body.String()
	}
	moved := orig
	moved.Password, moved.Extra = "", ""

	for name, change := range map[string]func(*models.Connection){
		"host": func(c *models.Connection) { c.Host = "attacker.example" },
		"port": func(c *models.Connection) { c.Port = 2222 },
		"type": func(c *models.Connection) { c.Type = models.ConnTypeGeneric },
	} {
		c := moved
		change(&c)
		if code, body := put(c); code != http.StatusBadRequest || !strings.Contains(body, "enter the password again") {
			t.Errorf("%s changed, secrets blank: %d %s", name, code, body)
		}
	}
	c := moved
	c.Host = "attacker.example"
	c.Password = "new"
	if code, body := put(c); code != http.StatusBadRequest || !strings.Contains(body, "extra settings are not kept") {
		t.Errorf("host changed, extra blank: %d %s", code, body)
	}
	stored, err := s.GetConnection("partner")
	if err != nil || stored.Host != "sftp.partner.example" {
		t.Fatalf("a refused update changed the stored connection: %+v %v", stored, err)
	}

	// Unchanged target: blank secrets still mean "keep them".
	same := moved
	same.Login = "u2"
	if code, body := put(same); code != http.StatusOK {
		t.Fatalf("same server, secrets blank: %d %s", code, body)
	}
	if stored, _ := s.GetConnection("partner"); stored.PasswordRef == "" || stored.ExtraRef == "" {
		t.Fatalf("secrets dropped on an update that kept the server: %+v", stored)
	}

	// Moving with both entered again is fine.
	c.Extra = `{"host_key": "SHA256:def"}`
	if code, body := put(c); code != http.StatusOK {
		t.Fatalf("moved with secrets re-entered: %d %s", code, body)
	}
}

// A database's extra settings are driver options, not credentials, so a
// new host keeps them; its password still has to be entered again.
func TestMovingADatabaseKeepsDriverOptionsNotThePassword(t *testing.T) {
	h, s := connRoundTripEnv(t)
	r := routeConn(h)
	orig := models.Connection{
		ConnID: "wh", Type: models.ConnTypePostgres, Host: "db.example.com", Port: 5432,
		Login: "etl", Password: "s3cret", Extra: `{"sslmode":"require"}`,
	}
	if w := doJSON(t, r, http.MethodPost, "/api/connections", orig); w.Code >= 300 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	moved := orig
	moved.Host, moved.Password, moved.Extra = "db2.example.com", "", ""
	if w := doJSON(t, r, http.MethodPut, "/api/connections/wh", moved); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "enter the password again") {
		t.Fatalf("new host, password blank: %d %s", w.Code, w.Body)
	}
	moved.Password = "new"
	if w := doJSON(t, r, http.MethodPut, "/api/connections/wh", moved); w.Code != http.StatusOK {
		t.Fatalf("new host, password re-entered: %d %s", w.Code, w.Body)
	}
	after, _ := s.GetConnection("wh")
	if got := decryptRefForTest(t, h, after.ExtraRef); got != `{"sslmode":"require"}` {
		t.Fatalf("driver options lost on a move: %q", got)
	}
}
