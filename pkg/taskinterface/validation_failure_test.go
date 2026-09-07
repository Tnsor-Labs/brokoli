package taskinterface

import (
	"strings"
	"testing"
)

func TestNewValidationFailure_NilCauseIsNil(t *testing.T) {
	if f := NewValidationFailure(DirectionOutput, "result", Type{Kind: KindInt64}, "x", nil, "$", false); f != nil {
		t.Fatalf("got %+v, want nil for a nil cause", f)
	}
}

func TestNewValidationFailure_PopulatesStructuredFields(t *testing.T) {
	ty := Type{Kind: KindInt64}
	cause := ValidateValue("not-an-int", ty, "$")
	if cause == nil {
		t.Fatal("setup: expected ValidateValue to fail")
	}
	f := NewValidationFailure(DirectionOutput, "result", ty, "not-an-int", cause, "$", false)
	if f == nil {
		t.Fatal("got nil, want a ValidationFailure")
	}
	if f.Direction != DirectionOutput || f.Name != "result" {
		t.Errorf("direction/name = %v/%v, want output/result", f.Direction, f.Name)
	}
	if f.ContractPath != "$" {
		t.Errorf("ContractPath = %q, want %q", f.ContractPath, "$")
	}
	if f.Expected != "int64" {
		t.Errorf("Expected = %q, want %q", f.Expected, "int64")
	}
	if f.Observed != "string(len=10)" {
		t.Errorf("Observed = %q, want %q", f.Observed, "string(len=10)")
	}
	if f.CheckedRows != 1 || f.Mode != ModeFull {
		t.Errorf("CheckedRows/Mode = %d/%v, want 1/full", f.CheckedRows, f.Mode)
	}
	if f.Redacted {
		t.Error("Redacted = true, want false (not a sensitive declaration)")
	}
	if f.Unwrap() != cause {
		t.Error("Unwrap() did not return the original cause")
	}
}

// A sensitive declaration must never leak the observed value's shape or
// the underlying validator's message -- enum validation in particular
// quotes the actual invalid string in its plain error text (see
// validateEnumValue), so Error() must never surface it when the
// declaration is sensitive.
func TestNewValidationFailure_SensitiveDeclarationRedactsEverything(t *testing.T) {
	ty := Type{Kind: KindEnum, Values: []string{"a", "b"}}
	secret := "definitely-not-a-or-b-super-secret-value"
	cause := ValidateValue(secret, ty, "$")
	if cause == nil {
		t.Fatal("setup: expected ValidateValue to fail")
	}
	if strings.Contains(cause.Error(), secret) == false {
		t.Fatal("setup: expected the underlying error to actually contain the secret (that's the bug this test guards against leaking)")
	}

	f := NewValidationFailure(DirectionParameter, "token", ty, secret, cause, "$", true)
	if !f.Redacted {
		t.Fatal("Redacted = false, want true for a sensitive declaration")
	}
	if f.Observed != "" {
		t.Errorf("Observed = %q, want empty when redacted", f.Observed)
	}
	if strings.Contains(f.Error(), secret) {
		t.Fatalf("Error() leaked the secret value: %q", f.Error())
	}
}

func TestValidationFailure_ErrorIncludesCauseWhenNotSensitive(t *testing.T) {
	ty := Type{Kind: KindString, MaxLength: intPtr(3)}
	cause := ValidateValue("way too long", ty, "$")
	f := NewValidationFailure(DirectionInput, "name", ty, "way too long", cause, "$", false)
	if !strings.Contains(f.Error(), "max_length") {
		t.Errorf("Error() = %q, want it to include the underlying cause detail", f.Error())
	}
}

func TestDescribeType_CompoundKinds(t *testing.T) {
	cases := []struct {
		name string
		typ  Type
		want string
	}{
		{"record", Type{Kind: KindRecord, Fields: []Field{{Name: "a"}, {Name: "b"}}}, "record{2 field(s)}"},
		{"array of string", Type{Kind: KindArray, Items: &Type{Kind: KindString}}, "array<string>"},
		{"nullable int64", Type{Kind: KindInt64, Nullable: true}, "int64 (nullable)"},
		{"enum", Type{Kind: KindEnum, Values: []string{"a", "b", "c"}}, "enum{3 value(s)}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeType(tc.typ); got != tc.want {
				t.Errorf("describeType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDescribeValueKind_NeverIncludesContent(t *testing.T) {
	cases := []struct {
		name string
		raw  interface{}
		want string
	}{
		{"nil", nil, "null"},
		{"string", "super-secret-content", "string(len=20)"},
		{"array", []interface{}{1, 2, 3}, "array(len=3)"},
		{"object", map[string]interface{}{"a": 1, "b": 2}, "object(2 field(s))"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := describeValueKind(tc.raw)
			if got != tc.want {
				t.Errorf("describeValueKind() = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "secret") {
				t.Errorf("describeValueKind() leaked value content: %q", got)
			}
		})
	}
}

func intPtr(i int) *int { return &i }
