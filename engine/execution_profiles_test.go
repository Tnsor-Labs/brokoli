package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func expandedProfile(strict bool) map[string]interface{} {
	return map[string]interface{}{
		"profile": map[string]interface{}{"name": "public_api_safe", "version": float64(1), "strict": strict},
		"timeout": 30, "max_retries": 3, "retry_backoff": "exponential", "retry_delay": 1,
		"max_concurrency": 2, "requests_per_second": 2, "retry_scope": "page",
		"checkpoint_every": 10, "page_max_retries": 3, "page_retry_backoff": "exponential",
	}
}

func profilePipeline(execution map[string]interface{}, pagination map[string]interface{}) *models.Pipeline {
	config := map[string]interface{}{"url": "https://example.test", "execution": execution,
		"timeout": 30, "max_retries": 3, "retry_backoff": "exponential", "retry_delay": 1}
	if pagination != nil {
		config["pagination"] = pagination
	}
	return &models.Pipeline{
		Name: "profiles", Nodes: []models.Node{{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source", Config: config}},
	}
}

func TestExecutionProfileRequiresExplicitExpansion(t *testing.T) {
	profile := expandedProfile(false)
	delete(profile, "max_concurrency")
	ve := ValidatePipeline(profilePipeline(profile, map[string]interface{}{"strategy": "offset"}))
	if !ve.HasErrors() {
		t.Fatal("profile without explicit timeout was accepted")
	}
}

func TestExecutionProfileAcceptsVersionedExpansion(t *testing.T) {
	ve := ValidatePipeline(profilePipeline(expandedProfile(false), map[string]interface{}{"strategy": "offset"}))
	if ve.HasErrors() {
		t.Fatalf("expanded profile rejected: %v", ve)
	}
}

func TestExecutionProfileStrictRejectsSequentialConcurrency(t *testing.T) {
	profile := expandedProfile(true)
	profile["max_concurrency"] = 4
	ve := ValidatePipeline(profilePipeline(profile, map[string]interface{}{"strategy": "cursor"}))
	if !ve.HasErrors() {
		t.Fatal("strict profile accepted ineffective sequential concurrency")
	}
}
