package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Connector is the interface for data source/sink plugins.
type Connector interface {
	// Type returns the connector type name (e.g., "s3", "gcs", "kafka").
	Type() string
	// Read fetches data from the source, returning a DataSet.
	Read(config map[string]interface{}) (*common.DataSet, error)
	// Write sends data to the sink.
	Write(config map[string]interface{}, data *common.DataSet) error
}

// ConnectorRegistry holds registered connectors.
type ConnectorRegistry struct {
	mu         sync.RWMutex
	connectors map[string]Connector
}

// Global registry
var Registry = &ConnectorRegistry{
	connectors: make(map[string]Connector),
}

// Register adds a connector to the registry.
func (cr *ConnectorRegistry) Register(c Connector) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.connectors[c.Type()] = c
}

// Get returns a connector by type name.
func (cr *ConnectorRegistry) Get(typeName string) (Connector, bool) {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	c, ok := cr.connectors[typeName]
	return c, ok
}

// List returns all registered connector type names.
func (cr *ConnectorRegistry) List() []string {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	var types []string
	for t := range cr.connectors {
		types = append(types, t)
	}
	return types
}

// --- HTTP Connector ---
// A generic connector that calls an external HTTP endpoint.
// Config: {"url": "https://...", "method": "GET/POST", "headers": {...}}

type HTTPConnector struct {
	name string
}

func NewHTTPConnector(name string) *HTTPConnector {
	return &HTTPConnector{name: name}
}

func (c *HTTPConnector) Type() string { return c.name }

func (c *HTTPConnector) Read(config map[string]interface{}) (*common.DataSet, error) {
	url, _ := config["url"].(string)
	if url == "" {
		return nil, fmt.Errorf("http connector requires 'url'")
	}

	method, _ := config["method"].(string)
	if method == "" {
		method = "GET"
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}

	// Apply headers
	if headers, ok := config["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Parse as JSON array of objects
	var rows []common.DataRow
	if err := json.Unmarshal(body, &rows); err != nil {
		// Try single object
		var row common.DataRow
		if err2 := json.Unmarshal(body, &row); err2 != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}
		rows = []common.DataRow{row}
	}

	// Extract columns from first row
	var columns []string
	if len(rows) > 0 {
		for k := range rows[0] {
			columns = append(columns, k)
		}
	}

	return &common.DataSet{Columns: columns, Rows: rows}, nil
}

func (c *HTTPConnector) Write(config map[string]interface{}, data *common.DataSet) error {
	url, _ := config["url"].(string)
	if url == "" {
		return fmt.Errorf("http connector requires 'url'")
	}

	body, err := json.Marshal(data.Rows)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("http write failed: status %d", resp.StatusCode)
	}
	return nil
}

// RegisterBuiltinConnectors registers default connectors.
func RegisterBuiltinConnectors() {
	Registry.Register(NewHTTPConnector("http"))
}
