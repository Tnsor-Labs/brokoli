package store

import (
	"encoding/json"
	"fmt"

	"github.com/Tnsor-Labs/brokoli/models"
)

// connectionDriverIdentity encodes a connection's pinned native driver for
// its TEXT column. The digest is stored lowercased so every later comparison
// (removal's pinned check, capability routing) is a plain string match.
func connectionDriverIdentity(c *models.Connection) (string, error) {
	if c.DriverIdentity == nil {
		return "", nil
	}
	b, err := json.Marshal(c.DriverIdentity.Normalized())
	if err != nil {
		return "", fmt.Errorf("encode connection driver identity: %w", err)
	}
	return string(b), nil
}

func setConnectionDriverIdentity(c *models.Connection, raw string) error {
	if raw == "" {
		return nil
	}
	var identity models.DriverIdentity
	if err := json.Unmarshal([]byte(raw), &identity); err != nil {
		return fmt.Errorf("decode connection driver identity: %w", err)
	}
	identity = identity.Normalized()
	c.DriverIdentity = &identity
	return nil
}
