// Package auth protects the dashboard with a local account: Argon2id
// password hashes, random session tokens stored hashed, and login
// throttling.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aganet/tikscope/internal/store"
)

const (
	cookieName   = "tikscope_session"
	tokenLen     = 32
	sessionTTL   = 30 * 24 * time.Hour
	idleTTL      = 7 * 24 * time.Hour
	touchEvery   = 5 * time.Minute
	maxBody      = 4096
	purgeEvery   = time.Hour
	hashParallel = 2 // bounds memory held by concurrent Argon2 runs
)

type Service struct {
	store      *store.Store
	log        *slog.Logger
	setupToken string
	limiter    *limiter
	hashSem    chan struct{}
	dummyHash  string
}

type ctxKey struct{}

// New prepares the service. setupToken is required by the registration
// page while no account exists.
func New(st *store.Store, setupToken string, log *slog.Logger) *Service {
	s := &Service{store: st, log: log, setupToken: setupToken, limiter: newLimiter(), hashSem: make(chan struct{}, hashParallel)}
	// Unknown usernames are verified against this hash so the response time
	// does not reveal whether an account exists.
	s.dummyHash = HashPassword(NewSetupToken())
	return s
}

// NewSetupToken returns a random token for the registration page.
func NewSetupToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// SessionFrom returns the session attached to an authenticated request.
func SessionFrom(ctx context.Context) (store.Session, bool) {
	s, ok := ctx.Value(ctxKey{}).(store.Session)
	return s, ok
}

// Protect gates the dashboard and API behind a session. Static assets and
// the auth endpoints stay reachable so the login page can load.
func (s *Service) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/healthz" || strings.HasPrefix(path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		if sess, ok := s.authenticate(r); ok {
			if path == "/login" {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
			return
		}
		switch {
		case strings.HasPrefix(path, "/api/"):
			writeError(w, "authentication required", http.StatusUnauthorized)
		case path == "/" || path == "/index.html":
			http.Redirect(w, r, "/login", http.StatusFound)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// Routes registers the auth endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", s.status)
	mux.HandleFunc("POST /api/auth/setup", s.setup)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("POST /api/auth/password", s.changePassword)
}

// Run purges expired sessions until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(purgeEvery)
	defer t.Stop()
	for {
		if err := s.store.PurgeSessions(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.Warn("purge sessions", "err", err)
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) authenticate(r *http.Request) (store.Session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return store.Session{}, false
	}
	hash, ok := hashToken(c.Value)
	if !ok {
		return store.Session{}, false
	}
	now := time.Now()
	sess, err := s.store.SessionByToken(r.Context(), hash, now)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && r.Context().Err() == nil {
			s.log.Error("session lookup", "err", err)
		}
		return store.Session{}, false
	}
	if now.Sub(sess.LastSeen) > idleTTL {
		s.store.DeleteSession(r.Context(), hash)
		return store.Session{}, false
	}
	if now.Sub(sess.LastSeen) > touchEvery {
		if err := s.store.TouchSession(r.Context(), hash, now); err != nil && r.Context().Err() == nil {
			s.log.Warn("touch session", "err", err)
		}
	}
	return sess, true
}

func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.UserCount(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	out := map[string]any{"setup_required": n == 0, "authenticated": false}
	if sess, ok := s.authenticate(r); ok {
		out["authenticated"] = true
		out["username"] = sess.Username
		out["since"] = sess.Created.Unix()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, out)
}

func (s *Service) setup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password, Token string }
	if !s.readJSON(w, r, &in) {
		return
	}
	ip := clientIP(r)
	if s.throttled(w, "ip:"+ip) {
		return
	}
	n, err := s.store.UserCount(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	if n > 0 {
		writeError(w, "an account already exists, sign in instead", http.StatusForbidden)
		return
	}
	if s.setupToken == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(in.Token)), []byte(s.setupToken)) != 1 {
		s.limiter.fail("ip:"+ip, time.Now())
		s.log.Warn("setup token rejected", "ip", ip)
		writeError(w, "setup token does not match the one in the collector log", http.StatusForbidden)
		return
	}
	username, err := NormalizeUsername(in.Username)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := CheckPassword(username, in.Password); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	hash := s.hash(in.Password)
	if err := s.store.CreateFirstUser(r.Context(), username, hash); err != nil {
		if errors.Is(err, store.ErrSetupDone) {
			writeError(w, "an account already exists, sign in instead", http.StatusForbidden)
			return
		}
		s.internal(w, err)
		return
	}
	s.limiter.reset("ip:" + ip)
	s.log.Info("account created", "user", username, "ip", ip)
	s.startSession(w, r, username)
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if !s.readJSON(w, r, &in) {
		return
	}
	ip := clientIP(r)
	username := strings.ToLower(strings.TrimSpace(in.Username))
	if s.throttled(w, "ip:"+ip) || s.throttled(w, "user:"+username) {
		return
	}
	if len(in.Password) > MaxPasswordLen || username == "" {
		s.reject(w, ip, username)
		return
	}
	user, err := s.store.UserByName(r.Context(), username)
	hash := s.dummyHash
	if err == nil {
		hash = user.PasswordHash
	} else if !errors.Is(err, store.ErrNotFound) {
		s.internal(w, err)
		return
	}
	ok, stale, verr := s.verify(hash, in.Password)
	if verr != nil {
		s.internal(w, verr)
		return
	}
	if !ok || user.ID == 0 {
		s.reject(w, ip, username)
		return
	}
	if stale {
		if err := s.store.SetPassword(r.Context(), user.ID, s.hash(in.Password)); err != nil {
			s.log.Warn("rehash password", "err", err)
		}
	}
	s.limiter.reset("ip:" + ip)
	s.limiter.reset("user:" + username)
	s.log.Info("signed in", "user", username, "ip", ip)
	s.startSession(w, r, username)
}

