package common

import "github.com/google/uuid"

// NewID generates a time-sorted UUIDv7 string.
// UUIDv7 embeds a millisecond timestamp in the first 48 bits,
// making IDs naturally sortable by creation time.
// This enables efficient cursor-based pagination using the primary key index.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Fallback to v4 if v7 fails (shouldn't happen)
		return uuid.New().String()
	}
	return id.String()
}
