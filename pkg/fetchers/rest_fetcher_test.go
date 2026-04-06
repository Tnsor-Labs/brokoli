package fetchers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/errors"
)

func TestRESTFetcher_Fetch(t *testing.T) {
	// Setup test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/array":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			errors.CheckErrorMultiple(w.Write([]byte(`[
				{"id": 1, "name": "John", "age": 30},
				{"id": 2, "name": "Jane", "age": 25}
			]`)))
		case "/object":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			errors.CheckErrorMultiple(w.Write([]byte(`{"id": 1, "name": "John", "age": 30}`)))
		case "/complex":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			errors.CheckErrorMultiple(w.Write([]byte(`{
				"id": 1, 
				"name": "John", 
				"address": {
					"street": "123 Main St",
					"city": "Anytown"
				},
				"tags": ["developer", "golang"]
			}`)))
		case "/empty":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			errors.CheckErrorMultiple(w.Write([]byte(`[]`)))
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
			errors.CheckErrorMultiple(w.Write([]byte(`{"error": "Internal Server Error"}`)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tests := []struct {
		name        string
		source      string
		options     map[string]interface{}
		wantErr     bool
		expectedLen int
	}{
		{
			name:        "Fetch array of objects",
			source:      server.URL + "/array",
			options:     map[string]interface{}{},
			wantErr:     false,
			expectedLen: 2,
		},
		{
			name:        "Fetch single object",
			source:      server.URL + "/object",
			options:     map[string]interface{}{},
			wantErr:     false,
			expectedLen: 1,
		},
		{
			name:        "Fetch complex object",
			source:      server.URL + "/complex",
			options:     map[string]interface{}{},
			wantErr:     false,
			expectedLen: 1,
		},
		{
			name:        "Fetch with custom headers",
			source:      server.URL + "/array",
			options:     map[string]interface{}{"headers": map[string]string{"X-Custom-Header": "test"}},
			wantErr:     false,
			expectedLen: 2,
		},
		{
			name:        "Fetch with timeout",
			source:      server.URL + "/array",
			options:     map[string]interface{}{"timeout": 5 * time.Second},
			wantErr:     false,
			expectedLen: 2,
		},
		{
			name:        "Fetch empty response",
			source:      server.URL + "/empty",
			options:     map[string]interface{}{},
			wantErr:     true,
			expectedLen: 0,
		},
		{
			name:        "Fetch error response",
			source:      server.URL + "/error",
			options:     map[string]interface{}{},
			wantErr:     true,
			expectedLen: 0,
		},
		{
			name:        "Invalid URL",
			source:      "invalid-url",
			options:     map[string]interface{}{},
			wantErr:     true,
			expectedLen: 0,
		},
		{
			name:        "Empty URL",
			source:      "",
			options:     map[string]interface{}{},
			wantErr:     true,
			expectedLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &RESTFetcher{}
			result, err := f.Fetch(tt.source, tt.options)

			if (err != nil) != tt.wantErr {
				t.Errorf("RESTFetcher.Fetch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if result == nil {
					t.Errorf("RESTFetcher.Fetch() returned nil result when no error was expected")
					return
				}

				if len(result.Rows) != tt.expectedLen {
					t.Errorf("RESTFetcher.Fetch() returned %d rows, expected %d", len(result.Rows), tt.expectedLen)
				}

				if len(result.Columns) == 0 {
					t.Errorf("RESTFetcher.Fetch() returned empty columns")
				}
			}
		})
	}
}

func TestGetFetcher(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
		wantType   string
		wantErr    bool
	}{
		{
			name:       "Get REST fetcher",
			sourceType: "rest",
			wantType:   "*fetchers.RESTFetcher",
			wantErr:    false,
		},
		{
			name:       "Get unsupported fetcher",
			sourceType: "unsupported",
			wantType:   "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher, err := GetFetcher(tt.sourceType)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetFetcher() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if fetcher == nil {
					t.Errorf("GetFetcher() returned nil fetcher when no error was expected")
					return
				}

				actualType := fmt.Sprintf("%T", fetcher)
				if actualType != tt.wantType {
					t.Errorf("GetFetcher() returned %s, expected %s", actualType, tt.wantType)
				}
			}
		})
	}
}
