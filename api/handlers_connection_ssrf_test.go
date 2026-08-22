package api

import (
	"context"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// TestTestGeneric_BlocksLoopbackWebhookURL pins the connection-test SSRF
// fix: testGeneric's webhook_url path previously made an outbound
// request with zero target validation at all.
func TestTestGeneric_BlocksLoopbackWebhookURL(t *testing.T) {
	result := testGeneric(context.Background(), &models.Connection{}, map[string]interface{}{
		"webhook_url": "http://127.0.0.1:1/should-be-blocked",
	})
	success, _ := result["success"].(bool)
	if success {
		t.Fatal("expected testGeneric to reject a loopback webhook_url, got success=true")
	}
	errMsg, _ := result["error"].(string)
	if !strings.Contains(errMsg, "blocked") {
		t.Fatalf("error = %q, want it to mention the target was blocked", errMsg)
	}
}

// TestTestHTTPAuth_BlocksLoopbackConnection pins the same fix for the
// HTTP-auth connection-test path (testHTTPAuth), which builds its
// target from the connection itself (c.BuildURI()) rather than a
// separate webhook_url field.
func TestTestHTTPAuth_BlocksLoopbackConnection(t *testing.T) {
	c := &models.Connection{Type: "http", Host: "127.0.0.1", Port: 1}
	result := testHTTPAuth(context.Background(), c, nil)
	success, _ := result["success"].(bool)
	if success {
		t.Fatal("expected testHTTPAuth to reject a loopback connection host, got success=true")
	}
	errMsg, _ := result["error"].(string)
	if !strings.Contains(errMsg, "blocked") {
		t.Fatalf("error = %q, want it to mention the target was blocked", errMsg)
	}
}

// TestTestS3_BlocksLoopbackEndpointInjection proves this S3 connection
// testing cannot be redirected to a loopback address through bucket input.
func TestTestS3_BlocksLoopbackEndpointInjection(t *testing.T) {
	result := testS3(context.Background(), map[string]interface{}{
		"bucket": "ignored@127.0.0.1:1/",
		"region": "us-east-1",
		"access_key": "test-key",
		"secret_key": "test-secret",
	})

	success, _ := result["success"].(bool)
	if success {
		t.Fatal("expected testS3 to reject a loopback target, got success=true")
	}

	errMsg, _ := result["error"].(string)
	if !strings.Contains(errMsg, "blocked") {
		t.Fatalf("error = %q, want it to mention the target was blocked", errMsg)
	}
}