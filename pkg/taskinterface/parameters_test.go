package taskinterface

import (
	"testing"
)

func mustParamDecls(t *testing.T, doc string) map[string]interface{} {
	return mustJSON(t, doc).(map[string]interface{})
}

func TestResolveParameters_AppliesDefaultForOmittedOptional(t *testing.T) {
	decls := mustParamDecls(t, `{
		"threshold": {"type": {"kind": "float64"}, "required": false, "default": 0.5}
	}`)
	resolved, err := ResolveParameters(decls, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved["threshold"] != 0.5 {
		t.Errorf("expected default 0.5, got %v", resolved["threshold"])
	}
}

func TestResolveParameters_SubmittedValueOverridesDefault(t *testing.T) {
	decls := mustParamDecls(t, `{
		"threshold": {"type": {"kind": "float64"}, "required": false, "default": 0.5}
	}`)
	resolved, err := ResolveParameters(decls, map[string]interface{}{"threshold": 0.9})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved["threshold"] != 0.9 {
		t.Errorf("expected submitted 0.9 to override default, got %v", resolved["threshold"])
	}
}

func TestResolveParameters_MissingRequired_Errors(t *testing.T) {
	decls := mustParamDecls(t, `{
		"region": {"type": {"kind": "string"}, "required": true}
	}`)
	if _, err := ResolveParameters(decls, map[string]interface{}{}); err == nil {
		t.Fatal("expected an error for a missing required parameter")
	}
}

func TestResolveParameters_UnknownSubmittedParameter_Errors(t *testing.T) {
	decls := mustParamDecls(t, `{
		"region": {"type": {"kind": "string"}, "required": true}
	}`)
	_, err := ResolveParameters(decls, map[string]interface{}{"region": "us-east", "surprise": true})
	if err == nil {
		t.Fatal("expected an error for an unknown submitted parameter")
	}
}

func TestResolveParameters_SubmittedValueFailsTypeValidation_Errors(t *testing.T) {
	decls := mustParamDecls(t, `{
		"threshold": {"type": {"kind": "float64"}, "required": true}
	}`)
	_, err := ResolveParameters(decls, map[string]interface{}{"threshold": "not a number"})
	if err == nil {
		t.Fatal("expected an error for a value that fails type validation")
	}
}

func TestResolveParameters_RequiredAndDefault_RejectedAtDeclarationTime(t *testing.T) {
	decls := mustParamDecls(t, `{
		"threshold": {"type": {"kind": "float64"}, "required": true, "default": 0.5}
	}`)
	_, err := ResolveParameters(decls, map[string]interface{}{"threshold": 0.5})
	if err == nil {
		t.Fatal("expected required:true + default to be rejected (ADR-032 section 3 rule 2)")
	}
}

func TestResolveParameters_InvalidParameterName_Rejected(t *testing.T) {
	decls := mustParamDecls(t, `{
		"1-not-an-identifier": {"type": {"kind": "string"}, "required": false}
	}`)
	if _, err := ResolveParameters(decls, map[string]interface{}{}); err == nil {
		t.Fatal("expected an invalid parameter name to be rejected")
	}
}

func TestResolveParameters_OptionalWithNoDefaultAndNotSubmitted_Omitted(t *testing.T) {
	decls := mustParamDecls(t, `{
		"nickname": {"type": {"kind": "string"}, "required": false}
	}`)
	resolved, err := ResolveParameters(decls, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := resolved["nickname"]; present {
		t.Errorf("expected an optional parameter with no default and no submission to be entirely absent, got %v", resolved["nickname"])
	}
}

func TestResolveParameters_EmptyDeclarations_EmptySubmission_Fine(t *testing.T) {
	resolved, err := ResolveParameters(map[string]interface{}{}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("expected an empty resolved map, got %v", resolved)
	}
}
