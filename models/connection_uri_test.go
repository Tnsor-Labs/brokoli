package models

import (
	"net/url"
	"strings"
	"testing"
)

// The engine used to carry a second URI builder that handled Redshift,
// Snowflake, and SQL Server and pinned sslmode=require. Nothing called it, so
// those connection types actually fell through to a bare hostname and no
// Postgres connection ever asked for TLS. These cases are the ones that dead
// builder claimed, asserted against the builder the engine really uses.
func TestBuildURI(t *testing.T) {
	tests := []struct {
		name string
		conn Connection
		want string
	}{
		{
			name: "postgres fills in the default port",
			conn: Connection{Type: ConnTypePostgres, Host: "db.example.com",
				Schema: "mydb", Login: "user", Password: "pass"},
			want: "postgres://user:pass@db.example.com:5432/mydb",
		},
		{
			name: "postgres honours an explicit sslmode",
			conn: Connection{Type: ConnTypePostgres, Host: "db.example.com", Port: 5432,
				Schema: "mydb", Login: "user", Password: "pass",
				Extra: `{"sslmode":"require"}`},
			want: "postgres://user:pass@db.example.com:5432/mydb?sslmode=require",
		},
		{
			name: "redshift keeps its own default port",
			conn: Connection{Type: ConnTypeRedshift, Host: "cluster.us-east-1.redshift.amazonaws.com",
				Schema: "analytics", Login: "admin", Password: "secret",
				Extra: `{"sslmode":"verify-full"}`},
			want: "redshift://admin:secret@cluster.us-east-1.redshift.amazonaws.com:5439/analytics?sslmode=verify-full",
		},
		{
			name: "snowflake carries its warehouse",
			conn: Connection{Type: ConnTypeSnowflake, Host: "acme.snowflakecomputing.com",
				Schema: "PROD", Login: "svc", Password: "pw",
				Extra: `{"warehouse":"ETL_WH"}`},
			want: "snowflake://svc:pw@acme.snowflakecomputing.com/PROD?warehouse=ETL_WH",
		},
		{
			name: "mysql keeps the tcp() DSN shape",
			conn: Connection{Type: ConnTypeMySQL, Host: "mysql.example.com", Port: 3306,
				Schema: "app", Login: "root", Password: "pw"},
			want: "mysql://root:pw@tcp(mysql.example.com:3306)/app",
		},
		{
			name: "mysql takes tls and charset from extra",
			conn: Connection{Type: ConnTypeMySQL, Host: "mysql.example.com", Port: 3306,
				Schema: "app", Login: "root", Password: "pw",
				Extra: `{"tls":"true","charset":"utf8mb4","parseTime":true}`},
			want: "mysql://root:pw@tcp(mysql.example.com:3306)/app?charset=utf8mb4&parseTime=true&tls=true",
		},
		{
			name: "sqlserver puts the database in the query",
			conn: Connection{Type: ConnTypeMSSQL, Host: "sql.example.com", Port: 1433,
				Schema: "mydb", Login: "sa", Password: "pw"},
			want: "sqlserver://sa:pw@sql.example.com:1433?database=mydb",
		},
		{
			name: "an option the type does not allow is dropped",
			conn: Connection{Type: ConnTypePostgres, Host: "db", Port: 5432, Schema: "d",
				Login: "u", Password: "p",
				Extra: `{"sslmode":"require","dbname":"attacker","host":"evil.example.com"}`},
			want: "postgres://u:p@db:5432/d?sslmode=require",
		},
		{
			name: "an unreadable extra does not stop the connection",
			conn: Connection{Type: ConnTypePostgres, Host: "db", Port: 5432, Schema: "d",
				Login: "u", Password: "p", Extra: "not json at all"},
			want: "postgres://u:p@db:5432/d",
		},
		{
			name: "sqlite is a path, not a URI",
			conn: Connection{Type: ConnTypeSQLite, Host: "/data/app.db"},
			want: "/data/app.db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.conn.BuildURI(); got != tt.want {
				t.Errorf("BuildURI()\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// The builder this replaced used fmt.Sprintf, so a password containing "@" or
// "/" silently changed which host the URI pointed at. Escaping is the whole
// reason the live builder goes through net/url, and the property worth
// asserting is not the exact encoding but that the URI still parses back to
// the host and credential the record declared.
func TestBuildURIEscapesCredentials(t *testing.T) {
	for _, pw := range []string{"p@ss/w0rd", "a:b@c", "pa ss", "sl/ash?q=1", "100%pw", "#hash"} {
		c := Connection{Type: ConnTypePostgres, Host: "db.internal", Port: 5432,
			Schema: "analytics", Login: "etl@corp", Password: pw}
		raw := c.BuildURI()

		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("password %q produced an unparseable URI %q: %v", pw, raw, err)
		}
		if u.Host != "db.internal:5432" {
			t.Errorf("password %q redirected the connection to %q (URI %q)", pw, u.Host, raw)
		}
		if got := u.User.Username(); got != "etl@corp" {
			t.Errorf("password %q corrupted the login: got %q", pw, got)
		}
		got, _ := u.User.Password()
		if got != pw {
			t.Errorf("password %q did not survive the round trip: got %q", pw, got)
		}
		if u.Path != "/analytics" {
			t.Errorf("password %q corrupted the database name: got %q", pw, u.Path)
		}
	}
}

// Every type the connection catalog offers must either build a URI or say it
// cannot. The failure this guards against is silent: a type with no driver
// used to fall through to the bare hostname, which reaches the Postgres driver
// as a malformed DSN and fails with a message naming neither the connection
// nor the reason.
func TestBuildsURICoversTheCatalog(t *testing.T) {
	withDriver := []ConnectionType{
		ConnTypePostgres, ConnTypeRedshift, ConnTypeMySQL, ConnTypeSQLite,
		ConnTypeMSSQL, ConnTypeSnowflake, ConnTypeHTTP, ConnTypeSFTP, ConnTypeS3,
	}
	withoutDriver := []ConnectionType{
		ConnTypeBigQuery, ConnTypeDatabricks, ConnTypeOracle,
		ConnTypeAzureBlob, ConnTypeGCS, ConnTypeGeneric,
	}

	for _, ct := range withDriver {
		c := Connection{Type: ct, Host: "h", Port: 1, Schema: "s", Login: "u", Password: "p"}
		if !c.BuildsURI() {
			t.Errorf("%s has a driver but BuildsURI() says otherwise", ct)
		}
		// SQLite is the one type whose URI legitimately is the bare host: the
		// host field holds the file path.
		if ct == ConnTypeSQLite {
			continue
		}
		if got := c.BuildURI(); got == "" || got == "h" {
			t.Errorf("%s claims a URI but built %q — the bare-hostname fallback", ct, got)
		}
	}
	for _, ct := range withoutDriver {
		c := Connection{Type: ct, Host: "h", Port: 1, Schema: "s", Login: "u", Password: "p"}
		if c.BuildsURI() {
			t.Errorf("%s has no engine driver but BuildsURI() claims a URI", ct)
		}
	}
}

// sslmode must reach the driver as a parameter, not as part of the database
// name. Parsing is what the driver does, so parse it the same way.
func TestSSLModeLandsInTheQueryString(t *testing.T) {
	c := Connection{Type: ConnTypePostgres, Host: "db", Port: 5432, Schema: "analytics",
		Login: "u", Password: "p", Extra: `{"sslmode":"verify-full","sslrootcert":"/certs/ca.pem"}`}
	u, err := url.Parse(c.BuildURI())
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("sslmode"); got != "verify-full" {
		t.Errorf("sslmode = %q, want verify-full", got)
	}
	if got := u.Query().Get("sslrootcert"); got != "/certs/ca.pem" {
		t.Errorf("sslrootcert = %q", got)
	}
	if strings.Contains(u.Path, "sslmode") {
		t.Errorf("sslmode leaked into the database name: %q", u.Path)
	}
}
