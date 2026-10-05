package store

import (
	"encoding/json"
	"fmt"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
)

func connectionDriverIdentity(c *models.Connection) (string, error) {
	if c.DriverIdentity == nil {
		return "", nil
	}
	b, err := json.Marshal(c.DriverIdentity)
	if err != nil {
		return "", fmt.Errorf("encode connection driver identity: %w", err)
	}
	return string(b), nil
}

func setConnectionDriverIdentity(c *models.Connection, raw string) error {
	if raw == "" {
		return nil
	}
	var identity struct {
		Name          string `json:"name"`
		Version       string `json:"version"`
		LibrarySHA256 string `json:"library_sha256"`
	}
	if err := json.Unmarshal([]byte(raw), &identity); err != nil {
		return fmt.Errorf("decode connection driver identity: %w", err)
	}
	c.DriverIdentity = &drivers.DriverIdentity{Name: identity.Name, Version: identity.Version, LibrarySHA256: identity.LibrarySHA256}
	return nil
}
