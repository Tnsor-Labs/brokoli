package engine

import (
	"testing"
)

func TestDetectDriver(t *testing.T) {
	tests := []struct {
		uri        string
		wantDriver string
		wantDSN    string
	}{
		{"postgres://user:pass@host:5432/db", "pgx", "postgres://user:pass@host:5432/db"},
		{"postgresql://user:pass@host/db", "pgx", "postgresql://user:pass@host/db"},
		{"redshift://user:pass@cluster.us-east-1.redshift.amazonaws.com:5439/db", "pgx", "postgres://user:pass@cluster.us-east-1.redshift.amazonaws.com:5439/db"},
		{"snowflake://user:pass@account/db/schema?warehouse=WH", "snowflake", "user:pass@account/db/schema?warehouse=WH"},
		{"mysql://user:pass@host:3306/db", "mysql", "user:pass@host:3306/db"},
		{"sqlite://test.db", "sqlite", "test.db"},
		{"test.db", "sqlite", "test.db"},
		{"sqlserver://user:pass@host:1433?database=db", "sqlserver", "sqlserver://user:pass@host:1433?database=db"},
		{"mssql://user:pass@host:1433?database=db", "sqlserver", "mssql://user:pass@host:1433?database=db"},
		// Default falls through to pgx
		{"host:5432/db", "pgx", "host:5432/db"},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			driver, dsn, err := DetectDriver(tt.uri)
			if err != nil {
				t.Fatalf("DetectDriver(%q) error: %v", tt.uri, err)
			}
			if driver != tt.wantDriver {
				t.Errorf("driver = %q, want %q", driver, tt.wantDriver)
			}
			if dsn != tt.wantDSN {
				t.Errorf("dsn = %q, want %q", dsn, tt.wantDSN)
			}
		})
	}
}
