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
// What is hidden: the password and extra values, and the body of an
// encrypted:// reference, which is the credential itself under the
// server's key. What is shown: env://, vault:// and k8s:// references,
// which are the location of a credential, not the credential, and which a
// user needs to see to edit the connection. maskRef (#755) is the one place
// that rule is written.

// maskConnection removes the secret material.
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
