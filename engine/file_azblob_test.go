package engine

import (
	"strings"
	"testing"
)

// The configuration an azure_blob connection carries. These run without a
// service; the integration tests beside them need Azurite.

func TestAzureBlobFileConfigFromExtra(t *testing.T) {
	good := `{"account":"acme","container":"exports","key":"a2V5"}`
	cfg, err := AzureBlobFileConfigFromExtra(good)
	if err != nil {
		t.Fatalf("a minimal valid config was refused: %v", err)
	}
	if cfg.Endpoint != "https://acme.blob.core.windows.net/" {
		t.Errorf("default endpoint = %q, want the public cloud's", cfg.Endpoint)
	}
	if cfg.MaxDownloadBytes != defaultAzureBlobDownloadLimit {
		t.Errorf("default download limit = %d", cfg.MaxDownloadBytes)
	}

	sas, err := AzureBlobFileConfigFromExtra(`{"account":"acme","container":"exports","sas_token":"?sv=2023&sig=x"}`)
	if err != nil {
		t.Fatalf("a SAS config was refused: %v", err)
	}
	if strings.HasPrefix(sas.SASToken, "?") {
		t.Errorf("a leading ? on the SAS token must be dropped, got %q", sas.SASToken)
	}

	cases := []struct {
		name, extra, want string
	}{
		{"empty", ``, "extra config is required"},
		{"not JSON", `nope`, "must be a JSON object"},
		{"no account", `{"container":"exports","key":"k"}`, "requires account"},
		{"account with capitals", `{"account":"Acme","container":"exports","key":"k"}`, "account must be"},
		{"no container", `{"account":"acme","key":"k"}`, "requires container"},
		{"container with capitals", `{"account":"acme","container":"Exports","key":"k"}`, "container name is invalid"},
		{"container with double hyphen", `{"account":"acme","container":"ex--ports","key":"k"}`, "container name is invalid"},
		{"container ending in hyphen", `{"account":"acme","container":"exports-","key":"k"}`, "container name is invalid"},
		{"no credential", `{"account":"acme","container":"exports"}`, "requires key"},
		{"both credentials", `{"account":"acme","container":"exports","key":"k","sas_token":"sv=1"}`, "alternatives"},
		{"service principal refused by name", `{"account":"acme","container":"exports","tenant_id":"t","client_id":"c","client_secret":"s"}`, "service principal"},
		{"endpoint not a URL", `{"account":"acme","container":"exports","key":"k","endpoint":"blob.example"}`, "http or https URL"},
		{"SAS smuggled into the endpoint", `{"account":"acme","container":"exports","key":"k","endpoint":"https://acme.blob.core.windows.net/?sig=x"}`, "must not carry a query string"},
		{"bad download limit", `{"account":"acme","container":"exports","key":"k","max_download_bytes":-1}`, "positive whole number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AzureBlobFileConfigFromExtra(tc.extra)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A connection saved against the catalogue's documented hint -- account,
// container, key -- must work without editing, because that hint existed
// before any node could use the type.
func TestAzureBlobAcceptsTheCatalogueHintFields(t *testing.T) {
	if _, err := AzureBlobFileConfigFromExtra(`{"container": "my-container", "account": "storageaccount", "key": "a2V5"}`); err != nil {
		t.Fatalf("the catalogue's own example config is refused: %v", err)
	}
}

// An unsupported connection type is refused by name, and the message
// lists every transport, so it cannot fall behind openFileTransport.
func TestFileTransportRefusalNamesEveryTransport(t *testing.T) {
	for _, name := range []string{"sftp", "s3", "azure_blob"} {
		if !strings.Contains(fileTransportTypes, name) {
			t.Errorf("fileTransportTypes %q does not name %s", fileTransportTypes, name)
		}
	}
}
