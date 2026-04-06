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

func TestBuildConnectionURI(t *testing.T) {
	tests := []struct {
		name     string
		connType string
		host     string
		port     int
		schema   string
		login    string
		password string
		extra    string
		want     string
	}{
		{
			name:     "postgres default port",
			connType: "postgres", host: "db.example.com", port: 0,
			schema: "mydb", login: "user", password: "pass",
			want: "postgres://user:pass@db.example.com:5432/mydb?sslmode=require",
		},
		{
			name:     "redshift default port",
			connType: "redshift", host: "cluster.us-east-1.redshift.amazonaws.com", port: 0,
			schema: "analytics", login: "admin", password: "secret",
			want: "redshift://admin:secret@cluster.us-east-1.redshift.amazonaws.com:5439/analytics?sslmode=require",
		},
		{
			name:     "snowflake with warehouse",
			connType: "snowflake", host: "acme.snowflakecomputing.com", port: 0,
			schema: "PROD/PUBLIC", login: "svc", password: "pw",
			extra: `{"warehouse": "ETL_WH"}`,
			want:  "snowflake://svc:pw@acme.snowflakecomputing.com/PROD/PUBLIC?warehouse=ETL_WH",
		},
		{
			name:     "mysql",
			connType: "mysql", host: "mysql.example.com", port: 3306,
			schema: "app", login: "root", password: "pw",
			want: "root:pw@tcp(mysql.example.com:3306)/app",
		},
		{
			name:     "mssql",
			connType: "mssql", host: "sql.example.com", port: 1433,
			schema: "mydb", login: "sa", password: "pw",
			want: "sqlserver://sa:pw@sql.example.com:1433?database=mydb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildConnectionURI(tt.connType, tt.host, tt.port, tt.schema, tt.login, tt.password, tt.extra)
			if got != tt.want {
				t.Errorf("BuildConnectionURI() = %q, want %q", got, tt.want)
			}
		})
	}
}
