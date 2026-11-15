package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/redis/go-redis/v9"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"securedesk/internal/platform"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	t     *testing.T
	s     *httptest.Server
	c     *http.Client
	api   *API
	creds map[string]string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("SECUREDESK_INTEGRATION") != "1" {
		t.Skip("requires Compose PostgreSQL/Redis/worker")
	}
	cache := platform.Redis()
	if e := cache.Del(context.Background(), "login:127.0.0.1").Err(); e != nil {
		t.Fatal(e)
	}
	a := New(cache)
	s := httptest.NewServer(a.Handler())
	jar, _ := cookiejar.New(nil)
	f := &fixture{t: t, s: s, c: &http.Client{Jar: jar, Timeout: 15 * time.Second}, api: a, creds: map[string]string{"username": "go_" + platform.Token(6), "password": "Strong_test_password_2026!"}}
	t.Cleanup(func() { s.Close(); cache.Close() })
	return f
}
func (f *fixture) call(method, path string, data any, status int) ([]byte, http.Header) {
	f.t.Helper()
	var b io.Reader
	if data != nil {
		encoded, e := json.Marshal(data)
		if e != nil {
			f.t.Fatal(e)
		}
		b = bytes.NewReader(encoded)
	}
	r, _ := http.NewRequest(method, f.s.URL+path, b)
	r.Header.Set("Content-Type", "application/json")
	res, e := f.c.Do(r)
	if e != nil {
		f.t.Fatal(e)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != status {
		f.t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.StatusCode, status, body)
	}
	return body, res.Header
}
func (f *fixture) account() {
	f.call("POST", "/api/register", f.creds, 201)
	f.call("POST", "/api/login", f.creds, 200)
}
func (f *fixture) create(title string) string {
	b, _ := f.call("POST", "/api/tickets", map[string]string{"title": title}, 201)
	var obj map[string]string
	_ = json.Unmarshal(b, &obj)
	return obj["id"]
}
func (f *fixture) sql(query string, args ...any) {
	f.t.Helper()
	c, e := platform.DB(context.Background())
	if e != nil {
		f.t.Fatal(e)
	}
	defer c.Close(context.Background())
	if _, e = c.Exec(context.Background(), query, args...); e != nil {
		f.t.Fatal(e)
	}
}
func TestHelloAndHealth(t *testing.T) {
	f := setup(t)
	b, _ := f.call("GET", "/api/hello", nil, 200)
	if !bytes.Contains(b, []byte("Go backend")) {
		t.Fatal(string(b))
	}
	f.call("GET", "/api/health", nil, 200)
}
func TestAuthorizationRequired(t *testing.T) {
	f := setup(t)
	for _, p := range []string{"/api/me", "/api/tickets", "/api/events"} {
		f.call("GET", p, nil, 401)
	}
}
func TestCookiesAndRevocation(t *testing.T) {
	f := setup(t)
	f.account()
	_, h := f.call("POST", "/api/login", f.creds, 200)
	cookie := h.Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Strict") {
		t.Fatal(cookie)
	}
	f.call("GET", "/api/me", nil, 200)
	f.call("POST", "/api/logout", map[string]string{}, 204)
	r, _ := http.NewRequest("GET", f.s.URL+"/api/me", nil)
	r.Header.Set("Cookie", strings.Split(cookie, ";")[0])
	res, e := f.c.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("revoked token still works")
	}
}
func TestDuplicateAndWrongPassword(t *testing.T) {
	f := setup(t)
	f.account()
	f.call("POST", "/api/register", f.creds, 409)
	f.creds["password"] = "Incorrect_password!"
	f.call("POST", "/api/login", f.creds, 401)
}
func TestCRUDCacheAndEvents(t *testing.T) {
	f := setup(t)
	f.account()
	id := f.create("First")
	_, h := f.call("GET", "/api/tickets", nil, 200)
	if h.Get("X-Cache") != "MISS" {
		t.Fatal(h)
	}
	_, h = f.call("GET", "/api/tickets", nil, 200)
	if h.Get("X-Cache") != "HIT" {
		t.Fatal(h)
	}
	f.call("PUT", "/api/tickets/"+id, map[string]string{"title": "Changed", "status": "done"}, 200)
	b, h := f.call("GET", "/api/tickets", nil, 200)
	if h.Get("X-Cache") != "MISS" || !bytes.Contains(b, []byte(`"done"`)) {
		t.Fatal(string(b), h)
	}
	f.call("DELETE", "/api/tickets/"+id, nil, 204)
	b, _ = f.call("GET", "/api/tickets", nil, 200)
	if strings.TrimSpace(string(b)) != "[]" {
		t.Fatal(string(b))
	}
	f.call("DELETE", "/api/tickets/"+id, nil, 404)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		b, _ = f.call("GET", "/api/events", nil, 200)
		var events []map[string]string
		_ = json.Unmarshal(b, &events)
		actions := map[string]bool{}
		for _, e := range events {
			if e["ticket_id"] == id {
				actions[e["action"]] = true
			}
		}
		if len(actions) == 3 {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("audit events not delivered")
}
func TestOwnerIsolation(t *testing.T) {
	f := setup(t)
	f.account()
	id := f.create("Private")
	g := setup(t)
	g.account()
	for _, p := range []string{"/api/tickets", "/api/events"} {
		b, _ := g.call("GET", p, nil, 200)
		if strings.TrimSpace(string(b)) != "[]" {
			t.Fatal(string(b))
		}
	}
	g.call("PUT", "/api/tickets/"+id, map[string]string{"title": "stolen", "status": "done"}, 404)
	g.call("DELETE", "/api/tickets/"+id, nil, 404)
}
func TestValidation(t *testing.T) {
	f := setup(t)
	f.account()
	cases := []map[string]string{{"title": ""}, {"title": " "}, {"title": strings.Repeat("я", 161)}, {"title": "x", "description": strings.Repeat("x", 2001)}, {"title": "x", "owner_id": "injected"}, {"title": "x", "status": "done"}}
	for i, p := range cases {
		t.Run(string(rune('A'+i)), func(t *testing.T) { f.call("POST", "/api/tickets", p, 422) })
	}
}
func TestInvalidIDAndStatus(t *testing.T) {
	f := setup(t)
	f.account()
	f.call("DELETE", "/api/tickets/not-a-uuid", nil, 422)
	id := f.create("x")
	f.call("PUT", "/api/tickets/"+id, map[string]string{"title": "x", "status": "admin"}, 422)
}
func TestSQLInjectionIsData(t *testing.T) {
	f := setup(t)
	f.account()
	title := `'; DROP TABLE tickets; -- <script>alert(1)</script>`
	f.create(title)
	b, _ := f.call("GET", "/api/tickets", nil, 200)
	var rows []map[string]any
	_ = json.Unmarshal(b, &rows)
	if rows[0]["title"] != title {
		t.Fatal(string(b))
	}
	f.create("Still works")
}
func TestCSRFAndMediaType(t *testing.T) {
	f := setup(t)
	f.account()
	for _, entry := range []struct {
		key, value string
		status     int
	}{{"Origin", "https://evil.example", 403}, {"Sec-Fetch-Site", "cross-site", 403}, {"Content-Type", "text/plain", 415}} {
		r, _ := http.NewRequest("POST", f.s.URL+"/api/tickets", strings.NewReader(`{"title":"bad"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(entry.key, entry.value)
		res, e := f.c.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != entry.status {
			t.Fatal(res.StatusCode, entry)
		}
	}
}
func TestRateLimit(t *testing.T) {
	f := setup(t)
	for i := 0; i < 20; i++ {
		f.call("POST", "/api/login", f.creds, 401)
	}
	f.call("POST", "/api/login", f.creds, 429)
}
func TestShortPassword(t *testing.T) {
	f := setup(t)
	f.creds["password"] = "short"
	f.call("POST", "/api/register", f.creds, 422)
}
func TestExpiredSession(t *testing.T) {
	f := setup(t)
	f.account()
	f.sql(`UPDATE sessions SET expires_at=now()-interval '1 second' WHERE user_id=(SELECT id FROM users WHERE username=$1)`, f.creds["username"])
	f.call("GET", "/api/me", nil, 401)
}
func TestDatabasePrivileges(t *testing.T) {
	_ = setup(t)
	for _, q := range []string{"CREATE TABLE forbidden(id int)", "DELETE FROM audit", "DELETE FROM outbox", "DELETE FROM comments", "UPDATE role_changes SET old_role='admin'", "DELETE FROM role_changes"} {
		c, e := platform.DB(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		_, e = c.Exec(context.Background(), q)
		c.Close(context.Background())
		if e == nil {
			t.Fatal("unexpected privilege", q)
		}
	}
}
func TestCacheOutageFallback(t *testing.T) {
	f := setup(t)
	f.account()
	f.create("Fallback")
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	defer dead.Close()
	f.api.Cache = dead
	f.call("GET", "/api/tickets", nil, 200)
	f.call("POST", "/api/login", f.creds, 503)
}
func TestStrictJSON(t *testing.T) {
	f := setup(t)
	f.account()
	for _, body := range []string{`{"title":"x"} {}`, `{"title":null}`, `[]`, strings.Repeat("x", 17000)} {
		r, _ := http.NewRequest("POST", f.s.URL+"/api/tickets", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		res, e := f.c.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 422 {
			t.Fatal(body[:2], res.StatusCode)
		}
	}
}
func TestConcurrentWrites(t *testing.T) {
	f := setup(t)
	f.account()
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := http.NewRequest("POST", f.s.URL+"/api/tickets", strings.NewReader(`{"title":"Concurrent"}`))
			r.Header.Set("Content-Type", "application/json")
			res, e := f.c.Do(r)
			if e != nil {
				errs <- e.Error()
				return
			}
			res.Body.Close()
			if res.StatusCode != 201 {
				errs <- res.Status
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	b, _ := f.call("GET", "/api/tickets", nil, 200)
	var rows []any
	_ = json.Unmarshal(b, &rows)
	if len(rows) != 8 {
		t.Fatal("lost writes", len(rows))
	}
}
