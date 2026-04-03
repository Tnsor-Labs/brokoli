package engine

import (
	"math"
	"time"
)

// RetryConfig defines retry behavior for a node.
type RetryConfig struct {
	MaxRetries  int           `json:"max_retries"`  // 0 = no retry
	BackoffType string        `json:"backoff_type"` // "fixed", "exponential", "linear"
	BackoffBase time.Duration `json:"backoff_base"` // base delay (e.g., 2s)
	BackoffMax  time.Duration `json:"backoff_max"`  // max delay cap (e.g., 60s)
}

// DefaultRetryConfig returns sensible defaults based on node type.
func DefaultRetryConfig(nodeType string) RetryConfig {
	switch nodeType {
	case "source_db", "source_api", "sink_db", "sink_api":
		// Network-dependent nodes get retries
		return RetryConfig{MaxRetries: 3, BackoffType: "exponential", BackoffBase: 2 * time.Second, BackoffMax: 30 * time.Second}
	case "source_file", "sink_file":
		// File nodes get fewer retries
		return RetryConfig{MaxRetries: 2, BackoffType: "fixed", BackoffBase: 1 * time.Second, BackoffMax: 5 * time.Second}
	default:
		// Transform/code/quality nodes don't retry by default
		return RetryConfig{MaxRetries: 0}
	}
}

// ParseRetryConfig extracts retry config from a node's config map.
// Falls back to defaults for the node type if not specified.
func ParseRetryConfig(nodeType string, config map[string]interface{}) RetryConfig {
	rc := DefaultRetryConfig(nodeType)

	if v, ok := config["max_retries"]; ok {
		// parse from float64 (JSON numbers)
		if f, ok := v.(float64); ok {
			rc.MaxRetries = int(f)
		}
	}
	if v, ok := config["retry_backoff"]; ok {
		if s, ok := v.(string); ok {
			rc.BackoffType = s
		}
	}
	return rc
}

// ComputeBackoff returns the delay for the given attempt number.
func ComputeBackoff(cfg RetryConfig, attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	var delay time.Duration
	switch cfg.BackoffType {
	case "exponential":
		delay = cfg.BackoffBase * time.Duration(math.Pow(2, float64(attempt-1)))
	case "linear":
		delay = cfg.BackoffBase * time.Duration(attempt)
	default: // "fixed"
		delay = cfg.BackoffBase
	}
	if cfg.BackoffMax > 0 && delay > cfg.BackoffMax {
		delay = cfg.BackoffMax
	}
	return delay
}

// ShouldRetry determines if a node execution should be retried.
func ShouldRetry(cfg RetryConfig, attempt int, err error) bool {
	if cfg.MaxRetries <= 0 || attempt >= cfg.MaxRetries {
		return false
	}
	if err == nil {
		return false
	}
	// Could add error classification here (transient vs permanent)
	return true
}
