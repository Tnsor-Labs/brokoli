package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// An admin resets a password only where it holds authority over the whole
// account: never a superadmin's, and in multi-tenant mode only a user whose
// organizations are all the admin's current one.
func TestResetPasswordRefusal(t *testing.T) {
	us := newTestUserStore(t)
	mk := func(name string, role Role) string {
		u, err := us.CreateUser(name, "ValidPass123", role)
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	su := mk("operator", RoleSuperAdmin)
	solo := mk("solo", RoleEditor)
	shared := mk("shared", RoleEditor)
	elsewhere := mk("elsewhere", RoleEditor)

	orgsOf := map[string][]string{
		su:        {"org-a"},
		solo:      {"org-a"},
		shared:    {"org-a", "org-b"},
		elsewhere: {"org-b"},
	}
	oldResolver, oldOrgs := OrgResolverFunc, UserOrganizationsFunc
	t.Cleanup(func() { OrgResolverFunc, UserOrganizationsFunc = oldResolver, oldOrgs })
	OrgResolverFunc = func(id string) string {
		if o := orgsOf[id]; len(o) > 0 {
			return o[0]
		}
		return ""
	}

	asAdminOfA := func() *http.Request {
		r := httptest.NewRequest("POST", "/api/auth/admin-reset-password", nil)
		claims := &jwt.MapClaims{"sub": "admin-of-a", "role": "admin"}
		ctx := context.WithValue(r.Context(), "claims", claims)
		return r.WithContext(context.WithValue(ctx, OrgIDContextKey{}, "org-a"))
	}

	t.Run("with membership lists", func(t *testing.T) {
		UserOrganizationsFunc = func(id string) ([]string, error) { return orgsOf[id], nil }
		for _, tc := range []struct {
			target  string
			allowed bool
		}{
			{solo, true},
			{su, false},        // outranks every admin
			{shared, false},    // also in org-b
			{elsewhere, false}, // not in org-a at all
			{"no-such-user", false},
		} {
			got := resetPasswordRefusal(asAdminOfA(), us, tc.target)
			if (got == "") != tc.allowed {
				t.Errorf("target %s: refusal %q, want allowed=%v", tc.target, got, tc.allowed)
			}
		}
	})

	t.Run("membership lookup failing refuses", func(t *testing.T) {
		UserOrganizationsFunc = func(string) ([]string, error) { return nil, errors.New("store down") }
		if got := resetPasswordRefusal(asAdminOfA(), us, solo); got == "" {
			t.Error("a failed membership lookup allowed the reset")
		}
	})

	t.Run("without membership lists, primary organizations decide", func(t *testing.T) {
		UserOrganizationsFunc = nil
		if got := resetPasswordRefusal(asAdminOfA(), us, solo); got != "" {
			t.Errorf("same primary organization refused: %q", got)
		}
		if got := resetPasswordRefusal(asAdminOfA(), us, elsewhere); got == "" {
			t.Error("another primary organization allowed")
		}
		if got := resetPasswordRefusal(asAdminOfA(), us, su); got == "" {
			t.Error("a superadmin target allowed")
		}
	})

	t.Run("single-tenant: any non-superadmin target", func(t *testing.T) {
		OrgResolverFunc, UserOrganizationsFunc = nil, nil
		if got := resetPasswordRefusal(asAdminOfA(), us, elsewhere); got != "" {
			t.Errorf("single-tenant reset refused: %q", got)
		}
		if got := resetPasswordRefusal(asAdminOfA(), us, su); got == "" {
			t.Error("single-tenant: a superadmin target allowed")
		}
	})
}
