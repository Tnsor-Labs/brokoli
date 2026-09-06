package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCapabilitiesHandler exercises GET /api/capabilities directly (the
// handler has no store/engine dependency, so it's invoked without the
// full router). SDK clients hit this endpoint to discover which
// ir_version(s) and node/connector capability tags a given Brokoli
// deployment understands before deploying a pipeline.
func TestCapabilitiesHandler(t *testing.T) {
	SetCodeRuntime("")
	t.Cleanup(func() { SetCodeRuntime("") })
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	rec := httptest.NewRecorder()

	CapabilitiesHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body["ir_version"] != "2.0" {
		t.Errorf("expected ir_version 2.0, got %v", body["ir_version"])
	}

	supported, ok := body["supported_ir_versions"].([]interface{})
	if !ok || len(supported) == 0 {
		t.Fatalf("expected non-empty supported_ir_versions, got %v", body["supported_ir_versions"])
	}
	found := map[string]bool{"2.0": false, "2.1": false}
	for _, v := range supported {
		if version, ok := v.(string); ok {
			if _, expected := found[version]; expected {
				found[version] = true
			}
		}
	}
	if !found["2.0"] || !found["2.1"] {
		t.Errorf("expected supported_ir_versions to include 2.0 and 2.1, got %v", supported)
	}

	if body["plugin_protocol_version"] == nil {
		t.Error("expected plugin_protocol_version to be present")
	}
	if body["supported_plugin_protocol_versions"] == nil {
		t.Error("expected supported_plugin_protocol_versions to be present")
	}

	pkgVersions, ok := body["supported_packaging_versions"].([]interface{})
	if !ok || len(pkgVersions) == 0 {
		t.Fatalf("expected non-empty supported_packaging_versions, got %v", body["supported_packaging_versions"])
	}
	hasV1 := false
	for _, v := range pkgVersions {
		if n, ok := v.(float64); ok && n == 1 {
			hasV1 = true
		}
	}
	if !hasV1 {
		t.Errorf("expected supported_packaging_versions to include 1, got %v", pkgVersions)
	}

	runtimeClasses, ok := body["supported_runtime_classes"].([]interface{})
	if !ok || len(runtimeClasses) == 0 {
		t.Fatalf("expected non-empty supported_runtime_classes, got %v", body["supported_runtime_classes"])
	}
	wantClasses := map[string]bool{"native": false, "python": false, "node": false, "jvm": false}
	for _, v := range runtimeClasses {
		if s, ok := v.(string); ok {
			if _, expected := wantClasses[s]; expected {
				wantClasses[s] = true
			}
		}
	}
	for cls, present := range wantClasses {
		if !present {
			t.Errorf("expected supported_runtime_classes to include %q, got %v", cls, runtimeClasses)
		}
	}

	nodeCaps, ok := body["node_capabilities"].([]interface{})
	if !ok || len(nodeCaps) == 0 {
		t.Fatalf("expected non-empty node_capabilities, got %v", body["node_capabilities"])
	}
	wantCaps := map[string]bool{"source": false, "sink": false, "compute": false, "dataset-output": false}
	for _, c := range nodeCaps {
		if s, ok := c.(string); ok {
			if _, known := wantCaps[s]; known {
				wantCaps[s] = true
			}
		}
	}
	for capName, seen := range wantCaps {
		if !seen {
			t.Errorf("expected node_capabilities to include %q, got %v", capName, nodeCaps)
		}
	}

	if body["node_type_capabilities"] == nil {
		t.Error("expected node_type_capabilities to be present")
	}
	if languages, ok := body["code_languages"].([]interface{}); !ok || len(languages) != 1 || languages[0] != "python" {
		t.Errorf("unexpected Python-only code_languages: %v", body["code_languages"])
	}
	if body["code_js_wrapper_version"] != float64(1) {
		t.Errorf("unexpected JS wrapper version: %v", body["code_js_wrapper_version"])
	}

	// ADR-029: the code-node execution contract is advertised like the
	// plugin protocol's.
	if v, ok := body["code_protocol_version"].(float64); !ok || v < 1 {
		t.Errorf("code_protocol_version missing or wrong: %v", body["code_protocol_version"])
	}
	if v, ok := body["code_wrapper_version"].(float64); !ok || v < 2 {
		t.Errorf("code_wrapper_version missing or wrong: %v", body["code_wrapper_version"])
	}

	features, ok := body["supported_execution_features"].([]interface{})
	if !ok || len(features) == 0 {
		t.Fatalf("expected supported_execution_features, got %v", body["supported_execution_features"])
	}
	// data_intervals: the SDK's catch_up gate matches the exact string
	// (#397 phase 4); dropping or renaming it silently re-bricks
	// Pipeline(catch_up=...) against this server.
	// code-streaming-emit: the SDK gate for emit()/begin_emit() scripts
	// matches the exact string (ADR-029); dropping or renaming it lets
	// emit() scripts deploy ungated to servers again.
	// task-interface-v1: the SDK gate for a node's "interface"/pipeline's
	// "parameters" fields (ADR-032 rollout step 3, #439); dropping or
	// renaming it lets SDK-inferred interfaces deploy ungated.
	want := map[string]bool{"conditional-routing": false, "dynamic-expansion": false, "union": false, "pagination-checkpoints": false, "data_intervals": false, "deferrable-waits": false, "code-streaming-emit": false, "task-interface-v1": false}
	for _, f := range features {
		if s, ok := f.(string); ok {
			if _, known := want[s]; known {
				want[s] = true
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("supported_execution_features missing %q", name)
		}
	}
}

func TestCapabilitiesAdvertiseTypeScriptOnlyWhenNodeResolves(t *testing.T) {
	SetCodeRuntime("/usr/bin/node")
	t.Cleanup(func() { SetCodeRuntime("") })
	rec := httptest.NewRecorder()
	CapabilitiesHandler(rec, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code_js_wrapper_version"] != float64(1) {
		t.Fatalf("wrong JS wrapper version: %v", body["code_js_wrapper_version"])
	}
	features := body["supported_execution_features"].([]interface{})
	seen := false
	for _, feature := range features {
		seen = seen || feature == "code-typescript"
	}
	if !seen {
		t.Fatalf("code-typescript not advertised: %v", features)
	}
}

func TestCapabilitiesRemainPublicThroughAuthMiddleware(t *testing.T) {
	auth := NewAuthConfig()
	auth.AddKey("brk_test", "test key")
	users := newTestUserStore(t)

	previousResolver := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = func(string) []string { return []string{"workspace-1"} }
	t.Cleanup(func() { UserWorkspaceResolverFunc = previousResolver })

	handler := APIKeyAuth(auth)(JWTAuth(users)(WorkspaceMiddleware(
		http.HandlerFunc(CapabilitiesHandler),
	)))

	capabilitiesReq := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	capabilitiesRec := httptest.NewRecorder()
	handler.ServeHTTP(capabilitiesRec, capabilitiesReq)
	if capabilitiesRec.Code != http.StatusOK {
		t.Fatalf("anonymous capabilities status = %d, want 200: %s", capabilitiesRec.Code, capabilitiesRec.Body.String())
	}

	protectedReq := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	protectedRec := httptest.NewRecorder()
	handler.ServeHTTP(protectedRec, protectedReq)
	if protectedRec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous protected route status = %d, want 401", protectedRec.Code)
	}
}

func TestEachAuthLayerExemptsOnlyCapabilities(t *testing.T) {
	pass := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("api key", func(t *testing.T) {
		auth := NewAuthConfig()
		auth.AddKey("brk_test", "test key")
		assertCapabilitiesBypass(t, APIKeyAuth(auth)(pass), http.StatusUnauthorized)
	})

	t.Run("JWT open mode", func(t *testing.T) {
		assertCapabilitiesBypass(t, JWTAuth(newTestUserStore(t))(pass), http.StatusServiceUnavailable)
	})

	t.Run("JWT configured", func(t *testing.T) {
		users := newTestUserStore(t)
		if _, err := users.CreateUser("admin", "ValidPass123", RoleAdmin); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		assertCapabilitiesBypass(t, JWTAuth(users)(pass), http.StatusUnauthorized)
	})

	t.Run("workspace ownership", func(t *testing.T) {
		previousResolver := UserWorkspaceResolverFunc
		UserWorkspaceResolverFunc = func(string) []string { return []string{"workspace-1"} }
		t.Cleanup(func() { UserWorkspaceResolverFunc = previousResolver })
		assertCapabilitiesBypass(t, WorkspaceMiddleware(pass), http.StatusUnauthorized)
	})

	t.Run("extension auth", func(t *testing.T) {
		redirect := func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
			})
		}
		handler := withPublicAuthBypass(redirect)(pass)
		assertCapabilitiesBypass(t, handler, http.StatusFound)

		healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
		healthRec := httptest.NewRecorder()
		handler.ServeHTTP(healthRec, healthReq)
		if healthRec.Code != http.StatusNoContent {
			t.Fatalf("health status = %d, want 204", healthRec.Code)
		}

		uploadReq := httptest.NewRequest(http.MethodGet, "/uploads/report.csv", nil)
		uploadRec := httptest.NewRecorder()
		handler.ServeHTTP(uploadRec, uploadReq)
		if uploadRec.Code != http.StatusFound {
			t.Fatalf("upload status = %d, want 302", uploadRec.Code)
		}
	})
}

func assertCapabilitiesBypass(t *testing.T, handler http.Handler, protectedStatus int) {
	t.Helper()

	capabilitiesReq := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	capabilitiesRec := httptest.NewRecorder()
	handler.ServeHTTP(capabilitiesRec, capabilitiesReq)
	if capabilitiesRec.Code != http.StatusNoContent {
		t.Fatalf("capabilities status = %d, want 204", capabilitiesRec.Code)
	}

	protectedReq := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	protectedRec := httptest.NewRecorder()
	handler.ServeHTTP(protectedRec, protectedReq)
	if protectedRec.Code != protectedStatus {
		t.Fatalf("protected status = %d, want %d", protectedRec.Code, protectedStatus)
	}
}
