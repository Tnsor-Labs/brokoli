package secretstore

import (
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"secret://vault-prod/warehouse/loader#password": {Store: "vault-prod", Path: "warehouse/loader", Field: "password"},
		"secret://aws/prod/warehouse?version=3#pw":      {Store: "aws", Path: "prod/warehouse", Version: "3", Field: "pw"},
		"secret://ssm//prod/warehouse/password":         {Store: "ssm", Path: "/prod/warehouse/password"},
		"secret://kv/arn:aws:secretsmanager:x:1:secret": {Store: "kv", Path: "arn:aws:secretsmanager:x:1:secret"},
	} {
		got, err := ParseRef(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %+v, want %+v", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"vault://x#y":                   "not a secret:// reference",
		"secret://vault-prod":           "expected secret://<store>/<path>",
		"secret://vault-prod/":          "expected secret://<store>/<path>",
		"secret://Vault/x":              "not a valid store name",
		"secret://-bad/x":               "not a valid store name",
		"secret://v/a/../b":             "may not contain '..'",
		"secret://v/x#":                 "empty #field",
		"secret://v/x?ttl=3":            `unknown option "ttl"`,
		"secret://v/x?version=1&other=": `unknown option "other"`,
	} {
		if _, err := ParseRef(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}

func TestShapeRules(t *testing.T) {
	withField, _ := ParseRef("secret://s/p#f")
	bare, _ := ParseRef("secret://s/p")
	if CheckShape(bare, ShapeMap) == nil || CheckShape(withField, ShapeString) == nil {
		t.Fatal("a map needs a field and a string refuses one")
	}
	if CheckShape(withField, ShapeMap) != nil || CheckShape(bare, ShapeString) != nil ||
		CheckShape(bare, ShapeEither) != nil || CheckShape(withField, ShapeEither) != nil {
		t.Fatal("valid shapes were refused")
	}
	m := Secret{Fields: map[string][]byte{"f": []byte("v"), "g": []byte("w")}}
	if v, err := Pick(withField, m); err != nil || string(v) != "v" {
		t.Fatalf("pick field: %q %v", v, err)
	}
	if _, err := Pick(bare, m); err == nil || !strings.Contains(err.Error(), "f, g") {
		t.Fatalf("a map without a field must name its fields: %v", err)
	}
	if _, err := Pick(withField, Secret{Value: []byte("x")}); err == nil {
		t.Fatal("a field on a single value must be refused, never ignored")
	}
	missing, _ := ParseRef("secret://s/p#nope")
	if _, err := Pick(missing, m); err == nil || !strings.Contains(err.Error(), `no field "nope"`) {
		t.Fatalf("missing field: %v", err)
	}
}

func TestStoreNames(t *testing.T) {
	for _, ok := range []string{"a", "vault-prod", "a1", strings.Repeat("a", 63)} {
		if !ValidStoreName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a.b", strings.Repeat("a", 64)} {
		if ValidStoreName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
