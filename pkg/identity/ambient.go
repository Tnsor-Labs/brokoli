package identity

import (
	"errors"
	"os"
	"strings"
)

// AmbientEnv switches off ambient identity: the credentials of the machine
// the process runs on (a cloud instance role, Application Default
// Credentials, a Kubernetes service account, a token a platform projects
// onto the machine).
//
// On a server that runs pipelines for one team, the machine's identity is the
// team's, and using it is the simplest setup there is. On a server that runs
// pipelines for several teams or customers, it is the operator's: a
// workspace allowed to use it would read the operator's resources with the
// operator's role. Such a deployment sets this to "deny" on every machine it
// runs (ADR-041 section 4).
const AmbientEnv = "BROKOLI_SECRET_STORE_AMBIENT"

// ErrAmbientDenied is returned when a backend asks for ambient identity on a
// machine that denies it. It says the method is switched off here, not that
// the cloud refused, so nobody goes looking for an IAM problem.
var ErrAmbientDenied = errors.New("identity: ambient identity is disabled on this deployment (" + AmbientEnv + "=deny)")

// AmbientAllowed reports whether ambient identity may be used. Anything
// other than "deny" allows it, so a single-team deployment needs no setting.
func AmbientAllowed() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(AmbientEnv)), "deny")
}
