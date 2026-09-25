package api

import "github.com/Tnsor-Labs/brokoli/models"

// Masking a connection before it goes on the wire.
//
// This exists as one function because it used to be written out at each
// call site, and the copies drifted: the unpaged list masked the
// password, the extra blob and both secret references, while the paged
// branch three lines above it masked only the first two. So
// GET /connections hid the references and GET /connections?page=1
// returned them.
//
// A secret reference is not a secret, but it is the location of one --
// "encrypted://<ciphertext>", "env://NAME", a vault path. Handing that
// out tells a reader which ciphertext to attack or which variable to
// read, and the product deliberately hides it everywhere else.

// maskConnection removes the secret material and the location of it.
func maskConnection(c *models.Connection) {
	if c == nil {
		return
	}
	c.Password = ""
	c.Extra = ""
	c.PasswordRef = maskRef(c.PasswordRef)
	c.ExtraRef = maskRef(c.ExtraRef)
}

// maskConnections masks a listing in place.
func maskConnections(conns []models.Connection) {
	for i := range conns {
		maskConnection(&conns[i])
	}
}
