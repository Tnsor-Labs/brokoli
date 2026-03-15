package models

import "time"

// ConnectionType identifies the kind of external system.
type ConnectionType string

const (
	ConnTypePostgres ConnectionType = "postgres"
	ConnTypeMySQL    ConnectionType = "mysql"
	ConnTypeSQLite   ConnectionType = "sqlite"
	ConnTypeHTTP     ConnectionType = "http"
	ConnTypeSFTP     ConnectionType = "sftp"
	ConnTypeS3       ConnectionType = "s3"
	ConnTypeGeneric  ConnectionType = "generic"
)

// Connection stores credentials and config for an external system.
type Connection struct {
	ID          string         `json:"id"`
	ConnID      string         `json:"conn_id"`      // human-readable slug, e.g. "prod_postgres"
	Type        ConnectionType `json:"type"`
	Description string         `json:"description"`
	Host        string         `json:"host"`
	Port        int            `json:"port,omitempty"`
	Schema      string         `json:"schema"`        // database name or path
	Login       string         `json:"login"`
	Password    string         `json:"password,omitempty"` // plaintext in memory, encrypted at rest
	Extra       string         `json:"extra,omitempty"`    // JSON blob for type-specific fields
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// BuildURI constructs a connection URI from the connection fields.
func (c *Connection) BuildURI() string {
	switch c.Type {
	case ConnTypePostgres:
		uri := "postgres://"
		if c.Login != "" {
			uri += c.Login
			if c.Password != "" {
				uri += ":" + c.Password
			}
			uri += "@"
		}
		uri += c.Host
		if c.Port > 0 {
			uri += ":" + itoa(c.Port)
		}
		if c.Schema != "" {
			uri += "/" + c.Schema
		}
		return uri
	case ConnTypeMySQL:
		if c.Login == "" {
			return c.Host
		}
		uri := c.Login
		if c.Password != "" {
			uri += ":" + c.Password
		}
		uri += "@tcp(" + c.Host
		if c.Port > 0 {
			uri += ":" + itoa(c.Port)
		}
		uri += ")"
		if c.Schema != "" {
			uri += "/" + c.Schema
		}
		return uri
	case ConnTypeSQLite:
		return c.Host // host is the file path
	case ConnTypeHTTP:
		scheme := "https"
		if c.Port == 80 {
			scheme = "http"
		}
		uri := scheme + "://" + c.Host
		if c.Port > 0 && c.Port != 80 && c.Port != 443 {
			uri += ":" + itoa(c.Port)
		}
		return uri
	default:
		return c.Host
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
