package models

import (
	"net/url"
	"strconv"
	"time"
)

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
	ConnID      string         `json:"conn_id"` // human-readable slug, e.g. "prod_postgres"
	Type        ConnectionType `json:"type"`
	Description string         `json:"description"`
	Host        string         `json:"host"`
	Port        int            `json:"port,omitempty"`
	Schema      string         `json:"schema"` // database name or path
	Login       string         `json:"login"`
	Password    string         `json:"password,omitempty"` // plaintext in memory, encrypted at rest
	Extra       string         `json:"extra,omitempty"`    // JSON blob for type-specific fields
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// BuildURI constructs a connection URI from the connection fields.
// Uses net/url builder to prevent URI injection via special characters.
func (c *Connection) BuildURI() string {
	switch c.Type {
	case ConnTypePostgres:
		u := &url.URL{Scheme: "postgres"}
		if c.Login != "" {
			if c.Password != "" {
				u.User = url.UserPassword(c.Login, c.Password)
			} else {
				u.User = url.User(c.Login)
			}
		}
		if c.Port > 0 {
			u.Host = c.Host + ":" + strconv.Itoa(c.Port)
		} else {
			u.Host = c.Host
		}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		return u.String()

	case ConnTypeMySQL:
		// MySQL DSN format: user:password@tcp(host:port)/dbname
		u := &url.URL{Scheme: "mysql"}
		if c.Login != "" {
			if c.Password != "" {
				u.User = url.UserPassword(c.Login, c.Password)
			} else {
				u.User = url.User(c.Login)
			}
		}
		host := c.Host
		if c.Port > 0 {
			host = c.Host + ":" + strconv.Itoa(c.Port)
		}
		u.Host = "tcp(" + host + ")"
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		return u.String()

	case ConnTypeSQLite:
		return c.Host // host is the file path

	case ConnTypeHTTP:
		scheme := "https"
		if c.Port == 80 {
			scheme = "http"
		}
		u := &url.URL{Scheme: scheme, Host: c.Host}
		if c.Port > 0 && c.Port != 80 && c.Port != 443 {
			u.Host = c.Host + ":" + strconv.Itoa(c.Port)
		}
		return u.String()

	case ConnTypeSFTP:
		u := &url.URL{Scheme: "sftp"}
		if c.Login != "" {
			if c.Password != "" {
				u.User = url.UserPassword(c.Login, c.Password)
			} else {
				u.User = url.User(c.Login)
			}
		}
		if c.Port > 0 {
			u.Host = c.Host + ":" + strconv.Itoa(c.Port)
		} else {
			u.Host = c.Host
		}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		return u.String()

	case ConnTypeS3:
		u := &url.URL{Scheme: "s3", Host: c.Host}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		return u.String()

	default:
		return c.Host
	}
}