func (s *Service) reject(w http.ResponseWriter, ip, username string) {
	now := time.Now()
	s.limiter.fail("ip:"+ip, now)
	if username != "" {
		s.limiter.fail("user:"+username, now)
	}
	s.log.Warn("sign in failed", "user", username, "ip", ip)
	writeError(w, "invalid username or password", http.StatusUnauthorized)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, "cross-site request rejected", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		if hash, ok := hashToken(c.Value); ok {
			if err := s.store.DeleteSession(r.Context(), hash); err != nil {
				s.internal(w, err)
				return
			}
		}
	}
	http.SetCookie(w, s.cookie(r, "", -1))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Service) changePassword(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.authenticate(r)
	if !ok {
		writeError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	var in struct{ Current, New string }
	if !s.readJSON(w, r, &in) {
		return
	}
	key := "user:" + sess.Username
	if s.throttled(w, key) {
		return
	}
	user, err := s.store.UserByName(r.Context(), sess.Username)
	if err != nil {
		s.internal(w, err)
		return
	}
	if len(in.Current) > MaxPasswordLen {
		in.Current = ""
	}
	ok, _, err = s.verify(user.PasswordHash, in.Current)
	if err != nil {
		s.internal(w, err)
		return
	}
	if !ok {
		s.limiter.fail(key, time.Now())
		s.log.Warn("password change rejected", "user", sess.Username, "ip", clientIP(r))
		writeError(w, "current password is wrong", http.StatusForbidden)
		return
	}
	if err := CheckPassword(user.Username, in.New); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.SetPassword(r.Context(), user.ID, s.hash(in.New)); err != nil {
		s.internal(w, err)
		return
	}
	// Other sessions are signed out; the current one stays valid.
	var keep []byte
	if c, err := r.Cookie(cookieName); err == nil {
		keep, _ = hashToken(c.Value)
	}
	if err := s.store.DeleteUserSessions(r.Context(), user.ID, keep); err != nil {
		s.internal(w, err)
		return
	}
	s.limiter.reset(key)
	s.log.Info("password changed", "user", user.Username, "ip", clientIP(r))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Service) startSession(w http.ResponseWriter, r *http.Request, username string) {
	user, err := s.store.UserByName(r.Context(), username)
	if err != nil {
		s.internal(w, err)
		return
	}
	raw := make([]byte, tokenLen)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash, _ := hashToken(token)
	agent := r.UserAgent()
	if len(agent) > 200 {
		agent = agent[:200]
	}
	if err := s.store.CreateSession(r.Context(), hash, user.ID, time.Now().Add(sessionTTL), clientIP(r), agent); err != nil {
		s.internal(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, token, int(sessionTTL.Seconds())))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"username": user.Username})
}

func (s *Service) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteStrictMode,
	}
}

// hashToken maps the cookie value to the stored lookup key.
func hashToken(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != tokenLen {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

func (s *Service) hash(password string) string {
	s.hashSem <- struct{}{}
	defer func() { <-s.hashSem }()
	return HashPassword(password)
}

func (s *Service) verify(hash, password string) (ok, stale bool, err error) {
	s.hashSem <- struct{}{}
	defer func() { <-s.hashSem }()
	return VerifyPassword(hash, password)
}

func (s *Service) throttled(w http.ResponseWriter, key string) bool {
	wait := s.limiter.retryAfter(key, time.Now())
	if wait <= 0 {
		return false
	}
	secs := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, "too many attempts, try again in "+wait.Round(time.Second).String(), http.StatusTooManyRequests)
	return true
}

// readJSON decodes a small JSON body after the same-origin checks that
// stand in for a CSRF token: the strict cookie, the fetch metadata and the
// JSON content type (which browsers never send cross-site without CORS).
func (s *Service) readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !sameOrigin(r) {
		writeError(w, "cross-site request rejected", http.StatusForbidden)
		return false
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(ct) != "application/json" {
		writeError(w, "expected application/json", http.StatusUnsupportedMediaType)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(v); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}

// clientIP is the peer address; forwarded headers are not trusted since
// anyone could set them.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Service) internal(w http.ResponseWriter, err error) {
	s.log.Error("auth", "err", err)
	writeError(w, "internal error", http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
