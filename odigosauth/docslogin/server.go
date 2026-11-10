package docslogin

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/odigos-io/odigos/odigosauth"
)

const cookieName = "__Host-odigos-docs"
const sessionLifetime = 24 * time.Hour

//go:embed login.html static/*
var assets embed.FS

// Config contains deployment settings. The build directory must never be served
// by a separate public origin or a CDN that bypasses this handler.
type Config struct {
	Origin     string
	ContentDir string
	SessionKey []byte
}

type Server struct {
	origin    string
	host      string
	key       []byte
	content   *os.Root
	login     *template.Template
	verify    func(string) (time.Time, error)
	now       func() time.Time
	slots     chan struct{}
	redirects map[string]string
}

func New(cfg Config) (*Server, error) {
	origin, err := url.Parse(cfg.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" ||
		origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("DOCS_ORIGIN must be an HTTPS origin without a path")
	}
	if len(cfg.SessionKey) < 32 {
		return nil, errors.New("session key must contain at least 32 random bytes")
	}
	root, err := os.OpenRoot(cfg.ContentDir)
	if err != nil {
		return nil, err
	}
	index, err := root.Open("enterprise/overview/index.html")
	if err != nil {
		root.Close()
		return nil, errors.New("content directory must contain a built enterprise/overview/index.html")
	}
	index.Close()
	redirects := make(map[string]string)
	if data, err := root.ReadFile("redirects.json"); err == nil {
		if err := json.Unmarshal(data, &redirects); err != nil {
			root.Close()
			return nil, errors.New("invalid docs redirects.json")
		}
		for source, target := range redirects {
			if safeRedirect(source) != source || safeRedirect(target) != target || source == target {
				root.Close()
				return nil, errors.New("docs redirects must use distinct local content paths")
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		root.Close()
		return nil, err
	}
	return &Server{
		origin:    cfg.Origin,
		host:      origin.Host,
		key:       append([]byte(nil), cfg.SessionKey...),
		content:   root,
		login:     template.Must(template.ParseFS(assets, "login.html")),
		verify:    odigosauth.VerifyOnpremTokenWithExpiry,
		now:       time.Now,
		slots:     make(chan struct{}, 32),
		redirects: redirects,
	}, nil
}

func (s *Server) Close() error { return s.content.Close() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	// Offline docs need only session checks and local search. In particular, do
	// not let the exported preview client poll its development socket endpoint.
	w.Header().Set("Content-Security-Policy",
		"connect-src "+s.origin+"/auth/session "+s.origin+"/pagefind/; "+
			"form-action 'self'; base-uri 'self'; frame-ancestors 'none'",
	)
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Path == "/login" {
		s.handleLogin(w, r)
		return
	}
	if r.URL.Path == "/logout" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method not allowed", 405)
			return
		}
		if !s.sameOrigin(r) {
			http.Error(w, "Invalid request origin", 403)
			return
		}
		s.clearSession(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/login-assets/") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", 405)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/login-assets/")
		if name != "style.css" && name != "logo_black.svg" && name != "logo_white.svg" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, assets, "static/"+name)
		return
	}
	expiry, err := s.session(r)
	if err != nil {
		s.clearSession(w)
		if r.URL.Path == "/auth/session" ||
			(r.Header.Get("Sec-Fetch-Dest") != "document" && !strings.Contains(r.Header.Get("Accept"), "text/html")) {
			http.Error(w, "An unexpired enterprise API key is required", http.StatusUnauthorized)
		} else {
			http.Redirect(
				w, r,
				"/login?"+url.Values{"redirect": {safeRedirect(r.URL.RequestURI())}}.Encode(),
				http.StatusSeeOther,
			)
		}
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", 405)
		return
	}
	if r.URL.Path == "/auth/session" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int64{
			"expiresAt":    expiry.Unix(),
			"serverTimeMs": s.now().UnixMilli(),
		})
		return
	}
	s.serveContent(w, r)
}

func (s *Server) sameOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == s.origin && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'self'; img-src 'self'; "+
			"form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
	)
	target := safeRedirect(r.URL.Query().Get("redirect"))
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		s.renderLogin(w, 200, target, "")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !s.sameOrigin(r) {
		http.Error(w, "Invalid request origin", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid login request", 400)
		return
	}
	target = safeRedirect(r.PostForm.Get("redirect"))
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Please try again shortly", 503)
		return
	}
	expiry, err := s.verify(r.PostForm.Get("api_key"))
	if err != nil || !expiry.After(s.now()) {
		s.renderLogin(w, 401, target,
			"This API key is invalid or expired. Enter a valid enterprise key, or contact Odigos for a renewal.",
		)
		return
	}
	now := s.now()
	if max := now.Add(sessionLifetime); expiry.After(max) {
		expiry = max
	}
	// Integer-second expiration prevents the cookie from outliving the license.
	expiry = time.Unix(expiry.Unix(), 0)
	if !expiry.After(now) {
		s.renderLogin(w, 401, target, "This API key has expired. Enter a renewed enterprise key.")
		return
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "Unable to sign in", 500)
		return
	}
	claims := jwt.RegisteredClaims{
		Issuer:    s.origin,
		Audience:  jwt.ClaimStrings{s.host},
		ID:        base64.RawURLEncoding.EncodeToString(nonce),
		ExpiresAt: jwt.NewNumericDate(expiry),
		IssuedAt:  jwt.NewNumericDate(now),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.key)
	if err != nil {
		http.Error(w, "Unable to sign in", 500)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiry,
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) session(r *http.Request) (time.Time, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || len(cookie.Value) > 4096 {
		return time.Time{}, errors.New("missing session")
	}
	claims := &jwt.RegisteredClaims{}
	_, err = jwt.ParseWithClaims(
		cookie.Value, claims, func(*jwt.Token) (any, error) { return s.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(s.origin),
		jwt.WithAudience(s.host),
		jwt.WithTimeFunc(s.now),
	)
	if err != nil {
		return time.Time{}, err
	}
	return claims.ExpiresAt.Time, nil
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, target, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	s.login.Execute(w, struct{ Redirect, Error string }{target, message})
}

func safeRedirect(raw string) string {
	const fallback = "/enterprise/overview/"
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" ||
		!strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") ||
		strings.ContainsAny(raw, "\\\r\n") || strings.ContainsAny(u.Path, "\\\r\n%") {
		return fallback
	}
	if path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/") && u.Path != "/" {
		return fallback
	}
	if u.Path == "/login" || u.Path == "/logout" || strings.HasPrefix(u.Path, "/auth/") {
		return fallback
	}
	return u.String()
}

func (s *Server) serveContent(w http.ResponseWriter, r *http.Request) {
	lookup := strings.TrimSuffix(r.URL.Path, "/")
	if lookup == "" {
		lookup = "/"
	}
	if target, ok := s.redirects[lookup]; ok {
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	name = strings.TrimSuffix(name, "/")
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	file, err := s.content.Open(name)
	if err == nil {
		info, statErr := file.Stat()
		if statErr != nil || info.IsDir() {
			file.Close()
			file, err = s.content.Open(path.Join(name, "index.html"))
		}
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, file.Name(), info.ModTime(), file)
}
