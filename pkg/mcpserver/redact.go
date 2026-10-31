package mcpserver

import (
	"regexp"
	"strings"
)

// Redacted replaces a secret value in every tool result.
const Redacted = "[redacted]"

// sensitiveKey matches the names of values that are credentials: a node's
// password or headers, a pipeline's webhook token, a connection's secret.
// Matched on the whole key, any case, so "webhook_token", "apiKey" and
// "Authorization" all count.
var sensitiveKey = regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|api[_-]?key|authorization|private[_-]?key|credentials?|cookie|signature)`)

// reference is a value that names a secret rather than holding one: a
// variable or secret template ("{{ secret.db }}", "${DB_PASSWORD}"). It is
// kept, so an agent can see how a pipeline is wired.
var reference = regexp.MustCompile(`^\s*(\{\{.*\}\}|\$\{[^}]*\})\s*$`)

// urlPassword is the password of a URL with credentials in it
// ("postgres://user:pw@host/db").
var urlPassword = regexp.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`)

// bearer is a bearer or basic credential written into a string.
var bearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)

// redact returns v (decoded JSON: maps, slices, strings, ...) with every
// credential replaced by Redacted. v is not modified.
func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if sensitiveKey.MatchString(k) && !isReference(val) && !isEmpty(val) {
				out[k] = Redacted
				continue
			}
			out[k] = redact(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redact(val)
		}
		return out
	case string:
		return redactString(t)
	}
	return v
}

func redactString(s string) string {
	s = urlPassword.ReplaceAllString(s, "${1}"+Redacted+"@")
	return bearer.ReplaceAllStringFunc(s, func(m string) string {
		return strings.SplitN(m, " ", 2)[0] + " " + Redacted
	})
}

func isReference(v any) bool {
	s, ok := v.(string)
	return ok && reference.MatchString(s)
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return true // a flag ("has_password") holds no secret
	}
	return false
}

// RedactText removes credentials from text that will leave the server:
// passwords in URLs, and Bearer and Basic credentials. It is what every
// MCP result's text goes through, for other places that send run text to
// an agent (ADR-047's trigger events).
func RedactText(s string) string { return redactString(s) }

// Clip cuts s to n bytes, saying how much was cut, as MCP results are.
func Clip(s string, n int) string { return clip(s, n) }

// MaxText is how long a single text MCP returns may be.
const MaxText = maxText
