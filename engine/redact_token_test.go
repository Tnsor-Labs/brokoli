package engine

import (
	"errors"
	"testing"
)

// A one-letter password mangled every word containing its letter (#780).
// The password is still removed wherever it stands on its own, at any
// length, and wherever a quoted DSN carries it.
func TestReplaceSecretTokenRemovesTheSecretNotItsLetters(t *testing.T) {
	cases := []struct{ msg, secret, want string }{
		// The live message from the issue: nothing is a password here.
		{`Snowflake authenticator "externalbrowser" is not supported; the only supported authenticator is "snowflake" (user and password)`, "p",
			`Snowflake authenticator "externalbrowser" is not supported; the only supported authenticator is "snowflake" (user and password)`},
		// On its own, at any length and in any position.
		{"password p rejected", "p", "password X rejected"},
		{"p", "p", "X"},
		{"p rejected", "p", "X rejected"},
		{"rejected p", "p", "rejected X"},
		{"password=p&user=u", "p", "password=X&user=u"},
		{`"p"`, "p", `"X"`},
		{"hunter2 rejected, retry hunter2", "hunter2", "X rejected, retry X"},
		// A secret whose edges are not letters or digits needs no boundary
		// on that side.
		{"login with p@ss w failed", "p@ss w", "login with X failed"},
		{"x!!y", "!!", "xXy"},
		// Glued to letters or digits on a word edge: another word.
		{"apple", "p", "apple"},
		{"p2", "p", "p2"},
		// An overlapping occurrence after a skipped one is still found.
		{"pp p", "p", "pp X"},
		{"11-1-1", "1-1", "11-X"},
		// Letters beyond ASCII count as letters.
		{"sécurité é", "é", "sécurité X"},
		{"", "p", ""},
		{"nothing here", "", "nothing here"},
	}
	for _, c := range cases {
		if got := replaceSecretToken(c.msg, c.secret, "X"); got != c.want {
			t.Errorf("replaceSecretToken(%q, %q) = %q, want %q", c.msg, c.secret, got, c.want)
		}
	}
}

func TestRedactDSNErrorKeepsTheMessageReadableForAShortPassword(t *testing.T) {
	dsn := "snowflake://u:p@acct/db"
	err := errors.New(`open "snowflake://u:p@acct/db": authenticator not supported; the only supported one is "snowflake" (user and password) for p`)
	want := `open "snowflake://u:xxxxx@acct/db": authenticator not supported; the only supported one is "snowflake" (user and password) for xxxxx`
	if got := RedactDSNError(err, dsn).Error(); got != want {
		t.Errorf("RedactDSNError = %q, want %q", got, want)
	}
}

func TestRedactSecretKeepsTheMessageReadableForAShortPassword(t *testing.T) {
	msg := `Snowflake authenticator "externalbrowser" is not supported; the only supported authenticator is "snowflake" (user and password); rejected p`
	want := `Snowflake authenticator "externalbrowser" is not supported; the only supported authenticator is "snowflake" (user and password); rejected [redacted]`
	if got := redactSecret(msg, "", "p"); got != want {
		t.Errorf("redactSecret = %q, want %q", got, want)
	}
}
