package engine

import (
	"strings"
	"testing"
)

func TestGenerateWebhookToken(t *testing.T) {
	token, err := GenerateWebhookToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check prefix
	if !strings.HasPrefix(token, "whk_") {
		t.Fatalf("expected token to start with 'whk_', got %q", token)
	}

	// 24 bytes = 48 hex chars + 4 char prefix = 52 total
	if len(token) != 52 {
		t.Fatalf("expected token length 52, got %d (%q)", len(token), token)
	}

	// Check uniqueness (two tokens should differ)
	token2, err := GenerateWebhookToken()
	if err != nil {
		t.Fatalf("unexpected error on second token: %v", err)
	}
	if token == token2 {
		t.Fatal("expected two generated tokens to be different")
	}
}

func TestValidateWebhookToken(t *testing.T) {
	token, _ := GenerateWebhookToken()

	// Valid match
	if !ValidateWebhookToken(token, token) {
		t.Fatal("expected matching tokens to validate")
	}

	// Invalid match
	if ValidateWebhookToken("whk_wrong", token) {
		t.Fatal("expected non-matching tokens to fail validation")
	}

	// Empty strings
	if ValidateWebhookToken("", token) {
		t.Fatal("expected empty provided token to fail")
	}
	if !ValidateWebhookToken("", "") {
		t.Fatal("expected two empty strings to match")
	}
}
