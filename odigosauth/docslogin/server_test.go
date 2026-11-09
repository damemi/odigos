package docslogin

import (
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testServer(t *testing.T) (*Server, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{
		"enterprise/overview/index.html",
		"enterprise/setup/installation/index.html",
		"assets/site.js",
		"pagefind/index.json",
		"images/private.svg",
		"llms.txt",
		"sitemap.xml",
	} {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("enterprise-private-content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	key := make([]byte, 32)
	rand.Read(key)
	if err := os.WriteFile(filepath.Join(dir, "redirects.json"), []byte(`{"/setup/installation":"/enterprise/setup/installation"}`), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Origin: "https://enterprise.docs.odigos.io", ContentDir: dir, SessionKey: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Unix(1800000000, 0)
	s.now = func() time.Time { return now }
	s.verify = func(key string) (time.Time, error) {
		if key != "valid-key" {
			return time.Time{}, errors.New("invalid")
		}
		return now.Add(time.Hour), nil
	}
	return s, &now
}

func loginRequest(s *Server, key, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", s.origin+"/login", strings.NewReader(url.Values{"api_key": {key}, "redirect": {target}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", s.origin)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestEveryContentRequestRequiresUnexpiredSession(t *testing.T) {
	s, now := testServer(t)
	response := loginRequest(s, "valid-key", "/enterprise/setup/installation/?source=oss#helm")
	if response.Code != 303 || response.Header().Get("Location") != "/enterprise/setup/installation/?source=oss#helm" {
		t.Fatalf("login: %d %s", response.Code, response.Header().Get("Location"))
	}
	cookie := response.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" || cookie.Path != "/" {
		t.Fatal("insecure cookie")
	}
	if !cookie.Expires.Equal(now.Add(time.Hour)) {
		t.Fatal("session outlives license")
	}
	for _, target := range []string{
		"/enterprise/overview/",
		"/enterprise/setup/installation/",
		"/assets/site.js",
		"/pagefind/index.json",
		"/images/private.svg",
		"/llms.txt",
		"/sitemap.xml",
		"/auth/session",
	} {
		for _, method := range []string{"GET", "HEAD"} {
			for _, state := range []string{"anonymous", "valid", "expired", "tampered"} {
				t.Run(method+target+state, func(t *testing.T) {
					saved := *now
					defer func() { *now = saved }()
					req := httptest.NewRequest(method, s.origin+target, nil)
					if state != "anonymous" {
						c := *cookie
						if state == "tampered" {
							c.Value = "bad" + c.Value
						}
						req.AddCookie(&c)
					}
					if state == "expired" {
						*now = cookie.Expires
					}
					w := httptest.NewRecorder()
					s.ServeHTTP(w, req)
					expected := 401
					if state == "valid" {
						expected = 200
					}
					if w.Code != expected {
						t.Fatalf("want %d got %d", expected, w.Code)
					}
					if state != "valid" && strings.Contains(w.Body.String(), "enterprise-private-content") {
						t.Fatal("private content leaked")
					}
					if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
						t.Fatal("cacheable response")
					}
				})
			}
		}
	}
}

func TestInvalidAndExpiredKeysDoNotCreateSession(t *testing.T) {
	s, now := testServer(t)
	for _, key := range []string{"", "malformed"} {
		if w := loginRequest(s, key, "/"); w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Fatal("invalid key accepted")
		}
	}
	s.verify = func(string) (time.Time, error) { return *now, nil }
	if w := loginRequest(s, "expired", "/"); w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("expired key accepted")
	}
}

func TestSessionCapAndNoCredentialLeak(t *testing.T) {
	s, now := testServer(t)
	s.verify = func(string) (time.Time, error) { return now.Add(365 * 24 * time.Hour), nil }
	w := loginRequest(s, "sensitive-api-key", "/")
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	c := w.Result().Cookies()[0]
	if !c.Expires.Equal(now.Add(sessionLifetime)) {
		t.Fatal("missing session cap")
	}
	if strings.Contains(w.Body.String()+w.Header().Get("Location")+c.Value, "sensitive-api-key") {
		t.Fatal("credential leak")
	}
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(c.Value, claims); err != nil {
		t.Fatal(err)
	}
	if len(claims) != 5 {
		t.Fatalf("unexpected session claims: %v", claims)
	}
}

