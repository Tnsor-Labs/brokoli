package models

import "strings"

// DriverIdentity pins the exact native ADBC driver build a connection runs
// with. LibrarySHA256 is the digest of the installed shared library, not of
// the archive it was distributed in. It is metadata, not a credential, and is
// safe to persist and return.
//
// It lives here rather than in pkg/drivers so the model package stays a leaf:
// pkg/drivers downloads, extracts and verifies archives, and a connection
// only needs to name what it was pinned to.
type DriverIdentity struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	LibrarySHA256 string `json:"library_sha256"`
}

// Normalized returns the identity with its digest lowercased, the one form
// it is stored, compared and routed in.
func (d DriverIdentity) Normalized() DriverIdentity {
	d.LibrarySHA256 = strings.ToLower(d.LibrarySHA256)
	return d
}

// Key is a comparison key for exact identity matches.
func (d DriverIdentity) Key() string {
	n := d.Normalized()
	return n.Name + "\x00" + n.Version + "\x00" + n.LibrarySHA256
}
