package api

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// ─── sanitizeWorkspaceID ────────────────────────────────────────────────────

func TestSanitizeWorkspaceID_ValidIDs(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"my-workspace", "my-workspace"},
		{"ws_123", "ws_123"},
		{"TeamAlpha", "TeamAlpha"},
		{"abc-def-123_456", "abc-def-123_456"},
	}
	for _, tc := range tests {
		result := sanitizeWorkspaceID(tc.input)
		if result != tc.expected {
			t.Errorf("sanitizeWorkspaceID(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestSanitizeWorkspaceID_InjectionAttempts(t *testing.T) {
	tests := []struct {
		input    string
		contains string // should NOT contain
	}{
		{"ws'; DROP TABLE--", ";"},
		{"ws<script>", "<"},
		{"ws\x00null", "\x00"},
		{"ws/../../../etc", "/"},
		{"ws&param=value", "&"},
	}
	for _, tc := range tests {
		result := sanitizeWorkspaceID(tc.input)
		if strings.Contains(result, tc.contains) {
			t.Errorf("sanitizeWorkspaceID(%q) = %q, still contains %q", tc.input, result, tc.contains)
		}
	}
}

func TestSanitizeWorkspaceID_EmptyAfterSanitize(t *testing.T) {
	// If all characters are stripped, should return default
	result := sanitizeWorkspaceID("!!!@@@###")
	if result != models.DefaultWorkspaceID {
		t.Errorf("fully-invalid workspace ID should return default, got %q", result)
	}
}

func TestSanitizeWorkspaceID_Empty(t *testing.T) {
	result := sanitizeWorkspaceID("")
	if result != models.DefaultWorkspaceID {
		t.Errorf("empty workspace ID should return default, got %q", result)
	}
}

// ─── sanitizeRunError ───────────────────────────────────────────────────────

func TestSanitizeRunError_ConnectionStrings(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		mustMask []string // substrings that must NOT appear in output
		mustHave []string // substrings that MUST appear in output
	}{
		{
			name:     "postgres connection string",
			input:    "connection failed: postgres://admin:secret@db.internal:5432/mydb",
			mustMask: []string{"secret", "db.internal", "admin"},
			mustHave: []string{"postgres://****"},
		},
		{
			name:     "mysql connection string",
			input:    "error connecting to mysql://root:p@ssw0rd@mysql-host:3306/app",
			mustMask: []string{"p@ssw0rd", "mysql-host"},
			mustHave: []string{"mysql://****"},
		},
		{
			name:     "redis connection string",
			input:    "redis timeout: redis://user:pwd@redis-cache:6379/0",
			mustMask: []string{"pwd", "redis-cache"},
			mustHave: []string{"redis://****"},
		},
		{
			name:     "mongodb connection string",
			input:    "failed: mongodb://admin:pass@mongo.internal:27017/db?authSource=admin",
			mustMask: []string{"pass", "mongo.internal"},
			mustHave: []string{"mongodb://****"},
		},
		{
			name:     "sqlite path",
			input:    "open failed: sqlite:///var/lib/secret/data.db",
			mustMask: []string{},
			mustHave: []string{"sqlite://****"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := sanitizeRunError(tc.input)
			for _, s := range tc.mustMask {
				if strings.Contains(result, s) {
					t.Errorf("result should not contain %q, got: %q", s, result)
				}
			}
			for _, s := range tc.mustHave {
				if !strings.Contains(result, s) {
					t.Errorf("result should contain %q, got: %q", s, result)
				}
			}
		})
	}
}

func TestSanitizeRunError_FilePaths(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		mustMask []string
	}{
		{
			name:     "etc passwd",
			input:    "file not found: /etc/passwd",
			mustMask: []string{"/etc/passwd"},
		},
		{
			name:     "home directory",
			input:    "error reading /home/deploy/.env",
			mustMask: []string{"/home/deploy/.env"},
		},
		{
			name:     "var log",
			input:    "cannot open /var/log/syslog",
			mustMask: []string{"/var/log/syslog"},
		},
		{
			name:     "usr path",
			input:    "binary not found: /usr/local/bin/python3",
			mustMask: []string{"/usr/local/bin/python3"},
		},
		{
			name:     "root ssh key",
			input:    "permission denied: /root/.ssh/id_rsa",
			mustMask: []string{"/root/.ssh/id_rsa"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := sanitizeRunError(tc.input)
			for _, s := range tc.mustMask {
				if strings.Contains(result, s) {
					t.Errorf("result should not contain %q, got: %q", s, result)
				}
			}
		})
	}
}

