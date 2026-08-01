package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetSessionCookieUsesForwardedHTTPS(t *testing.T) {
	req := httptest.NewRequest("GET", "http://brokoli.test/callback", nil)
	req.Header.Set("X-Forwarded-Proto", "HTTPS")
	rec := httptest.NewRecorder()

	SetSessionCookie(rec, req, "signed-token")

	cookie := rec.Header().Get("Set-Cookie")
	for _, want := range []string{"brokoli_session=signed-token", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("Set-Cookie = %q, want %q", cookie, want)
		}
	}
}

func TestClearSessionCookieExpiresSession(t *testing.T) {
	req := httptest.NewRequest("GET", "http://brokoli.test/logout", nil)
	rec := httptest.NewRecorder()

	ClearSessionCookie(rec, req)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "brokoli_session" || cookies[0].MaxAge != -1 {
		t.Fatalf("unexpected cookies: %#v", cookies)
	}
}
