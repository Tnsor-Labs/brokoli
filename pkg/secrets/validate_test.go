package secrets

import (
	"strings"
	"testing"
)

func TestValidateRefAcceptsWellFormedReferences(t *testing.T) {
	// No allowlist is set and none of these variables exist: validation
	// reads neither.
	for _, ref := range []string{
		"env://WAREHOUSE_PASSWORD",
		"env://_X1",
		"vault://secret/data/prod/warehouse#password",
		"k8s://brokoli/db-creds/password",
		"k8s://db-creds/password",
	} {
		if err := ValidateRef(ref); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
}

func TestValidateRefRefusesByName(t *testing.T) {
	for ref, want := range map[string]string{
		"WAREHOUSE_PASSWORD":                "is not a reference",
		"valt://secret/data/x#k":            `scheme "valt://" is not supported`,
		"secret://Vault_Prod/warehouse#pw":  `not a valid store name`,
		"secret://vault-prod":               `expected secret://<store>/<path>`,
		"encrypted://Zm9vYmFy":              "created by the server",
		"env://":                            "variable name",
		"env://1ST":                         "variable name",
		"env://NAME WITH SPACE":             "variable name",
		"env://BROKOLI_ENCRYPTION_KEY":      "can never be read",
		"env://brokoli_jwt_secret":          "can never be read",
		"vault://secret/data/x":             "expected vault://path#key",
		"vault://#key":                      "expected vault://path#key",
		"vault://secret/data/x#":            "expected vault://path#key",
		"vault://secret/../sys/x#k":         "'..'",
		"vault://secret/data/x?version=1#k": "'?'",
		"k8s://only":                        "expected k8s://",
		"k8s://a/b/c/d":                     "expected k8s://",
		"k8s://ns/-secret/key":              "not a valid name",
		"k8s://ns/se cret/key":              "not a valid name",
		"k8s://ns//key":                     "not a valid name",
	} {
		err := ValidateRef(ref)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to contain %q", ref, err, want)
		}
	}
}
