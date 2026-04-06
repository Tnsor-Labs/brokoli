package engine

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultRetryConfig_NetworkNodes(t *testing.T) {
	for _, nt := range []string{"source_db", "source_api", "sink_db", "sink_api"} {
		rc := DefaultRetryConfig(nt)
		if rc.MaxRetries != 3 {
			t.Errorf("%s: expected MaxRetries=3, got %d", nt, rc.MaxRetries)
		}
		if rc.BackoffType != "exponential" {
			t.Errorf("%s: expected BackoffType=exponential, got %s", nt, rc.BackoffType)
		}
		if rc.BackoffBase != 2*time.Second {
			t.Errorf("%s: expected BackoffBase=2s, got %v", nt, rc.BackoffBase)
		}
		if rc.BackoffMax != 30*time.Second {
			t.Errorf("%s: expected BackoffMax=30s, got %v", nt, rc.BackoffMax)
		}
	}
}

func TestDefaultRetryConfig_FileNodes(t *testing.T) {
	for _, nt := range []string{"source_file", "sink_file"} {
		rc := DefaultRetryConfig(nt)
		if rc.MaxRetries != 2 {
			t.Errorf("%s: expected MaxRetries=2, got %d", nt, rc.MaxRetries)
		}
		if rc.BackoffType != "fixed" {
			t.Errorf("%s: expected BackoffType=fixed, got %s", nt, rc.BackoffType)
		}
	}
}

func TestDefaultRetryConfig_TransformNodes(t *testing.T) {
	for _, nt := range []string{"transform", "code", "quality_check", "join"} {
		rc := DefaultRetryConfig(nt)
		if rc.MaxRetries != 0 {
			t.Errorf("%s: expected MaxRetries=0, got %d", nt, rc.MaxRetries)
		}
	}
}

func TestParseRetryConfig_Defaults(t *testing.T) {
	cfg := map[string]interface{}{}
	rc := ParseRetryConfig("source_db", cfg)
	if rc.MaxRetries != 3 {
		t.Errorf("expected default MaxRetries=3, got %d", rc.MaxRetries)
	}
	if rc.BackoffType != "exponential" {
		t.Errorf("expected default BackoffType=exponential, got %s", rc.BackoffType)
	}
}

func TestParseRetryConfig_Override(t *testing.T) {
	cfg := map[string]interface{}{
		"max_retries":   float64(5),
		"retry_backoff": "linear",
	}
	rc := ParseRetryConfig("source_db", cfg)
	if rc.MaxRetries != 5 {
		t.Errorf("expected MaxRetries=5, got %d", rc.MaxRetries)
	}
	if rc.BackoffType != "linear" {
		t.Errorf("expected BackoffType=linear, got %s", rc.BackoffType)
	}
}

func TestComputeBackoff_Fixed(t *testing.T) {
	cfg := RetryConfig{BackoffType: "fixed", BackoffBase: 3 * time.Second, BackoffMax: 10 * time.Second}
	for _, attempt := range []int{1, 2, 3, 5} {
		d := ComputeBackoff(cfg, attempt)
		if d != 3*time.Second {
			t.Errorf("attempt %d: expected 3s, got %v", attempt, d)
		}
	}
}

func TestComputeBackoff_Exponential(t *testing.T) {
	cfg := RetryConfig{BackoffType: "exponential", BackoffBase: 1 * time.Second, BackoffMax: 60 * time.Second}
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second}, // 1 * 2^0
		{2, 2 * time.Second}, // 1 * 2^1
		{3, 4 * time.Second}, // 1 * 2^2
		{4, 8 * time.Second}, // 1 * 2^3
	}
	for _, tt := range tests {
		d := ComputeBackoff(cfg, tt.attempt)
		if d != tt.expected {
			t.Errorf("attempt %d: expected %v, got %v", tt.attempt, tt.expected, d)
		}
	}
}

func TestComputeBackoff_Linear(t *testing.T) {
	cfg := RetryConfig{BackoffType: "linear", BackoffBase: 2 * time.Second, BackoffMax: 20 * time.Second}
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 6 * time.Second},
	}
	for _, tt := range tests {
		d := ComputeBackoff(cfg, tt.attempt)
		if d != tt.expected {
			t.Errorf("attempt %d: expected %v, got %v", tt.attempt, tt.expected, d)
		}
	}
}

func TestComputeBackoff_MaxCap(t *testing.T) {
	cfg := RetryConfig{BackoffType: "exponential", BackoffBase: 10 * time.Second, BackoffMax: 30 * time.Second}
	d := ComputeBackoff(cfg, 5) // 10 * 2^4 = 160s, capped at 30s
	if d != 30*time.Second {
		t.Errorf("expected 30s cap, got %v", d)
	}
}

func TestComputeBackoff_ZeroAttempt(t *testing.T) {
	cfg := RetryConfig{BackoffType: "fixed", BackoffBase: 5 * time.Second}
	d := ComputeBackoff(cfg, 0)
	if d != 0 {
		t.Errorf("expected 0 for attempt 0, got %v", d)
	}
}

func TestShouldRetry_NoConfig(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 0}
	if ShouldRetry(cfg, 0, errors.New("fail")) {
		t.Error("should not retry when MaxRetries=0")
	}
}

func TestShouldRetry_WithinLimit(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3}
	if !ShouldRetry(cfg, 1, errors.New("fail")) {
		t.Error("should retry when attempt < MaxRetries")
	}
	if !ShouldRetry(cfg, 2, errors.New("fail")) {
		t.Error("should retry when attempt < MaxRetries")
	}
}

func TestShouldRetry_ExceedsLimit(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3}
	if ShouldRetry(cfg, 3, errors.New("fail")) {
		t.Error("should not retry when attempt >= MaxRetries")
	}
	if ShouldRetry(cfg, 5, errors.New("fail")) {
		t.Error("should not retry when attempt > MaxRetries")
	}
}

func TestShouldRetry_NoError(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3}
	if ShouldRetry(cfg, 1, nil) {
		t.Error("should not retry when there is no error")
	}
}
