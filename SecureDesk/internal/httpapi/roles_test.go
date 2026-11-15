package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"securedesk/internal/platform"
	"strings"
	"testing"
	"time"
)

func (f *fixture) identity() actor {
	b, _ := f.call("GET", "/api/me", nil, 200)
	var u actor
	if e := json.Unmarshal(b, &u); e != nil {
		f.t.Fatal(e)
	}
	return u
}
func roleFixture(t *testing.T, role string) *fixture {
	f := setup(t)
	f.account()
	if role != "user" {
		f.sql(`UPDATE users SET role=$1 WHERE username=$2`, role, f.creds["username"])
	}
	return f
}
func hasTicket(t *testing.T, f *fixture, id string, want bool) {
	t.Helper()
	b, _ := f.call("GET", "/api/tickets", nil, 200)
	if bytes.Contains(b, []byte(id)) != want {
		t.Fatalf("visibility mismatch for %s", f.identity().Role)
	}
}
func TestRolesAndLivePermissionChanges(t *testing.T) {
	admin := roleFixture(t, "admin")
	u := roleFixture(t, "user")
	id := u.identity().ID
	u.call("GET", "/api/admin/users", nil, 403)
	u.call("PUT", "/api/admin/users/"+id+"/role", map[string]string{"role": "admin"}, 403)
	anonymous := setup(t)
	anonymous.call("POST", "/api/register", map[string]string{"username": "evil", "password": "Long_password_2026", "role": "admin"}, 422)
	admin.call("PUT", "/api/admin/users/"+id+"/role", map[string]string{"role": "superuser"}, 422)
	admin.call("PUT", "/api/admin/users/"+id+"/role", map[string]string{"role": "operator"}, 204)
	if u.identity().Role != "operator" {
		t.Fatal("session did not see new role")
	}
	admin.call("PUT", "/api/admin/users/"+id+"/role", map[string]string{"role": "admin"}, 204)
	u.call("GET", "/api/admin/users", nil, 200)
	admin.call("PUT", "/api/admin/users/"+id+"/role", map[string]string{"role": "user"}, 204)
	u.call("GET", "/api/admin/users", nil, 403)
	admin.call("PUT", "/api/admin/users/"+admin.identity().ID+"/role", map[string]string{"role": "user"}, 409)
	b, _ := admin.call("GET", "/api/admin/users?search="+u.creds["username"], nil, 200)
	if !bytes.Contains(b, []byte(id)) || bytes.Contains(b, []byte("password")) {
		t.Fatal("unsafe user search")
	}
	c, e := platform.DB(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	var n int
	if e = c.QueryRow(context.Background(), `SELECT count(*) FROM role_changes WHERE user_id=$1`, id).Scan(&n); e != nil || n != 3 {
		t.Fatal("role audit", n, e)
	}
}
func TestAssignmentsIsolationAndCacheRevocation(t *testing.T) {
	admin := roleFixture(t, "admin")
	u := roleFixture(t, "user")
	op := roleFixture(t, "operator")
	other := roleFixture(t, "operator")
	id := u.create("Assignment")
	path := "/api/tickets/" + id
	hasTicket(t, u, id, true)
	u.call("GET", "/api/tickets", nil, 200)
	hasTicket(t, op, id, false)
	hasTicket(t, admin, id, true)
	u.call("PUT", path+"/assignment", map[string]any{"operator_id": op.identity().ID}, 403)
	admin.call("PUT", path+"/assignment", map[string]any{"operator_id": u.identity().ID}, 422)
	admin.call("PUT", path+"/assignment", map[string]any{"operator_id": "bad"}, 422)
	admin.call("PUT", path+"/assignment", map[string]any{"operator_id": "00000000-0000-0000-0000-000000000000"}, 422)
	admin.call("PUT", path+"/assignment", map[string]any{"operator_id": op.identity().ID}, 204)
	b, h := u.call("GET", "/api/tickets", nil, 200)
	if h.Get("X-Cache") != "MISS" || !bytes.Contains(b, []byte(op.creds["username"])) {
		t.Fatal("assignment cache stale")
	}
	hasTicket(t, op, id, true)
	hasTicket(t, other, id, false)
	op.call("PATCH", path+"/status", map[string]string{"status": "in_progress"}, 204)
	op.call("PUT", path, map[string]string{"title": "overwrite", "status": "done"}, 403)
	op.call("DELETE", path, nil, 403)
	other.call("GET", path, nil, 404)
	other.call("PATCH", path+"/status", map[string]string{"status": "done"}, 404)
	admin.call("PUT", "/api/admin/users/"+op.identity().ID+"/role", map[string]string{"role": "user"}, 409)
	admin.call("PUT", path+"/assignment", map[string]any{"operator_id": other.identity().ID}, 204)
	hasTicket(t, op, id, false)
	op.call("GET", path+"/comments", nil, 404)
	op.call("POST", path+"/comments", map[string]string{"body": "late"}, 404)
	hasTicket(t, other, id, true)
	admin.call("PUT", path+"/assignment", map[string]any{"operator_id": nil}, 204)
	hasTicket(t, other, id, false)
	admin.call("PUT", "/api/admin/users/"+op.identity().ID+"/role", map[string]string{"role": "user"}, 204)
	admin.call("PUT", path, map[string]string{"title": "admin edit", "status": "done"}, 200)
	admin.call("DELETE", path, nil, 204)
}
func TestCommentsValidationOwnershipAndDelivery(t *testing.T) {
	admin := roleFixture(t, "admin")
	u := roleFixture(t, "user")
	op := roleFixture(t, "operator")
	stranger := roleFixture(t, "user")
	id := u.create("Discussion")
	path := "/api/tickets/" + id
	comments := path + "/comments"
	b, _ := u.call("GET", comments, nil, 200)
	if strings.TrimSpace(string(b)) != "[]" {
		t.Fatal(string(b))
	}
	stranger.call("GET", comments, nil, 404)
	stranger.call("POST", comments, map[string]string{"body": "hidden"}, 404)
	admin.call("PUT", path+"/assignment", map[string]string{"operator_id": op.identity().ID}, 204)
	for _, v := range []map[string]string{{"body": " "}, {"body": strings.Repeat("я", 2001)}, {"body": "hi", "author_id": admin.identity().ID}} {
		u.call("POST", comments, v, 422)
	}
	u.call("GET", "/api/tickets", nil, 200)
	text := `<img src=x onerror=alert(1)> '; DROP TABLE comments; --`
	u.call("POST", comments, map[string]string{"body": text}, 201)
	op.call("POST", comments, map[string]string{"body": "Оператор отвечает"}, 201)
	admin.call("POST", comments, map[string]string{"body": "Администратор проверил"}, 201)
	b, _ = op.call("GET", comments, nil, 200)
	var rows []map[string]any
	if e := json.Unmarshal(b, &rows); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 3 || rows[0]["body"] != text || rows[0]["author_id"] != u.identity().ID {
		t.Fatal("comments or attribution incorrect", string(b))
	}
	b, h := u.call("GET", "/api/tickets", nil, 200)
	if h.Get("X-Cache") != "MISS" || !bytes.Contains(b, []byte(`"comment_count":3`)) {
		t.Fatal("comment cache stale", string(b))
	}
	deadline := time.Now().Add(30 * time.Second)
	delivered := false
	for time.Now().Before(deadline) {
		b, _ = u.call("GET", "/api/events", nil, 200)
		if bytes.Contains(b, []byte(`"commented"`)) && bytes.Contains(b, []byte(`"assigned"`)) {
			delivered = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !delivered {
		t.Fatal("new outbox actions not delivered")
	}
	u.call("DELETE", path, nil, 204)
	admin.call("GET", comments, nil, 404)
	c, e := platform.DB(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	var n int
	if e = c.QueryRow(context.Background(), `SELECT count(*) FROM comments WHERE ticket_id=$1`, id).Scan(&n); e != nil || n != 0 {
		t.Fatal("comments not cascaded", e)
	}
}
func TestNewRoutesRequireAuthentication(t *testing.T) {
	f := setup(t)
	id := "00000000-0000-0000-0000-000000000000"
	for _, p := range []string{"/api/admin/users", "/api/tickets/" + id, "/api/tickets/" + id + "/comments"} {
		f.call("GET", p, nil, 401)
	}
	f.call("PUT", "/api/tickets/"+id+"/assignment", map[string]any{"operator_id": nil}, 401)
	f.call("PATCH", "/api/tickets/"+id+"/status", map[string]string{"status": "done"}, 401)
	f.call("POST", "/api/tickets/"+id+"/comments", map[string]string{"body": "hi"}, 401)
	f.call("PUT", "/api/admin/users/"+id+"/role", map[string]string{"role": "admin"}, 401)
}

func asyncRequest(f *fixture, method, path string, data any, out chan<- int) {
	b, _ := json.Marshal(data)
	r, _ := http.NewRequest(method, f.s.URL+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	res, e := f.c.Do(r)
	if e != nil {
		out <- 0
		return
	}
	res.Body.Close()
	out <- res.StatusCode
}
func TestConcurrentAdminDemotions(t *testing.T) {
	a := roleFixture(t, "admin")
	b := roleFixture(t, "admin")
	aid, bid := a.identity().ID, b.identity().ID
	out := make(chan int, 2)
	go asyncRequest(a, "PUT", "/api/admin/users/"+bid+"/role", map[string]string{"role": "user"}, out)
	go asyncRequest(b, "PUT", "/api/admin/users/"+aid+"/role", map[string]string{"role": "user"}, out)
	x, y := <-out, <-out
	if !(x == 204 && y == 403 || x == 403 && y == 204) {
		t.Fatalf("demotion race: %d %d", x, y)
	}
	if (a.identity().Role == "admin") == (b.identity().Role == "admin") {
		t.Fatal("must preserve exactly one of the two admins")
	}
}
func TestAssignmentAndDemotionAreAtomic(t *testing.T) {
	admin := roleFixture(t, "admin")
	u := roleFixture(t, "user")
	op := roleFixture(t, "operator")
	id := u.create("Race assignment")
	oid := op.identity().ID
	assigned, demoted := make(chan int, 1), make(chan int, 1)
	go asyncRequest(admin, "PUT", "/api/tickets/"+id+"/assignment", map[string]string{"operator_id": oid}, assigned)
	go asyncRequest(admin, "PUT", "/api/admin/users/"+oid+"/role", map[string]string{"role": "user"}, demoted)
	x, y := <-assigned, <-demoted
	if !(x == 204 && y == 409 || x == 422 && y == 204) {
		t.Fatalf("assignment race: %d %d", x, y)
	}
	b, _ := u.call("GET", "/api/tickets/"+id, nil, 200)
	var ticket map[string]any
	json.Unmarshal(b, &ticket)
	if ticket["assigned_operator_id"] != nil && op.identity().Role != "operator" {
		t.Fatal("assigned a non-operator")
	}
	admin.call("DELETE", "/api/tickets/"+id, nil, 204)
}