func TestSanitizeRunError_SafePaths(t *testing.T) {
	// /data and /tmp paths should NOT be masked
	tests := []string{
		"loaded file from /data/input.csv",
		"wrote output to /tmp/result.json",
	}
	for _, input := range tests {
		result := sanitizeRunError(input)
		if result != input {
			t.Errorf("safe path was modified: input=%q result=%q", input, result)
		}
	}
}

func TestSanitizeRunError_SafeErrors(t *testing.T) {
	safeErrors := []string{
		"Node 'Transform' failed: column 'name' not found",
		"quality check failed: 2/5 checks failed on 1000 rows",
		"join key 'user_id' not found in right dataset",
		"timeout after 30s",
		"cancelled by user",
	}
	for _, input := range safeErrors {
		result := sanitizeRunError(input)
		if result != input {
			t.Errorf("safe error was modified: input=%q result=%q", input, result)
		}
	}
}

func TestSanitizeRunError_Empty(t *testing.T) {
	if sanitizeRunError("") != "" {
		t.Error("empty string should return empty string")
	}
}

func TestSanitizeRunError_MultipleConnectionStrings(t *testing.T) {
	input := "failed: mysql://root:pass@host1:3306/db and redis://user:pwd@host2:6379/0"
	result := sanitizeRunError(input)
	if strings.Contains(result, "pass") {
		t.Errorf("mysql password not masked in: %q", result)
	}
	if strings.Contains(result, "pwd") {
		t.Errorf("redis password not masked in: %q", result)
	}
	if strings.Contains(result, "host1") {
		t.Errorf("mysql host not masked in: %q", result)
	}
	if strings.Contains(result, "host2") {
		t.Errorf("redis host not masked in: %q", result)
	}
}

func TestSanitizeRunError_MixedSensitiveContent(t *testing.T) {
	input := "connection to postgres://admin:s3cr3t@prod-db:5432/app failed, check /etc/hosts"
	result := sanitizeRunError(input)
	if strings.Contains(result, "s3cr3t") {
		t.Error("password not masked")
	}
	if strings.Contains(result, "prod-db") {
		t.Error("host not masked")
	}
	if strings.Contains(result, "/etc/hosts") {
		t.Error("system path not masked")
	}
}

// ─── Connection secrets masking ─────────────────────────────────────────────
//
// The ConnectionHandler.List method masks secrets inline:
//   - Password is set to "" for every connection in the list
//   - Extra (encrypted JSON) is set to "" to prevent leaking encrypted blobs
//
// The ConnectionHandler.Get method:
//   - Password is set to "********" (masked placeholder)
//   - Extra is decrypted and returned (for editing forms)
//
// These behaviors are best tested via integration/HTTP tests with a real store
// and crypto config. Unit tests would require mocking the store interface.
// The masking logic is straightforward assignment (not extractable to a helper).

// ─── User creation protection ───────────────────────────────────────────────
//
// CreateUserHandler enforces two security invariants:
//
// 1. First user forced to admin: When UserCount() == 0, the role is always
//    overridden to RoleAdmin, preventing an attacker from creating a viewer-only
//    first account and locking out the system.
//
// 2. Subsequent users require admin auth: When UserCount() > 0, the handler
//    checks for JWT claims in context and requires the caller's role to be
//    "admin" or "superadmin". Non-admin callers get HTTP 403.
//
// These are HTTP handler behaviors that require a full UserStore (SQLite DB)
// and JWT token generation to test properly. They would be covered in
// integration tests or with a test helper that sets up an in-memory SQLite DB.

// ─── Password validation ────────────────────────────────────────────────────

func TestValidatePassword_TooShort(t *testing.T) {
	err := validatePassword("Sh0rt")
	if err == nil {
		t.Error("password under 10 chars should be rejected")
	}
}

func TestValidatePassword_NoUppercase(t *testing.T) {
	err := validatePassword("alllowercase123")
	if err == nil {
		t.Error("password without uppercase should be rejected")
	}
}

func TestValidatePassword_NoLowercase(t *testing.T) {
	err := validatePassword("ALLUPPERCASE123")
	if err == nil {
		t.Error("password without lowercase should be rejected")
	}
}

func TestValidatePassword_NoDigit(t *testing.T) {
	err := validatePassword("NoDigitsHere!")
	if err == nil {
		t.Error("password without digit should be rejected")
	}
}

func TestValidatePassword_Valid(t *testing.T) {
	err := validatePassword("StrongPass1!")
	if err != nil {
		t.Errorf("valid password should be accepted, got: %v", err)
	}
}

func TestValidatePassword_ExactlyTenChars(t *testing.T) {
	err := validatePassword("Abcdefgh1x")
	if err != nil {
		t.Errorf("10-char valid password should be accepted, got: %v", err)
	}
}
