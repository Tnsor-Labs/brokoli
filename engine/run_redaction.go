package engine

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Resolved credentials are redacted from what a run records (ADR-041
// section 7, #782).
//
// Every credential value a run resolves from a reference is added to that
// run's redaction set: a connection's password, and the credential-like
// values of a resolved extra document. Node log lines, node and run
// errors, the events that carry them, and recorded SQL pass through the
// set before they are stored or sent, so a driver that quotes its DSN, or
// a node that echoes its configuration, does not put the plaintext where
// everyone who can view the run reads it.
//
// Values shorter than minMaskableSecretBytes are not redacted: a short
// value occurs all over ordinary text, and masking every occurrence makes
// a log unreadable without showing that the secret was removed (#780 is
// that failure in a driver's error path). The same floor as recorded SQL.
//
// A resolved extra document mixes credentials with settings (sslmode,
// region, bucket). Only the values under credential-like keys are added;
// redacting every string in it would mask "disable" and "us-east-1" across
// the run's logs. Per-field references (ADR-041 section 2) make each
// secret its own resolved value, redacted exactly.

var runRedactions sync.Map // run ID -> *redactionSet

type redactionSet struct {
	mu     sync.RWMutex
	values []string // longest first, so a value containing another is masked whole
}

// addRunSecret records a resolved credential for runID. A no-op without a
// run (code resolving outside one) or for a value too short to mask.
func addRunSecret(runID, value string) {
	if runID == "" || len(value) < minMaskableSecretBytes {
		return
	}
	v, _ := runRedactions.LoadOrStore(runID, &redactionSet{})
	set := v.(*redactionSet)
	set.mu.Lock()
	defer set.mu.Unlock()
	for _, form := range secretForms(value) {
		if len(form) < minMaskableSecretBytes || containsString(set.values, form) {
			continue
		}
		set.values = append(set.values, form)
	}
	sort.SliceStable(set.values, func(i, j int) bool { return len(set.values[i]) > len(set.values[j]) })
}

// addRunExtraSecrets records the credential-like values of a resolved
// extra document. A document that is not a JSON object is a credential as
// a whole.
func addRunExtraSecrets(runID, extra string) {
	if runID == "" || strings.TrimSpace(extra) == "" {
		return
	}
	var doc interface{}
	if err := json.Unmarshal([]byte(extra), &doc); err != nil {
		addRunSecret(runID, extra)
		return
	}
	obj, ok := doc.(map[string]interface{})
	if !ok {
		addRunSecret(runID, extra)
		return
	}
	walkCredentialValues(obj, func(v string) { addRunSecret(runID, v) })
}

func walkCredentialValues(obj map[string]interface{}, add func(string)) {
	for key, raw := range obj {
		switch v := raw.(type) {
		case string:
			if credentialKey(key) {
				add(v)
				// An Authorization-style value is a scheme and a
				// credential ("Bearer <token>"); what leaks into an error
				// or a URL is the credential alone.
				if _, cred, ok := strings.Cut(v, " "); ok && authScheme(v) {
					add(strings.TrimSpace(cred))
				}
			}
		case map[string]interface{}:
			if credentialKey(key) {
				// A nested credential document (a service-account key
				// under "credentials") is a secret as a whole, and so is
				// every string in it.
				if b, err := json.Marshal(v); err == nil {
					add(string(b))
				}
				walkAllStrings(v, add)
			} else {
				walkCredentialValues(v, add)
			}
		}
	}
}

func walkAllStrings(obj map[string]interface{}, add func(string)) {
	for _, raw := range obj {
		switch v := raw.(type) {
		case string:
			add(v)
		case map[string]interface{}:
			walkAllStrings(v, add)
		}
	}
}

// authScheme reports whether v starts with an HTTP authorization scheme.
func authScheme(v string) bool {
	scheme, _, _ := strings.Cut(v, " ")
	switch strings.ToLower(scheme) {
	case "bearer", "basic", "token", "apikey", "api-key", "digest", "negotiate":
		return true
	}
	return false
}

// credentialKey reports whether an extra key names a credential.
func credentialKey(key string) bool {
	k := strings.ToLower(key)
	for _, word := range []string{"password", "passwd", "passphrase", "secret", "token", "credential",
		"private_key", "privatekey", "api_key", "apikey", "access_key", "sas", "auth"} {
		if strings.Contains(k, word) {
			return true
		}
	}
	// "key" on its own (Azure Blob's account key), or as a suffix
	// (host_key, account_key), but not key_columns-style settings.
	return k == "key" || strings.HasSuffix(k, "_key") || strings.HasSuffix(k, "key")
}

// secretForms are the spellings a value takes where it is embedded: as
// is, and URL-escaped the ways a connection URI or a query carries it.
func secretForms(value string) []string {
	forms := []string{value}
	userinfo := strings.TrimPrefix(url.UserPassword("", value).String(), ":")
	for _, f := range []string{userinfo, url.QueryEscape(value), url.PathEscape(value)} {
		if f != value && !containsString(forms, f) {
			forms = append(forms, f)
		}
	}
	return forms
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// redactRun masks runID's resolved credentials in msg.
func redactRun(runID, msg string) string {
	if runID == "" || msg == "" {
		return msg
	}
	v, ok := runRedactions.Load(runID)
	if !ok {
		return msg
	}
	set := v.(*redactionSet)
	set.mu.RLock()
	defer set.mu.RUnlock()
	for _, s := range set.values {
		if strings.Contains(msg, s) {
			msg = strings.ReplaceAll(msg, s, recordedSecretMask)
		}
	}
	return msg
}

// runSecrets returns runID's redaction set, for recorded SQL.
func runSecrets(runID string) []string {
	v, ok := runRedactions.Load(runID)
	if !ok {
		return nil
	}
	set := v.(*redactionSet)
	set.mu.RLock()
	defer set.mu.RUnlock()
	return append([]string(nil), set.values...)
}

// dropRunRedactions forgets runID's resolved credentials when the run ends.
func dropRunRedactions(runID string) {
	if runID != "" {
		runRedactions.Delete(runID)
	}
}
