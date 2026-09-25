package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Editing your own profile (#604).
//
// The store already had SetProfile, and nothing outside tests ever called
// it. It also cannot clear a field: an empty string means "leave this
// alone", which is the right reading for "fill in what the SSO provider
// gave us" and the wrong one for "the user emptied the box". So the
// endpoint does not forward to it.

// ProfileUpdate carries the fields a user may change about their own
// account. Each is a pointer so that absent and empty are different
// things: a field the caller did not send keeps its value, and a field
// sent as "" is cleared. A struct of plain strings cannot express the
// difference, and every partial update would blank the rest.
type ProfileUpdate struct {
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	AvatarURL   *string `json:"avatar_url"`
}

const (
	maxDisplayNameLen = 128
	maxEmailLen       = 254 // RFC 5321 section 4.5.3.1.3
	maxAvatarURLLen   = 2048
)

// Validate reports the first problem with an update, or nil.
func (p ProfileUpdate) Validate() error {
	if p.DisplayName != nil {
		name := strings.TrimSpace(*p.DisplayName)
		if len(name) > maxDisplayNameLen {
			return fmt.Errorf("display name must be at most %d characters", maxDisplayNameLen)
		}
		if strings.ContainsAny(name, "\r\n") {
			return fmt.Errorf("display name must not contain line breaks")
		}
	}
	if p.Email != nil {
		addr := strings.TrimSpace(*p.Email)
		if addr != "" {
			if len(addr) > maxEmailLen {
				return fmt.Errorf("email must be at most %d characters", maxEmailLen)
			}
			if _, err := mail.ParseAddress(addr); err != nil {
				return fmt.Errorf("email is not a valid address")
			}
		}
	}
	if p.AvatarURL != nil {
		raw := strings.TrimSpace(*p.AvatarURL)
		if raw != "" {
			if len(raw) > maxAvatarURLLen {
				return fmt.Errorf("avatar URL must be at most %d characters", maxAvatarURLLen)
			}
			u, err := url.Parse(raw)
			// http and https only. An avatar is rendered as an image
			// source, so a javascript: or data: value is a script the
			// server would be handing to every viewer of the members
			// list, not a picture.
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("avatar URL must be an absolute http or https URL")
			}
		}
	}
	return nil
}

// normalized returns the update with each present field trimmed.
func (p ProfileUpdate) normalized() ProfileUpdate {
	trim := func(s *string) *string {
		if s == nil {
			return nil
		}
		t := strings.TrimSpace(*s)
		return &t
	}
	return ProfileUpdate{
		DisplayName: trim(p.DisplayName),
		Email:       trim(p.Email),
		AvatarURL:   trim(p.AvatarURL),
	}
}

// ErrEmailInUse is returned when another account already holds the
// address. The column has no unique constraint, because installs predate
// it and may already hold duplicates; refusing new collisions is what can
// be enforced without rejecting data that is already there.
var ErrEmailInUse = fmt.Errorf("email is already used by another account")

// UpdateProfile writes the present fields of an update and returns the
// account as it now stands.
func (us *UserStore) UpdateProfile(userID string, p ProfileUpdate) (*User, error) {
	p = p.normalized()
	if err := p.Validate(); err != nil {
		return nil, err
	}

	if p.Email != nil && *p.Email != "" {
		var other string
		err := us.db.QueryRow(
			us.q(`SELECT id FROM users WHERE email = ? AND id <> ?`), *p.Email, userID,
		).Scan(&other)
		if err == nil && other != "" {
			return nil, ErrEmailInUse
		}
	}

	// Assembled from a fixed set of literal column fragments, never from
	// caller input, so the statement stays obviously parameterized. The
	// same reason SetProfile spells its three cases out; here there are
	// eight combinations, which is past the point where writing them all
	// out is clearer.
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if p.DisplayName != nil {
		sets = append(sets, `display_name = ?`)
		args = append(args, *p.DisplayName)
	}
	if p.Email != nil {
		sets = append(sets, `email = ?`)
		args = append(args, *p.Email)
	}
	if p.AvatarURL != nil {
		sets = append(sets, `avatar_url = ?`)
		args = append(args, *p.AvatarURL)
	}
	if len(sets) == 0 {
		// Nothing to write. Still return the account, so a caller that
		// sent an empty body gets the same shape as one that changed
		// something.
		return us.GetUserByID(userID)
	}
	args = append(args, userID)

	res, err := us.db.Exec(us.q(`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`), args...)
	if err != nil {
		return nil, err
	}
	// A profile write that matched no row means the token names an
	// account that no longer exists. Reporting success there would show
	// the user their edit was saved when nothing was.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil, fmt.Errorf("user not found")
	}
	return us.GetUserByID(userID)
}

// UpdateProfileHandler lets the calling user edit their own profile.
//
// The account is taken from the token's subject, never from the body, so
// there is no field an attacker could set to edit somebody else.
func UpdateProfileHandler(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		userID, _ := (*claims)["sub"].(string)
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		var p ProfileUpdate
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		user, err := us.UpdateProfile(userID, p)
		switch {
		case err == ErrEmailInUse:
			writeError(w, http.StatusConflict, err.Error())
			return
		case err != nil && err.Error() == "user not found":
			writeError(w, http.StatusNotFound, "user not found")
			return
		case err != nil:
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, user)
	}
}
