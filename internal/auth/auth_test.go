package auth

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aganet/tikscope/internal/store"
)

func TestPasswordHash(t *testing.T) {
	hash := HashPassword("correct horse battery staple")
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	ok, stale, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok || stale {
		t.Fatalf("verify: ok=%v stale=%v err=%v", ok, stale, err)
	}
	if ok, _, _ := VerifyPassword(hash, "correct horse battery staple!"); ok {
		t.Fatal("wrong password accepted")
	}
	if HashPassword("x") == HashPassword("x") {
		t.Fatal("salt is not random")
	}
	weak := "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0c2Fs$" + strings.Split(hash, "$")[5]
	if _, stale, err := VerifyPassword(weak, "anything"); err != nil || !stale {
		t.Fatalf("weak parameters should be reported stale: stale=%v err=%v", stale, err)
	}
	if _, _, err := VerifyPassword("$2a$10$abc", "x"); err == nil {
		t.Fatal("foreign hash format accepted")
	}
}

func TestPasswordPolicy(t *testing.T) {
	cases := map[string]bool{
		"short":                     false,
		"aaaaaaaaaaaaaaaa":          false,
		"my-admin-passphrase-2026":  false, // contains the username
		"tikscope-is-the-best-tool": false,
		"correct horse battery":     true,
		"Xy9#kL2@pQ7!mN4$":          true,
	}
	for pw, want := range cases {
		if got := CheckPassword("admin", pw) == nil; got != want {
			t.Errorf("%q: accepted=%v want %v", pw, got, want)
		}
	}
	if _, err := NormalizeUsername("Admin"); err != nil {
		t.Error(err)
	}
	if _, err := NormalizeUsername("ad min"); err == nil {
		t.Error("space in username accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	for i := 0; i < 4; i++ {
		l.fail("k", now)
	}
	if l.retryAfter("k", now) != 0 {
		t.Fatal("locked before the fifth failure")
	}
	l.fail("k", now)
	if w := l.retryAfter("k", now); w <= 0 || w > 30*time.Second {
		t.Fatalf("first lock %v", w)
	}
	l.fail("k", now.Add(31*time.Second))
	if w := l.retryAfter("k", now.Add(31*time.Second)); w <= 30*time.Second {
		t.Fatalf("lock did not grow: %v", w)
	}
	l.reset("k")
	if l.retryAfter("k", now) != 0 {
		t.Fatal("reset did not clear the lock")
	}
}

func newTestServer(t *testing.T) (*httptest.Server, *Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st, "secret-token", slog.Default())
	mux := http.NewServeMux()
	svc.Routes(mux)
	mux.HandleFunc("GET /api/data", func(w http.ResponseWriter, r *http.Request) {
		sess, _ := SessionFrom(r.Context())
		w.Write([]byte(sess.Username))
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("page " + r.URL.Path)) })
	srv := httptest.NewServer(svc.Protect(mux))
	t.Cleanup(srv.Close)
	return srv, svc
}

func call(t *testing.T, srv *httptest.Server, method, path string, body any, cookie *http.Cookie, hdr map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func sessionCookie(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

func TestSetupLoginFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	pw := "correct horse battery staple"

	res, out := call(t, srv, "GET", "/api/auth/status", nil, nil, nil)
	if res.StatusCode != 200 || out["setup_required"] != true {
		t.Fatalf("status before setup: %d %v", res.StatusCode, out)
	}
	if res, _ := call(t, srv, "GET", "/", nil, nil, nil); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login" {
		t.Fatalf("dashboard without session: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := call(t, srv, "GET", "/api/data", nil, nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api without session: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "POST", "/api/auth/setup", map[string]string{"username": "Admin", "password": pw, "token": "wrong"}, nil, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong setup token: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "POST", "/api/auth/setup", map[string]string{"username": "Admin", "password": "short", "token": "secret-token"}, nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("weak password: %d", res.StatusCode)
	}
	res, out = call(t, srv, "POST", "/api/auth/setup", map[string]string{"username": "Admin", "password": pw, "token": "secret-token"}, nil, nil)
	if res.StatusCode != 200 || out["username"] != "admin" {
		t.Fatalf("setup: %d %v", res.StatusCode, out)
	}
	c := sessionCookie(res)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie: %+v", c)
	}
	if res, _ := call(t, srv, "POST", "/api/auth/setup", map[string]string{"username": "other", "password": pw, "token": "secret-token"}, nil, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("second setup allowed: %d", res.StatusCode)
	}

	res, _ = call(t, srv, "GET", "/api/data", nil, c, nil)
	if res.StatusCode != 200 {
		t.Fatalf("api with session: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "GET", "/login", nil, c, nil); res.StatusCode != http.StatusFound {
		t.Fatalf("login page with session should redirect: %d", res.StatusCode)
	}
	forged := &http.Cookie{Name: cookieName, Value: strings.Repeat("A", 43)}
	if res, _ := call(t, srv, "GET", "/api/data", nil, forged, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged token accepted: %d", res.StatusCode)
	}

	if res, _ := call(t, srv, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "nope"}, nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad password: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "POST", "/api/auth/login", map[string]string{"username": "nobody", "password": pw}, nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown user: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": pw}, nil, map[string]string{"Sec-Fetch-Site": "cross-site"}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site login accepted: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": pw}, nil, map[string]string{"Origin": "https://evil.example"}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin accepted: %d", res.StatusCode)
	}
	res, _ = call(t, srv, "POST", "/api/auth/login", map[string]string{"username": "ADMIN", "password": pw}, nil, nil)
	if res.StatusCode != 200 || sessionCookie(res) == nil {
		t.Fatalf("login: %d", res.StatusCode)
	}
	c2 := sessionCookie(res)

	res, _ = call(t, srv, "POST", "/api/auth/password", map[string]string{"current": pw, "new": "a brand new passphrase"}, c2, nil)
	if res.StatusCode != 200 {
		t.Fatalf("change password: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "GET", "/api/data", nil, c, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("other session survived password change: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "GET", "/api/data", nil, c2, nil); res.StatusCode != 200 {
		t.Fatalf("current session lost after password change: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "POST", "/api/auth/logout", nil, c2, map[string]string{"Content-Type": "application/json"}); res.StatusCode != 200 {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	if res, _ := call(t, srv, "GET", "/api/data", nil, c2, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session survived logout: %d", res.StatusCode)
	}
}

func TestLoginThrottle(t *testing.T) {
	srv, _ := newTestServer(t)
	for i := 0; i < 5; i++ {
		call(t, srv, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "wrong-password-x"}, nil, nil)
	}
	res, _ := call(t, srv, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "wrong-password-x"}, nil, nil)
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d", res.StatusCode)
	}
}
