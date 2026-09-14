package api

import (
	"net/http"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/golang-jwt/jwt/v5"
)

// Who started a run (#241).

// runAttributionFromRequest reads the caller out of the request's claims.
//
// Returns nil when there is nobody to name -- an unauthenticated
// deployment, or a token carrying no subject. Nil means "not recorded",
// which is the honest answer; inventing a placeholder would put a name
// on a run nobody can be held to.
//
// The name is copied here rather than resolved when the run is read.
// A run is a historical fact: the person may later be renamed, removed
// from the organization, or have their token revoked, and resolving
// afterwards would show the run as started by nobody, or by whoever
// holds that id now.
func runAttributionFromRequest(r *http.Request) *models.RunAttribution {
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		return nil
	}
	sub, _ := (*claims)["sub"].(string)
	if sub == "" {
		return nil
	}
	a := &models.RunAttribution{Kind: models.RunTriggerKindUser, UserID: sub}
	// display_name is put in the claims by the login path for exactly
	// this kind of presentation; username is the fallback for accounts
	// that have no display name.
	if name, _ := (*claims)["display_name"].(string); name != "" {
		a.UserName = name
	} else if name, _ := (*claims)["username"].(string); name != "" {
		a.UserName = name
	}
	// A token-authenticated caller is a machine, not a person, even
	// though it carries a subject. The distinction is what lets a UI say
	// "started by the deploy pipeline" rather than naming whoever minted
	// the token years ago.
	if tokenName, _ := (*claims)["token_name"].(string); tokenName != "" {
		a.Kind = models.RunTriggerKindAPIToken
		a.TokenName = tokenName
	}
	return a
}
