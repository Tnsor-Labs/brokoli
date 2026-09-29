package engine

import (
	"strconv"
	"testing"
)

const gcsTestCredentials = `{"type":"service_account","project_id":"test-project"}`

func TestGCSFileConfigFromExtra(t *testing.T) {
	cfg, err := GCSFileConfigFromExtra(`{"bucket":"brokoli-test","credentials":` + strconv.Quote(gcsTestCredentials) + `,"max_download_bytes":42}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bucket != "brokoli-test" || cfg.MaxDownloadBytes != 42 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestGCSFileConfigRejectsInvalidCredentialsAndBucket(t *testing.T) {
	invalidBucket := `{"bucket":"Bad Bucket","credentials":` + strconv.Quote(gcsTestCredentials) + `}`
	for name, extra := range map[string]string{
		"wrong credential type": `{"bucket":"brokoli-test","credentials":"{\"type\":\"authorized_user\"}"}`,
		"invalid bucket":        invalidBucket,
		// Whoever controls the endpoint receives the access token.
		"endpoint from connection data": `{"bucket":"brokoli-test","endpoint":"http://example.com"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := GCSFileConfigFromExtra(extra); err == nil {
				t.Fatal("expected invalid GCS config")
			}
		})
	}
}