func TestCSRFBodyLimitAndQueryCredentials(t *testing.T) {
	s, _ := testServer(t)
	for _, tc := range []struct {
		method, origin, body, target string
		status                       int
	}{
		{"POST", "https://attacker.example", "api_key=valid-key", "/login", 403},
		{"POST", "", "api_key=valid-key", "/login", 403},
		{"POST", s.origin, "api_key=" + strings.Repeat("a", 17<<10), "/login", 400},
		{"PUT", s.origin, "api_key=valid-key", "/login", 405},
		{"POST", s.origin, "", "/login?api_key=valid-key", 401},
	} {
		req := httptest.NewRequest(tc.method, s.origin+tc.target, strings.NewReader(tc.body))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("got %d want %d", w.Code, tc.status)
		}
	}
}

func TestRedirectValidation(t *testing.T) {
	for _, target := range []string{
		"https://evil.example",
		"//evil.example",
		"/%2fevil.example",
		"/\\evil.example",
		"/%5cevil.example",
		"/a/../login",
		"/login",
		"/auth/session",
		"/%252f%252fevil.example",
		"/\r\nevil",
	} {
		if got := safeRedirect(target); got != "/enterprise/overview/" {
			t.Errorf("unsafe %q became %q", target, got)
		}
	}
}

func TestLegacyRedirectsRequireAuthentication(t *testing.T) {
	s, now := testServer(t)
	cookie := loginRequest(s, "valid-key", "/").Result().Cookies()[0]
	for _, state := range []string{"anonymous", "valid", "expired"} {
		req := httptest.NewRequest("GET", s.origin+"/setup/installation/?from=old-link", nil)
		if state != "anonymous" {
			req.AddCookie(cookie)
		}
		if state == "expired" {
			*now = cookie.Expires
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if state == "valid" {
			if w.Code != 307 || w.Header().Get("Location") != "/enterprise/setup/installation?from=old-link" {
				t.Fatal("legacy link did not preserve target and query")
			}
		} else if w.Code != 401 {
			t.Fatal("legacy redirect bypassed authentication")
		}
	}
}

func TestLogoutAndKeyRotation(t *testing.T) {
	s, _ := testServer(t)
	cookie := loginRequest(s, "valid-key", "/").Result().Cookies()[0]
	req := httptest.NewRequest("POST", s.origin+"/logout", nil)
	req.Header.Set("Origin", s.origin)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 303 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not clear cookie")
	}
	rand.Read(s.key)
	req = httptest.NewRequest("GET", s.origin+"/assets/site.js", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("old session accepted after key rotation")
	}
}

func TestNoDirectoryListingOrTraversal(t *testing.T) {
	s, _ := testServer(t)
	cookie := loginRequest(s, "valid-key", "/").Result().Cookies()[0]
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("outside secret"), 0600)
	if err := s.content.Symlink(outside, "escape.txt"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/assets/", "/../secret.txt", "/%2e%2e/secret.txt", "/escape.txt"} {
		req := httptest.NewRequest("GET", s.origin+target, nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("%s returned %d", target, w.Code)
		}
	}
}

func TestPublicSurfaceAndInvalidConfig(t *testing.T) {
	s, _ := testServer(t)
	for _, target := range []string{"/healthz", "/login", "/login-assets/style.css", "/login-assets/logo_black.svg"} {
		req := httptest.NewRequest("GET", s.origin+target, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s: %d", target, w.Code)
		}
	}
	req := httptest.NewRequest("GET", s.origin+"/enterprise/overview/", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/login?") {
		t.Fatal("anonymous navigation does not open login")
	}
	for _, origin := range []string{
		"",
		"http://example.com",
		"https://example.com/path",
		"https://user@example.com",
		"https://example.com?x=y",
	} {
		server, err := New(Config{Origin: origin, ContentDir: "missing", SessionKey: make([]byte, 32)})
		if err == nil {
			server.Close()
			t.Fatal("invalid config accepted")
		}
	}
}

func BenchmarkSessionVerification(b *testing.B) {
	key := make([]byte, 32)
	rand.Read(key)
	now := time.Now()
	s := &Server{
		key:    key,
		origin: "https://enterprise.docs.odigos.io",
		host:   "enterprise.docs.odigos.io",
		now:    func() time.Time { return now },
	}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    s.origin,
		Audience:  jwt.ClaimStrings{s.host},
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}).SignedString(key)
	req := httptest.NewRequest("GET", s.origin, nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.session(req); err != nil {
			b.Fatal(err)
		}
	}
}
