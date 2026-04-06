package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestConnectionResolver_NoConnID(t *testing.T) {
	cr := &ConnectionResolver{}
	config := map[string]interface{}{
		"uri": "postgres://localhost/mydb",
	}
	result := cr.Resolve(config, models.NodeTypeSourceDB)
	if result["uri"] != "postgres://localhost/mydb" {
		t.Error("expected config unchanged when no conn_id")
	}
}

func TestConnectionResolver_EmptyConnID(t *testing.T) {
	cr := &ConnectionResolver{}
	config := map[string]interface{}{
		"conn_id": "",
		"uri":     "postgres://localhost/mydb",
	}
	result := cr.Resolve(config, models.NodeTypeSourceDB)
	if result["uri"] != "postgres://localhost/mydb" {
		t.Error("expected config unchanged when conn_id is empty")
	}
}
