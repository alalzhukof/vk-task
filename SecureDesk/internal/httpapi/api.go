// Package httpapi implements the HTTP contract without a web framework.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
	"securedesk/internal/platform"
)

type API struct {
	Cache *redis.Client
	dummy string
}
type failure struct {
	code    int
	message string
}

func (f failure) Error() string      { return f.message }
func bad(code int, msg string) error { return failure{code, msg} }

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type ticket struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status,omitempty"`
}

var username = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)
var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func New(cache *redis.Client) *API {
	dummy, e := platform.HashPassword("dummy-password-for-timing")
	if e != nil {
		panic(e)
	}
	return &API{cache, dummy}
}
func send(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if code != 204 {
		_ = json.NewEncoder(w).Encode(data)
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return bad(422, "Некорректный JSON или поля запроса")
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return bad(422, "Ожидается один JSON объект")
	}
	return nil
}
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hello", a.wrap(func(w http.ResponseWriter, r *http.Request) error {
		send(w, 200, map[string]string{"message": "Hello from SecureDesk Go backend"})
		return nil
	}))
	mux.HandleFunc("GET /api/health", a.wrap(func(w http.ResponseWriter, r *http.Request) error {
		c, e := platform.DB(r.Context())
		if e != nil {
			return e
		}
		defer c.Close(context.Background())
		if e = c.Ping(r.Context()); e != nil {
			return e
		}
		send(w, 200, map[string]string{"status": "ok"})
		return nil
	}))
	mux.HandleFunc("GET /api/openapi.json", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "/srv/openapi.json") })
	mux.HandleFunc("POST /api/register", a.wrap(a.register))
	mux.HandleFunc("POST /api/login", a.wrap(a.login))
	mux.HandleFunc("POST /api/logout", a.wrap(a.logout))
	mux.HandleFunc("GET /api/me", a.wrap(a.me))
	mux.HandleFunc("GET /api/tickets", a.wrap(a.list))
	mux.HandleFunc("POST /api/tickets", a.wrap(a.create))
	mux.HandleFunc("PUT /api/tickets/{id}", a.wrap(a.update))
	mux.HandleFunc("DELETE /api/tickets/{id}", a.wrap(a.delete))
	mux.HandleFunc("GET /api/events", a.wrap(a.events))
	mux.HandleFunc("GET /api/tickets/{id}", a.wrap(a.detail))
	mux.HandleFunc("PUT /api/tickets/{id}/assignment", a.wrap(a.assign))
	mux.HandleFunc("PATCH /api/tickets/{id}/status", a.wrap(a.status))
	mux.HandleFunc("GET /api/tickets/{id}/comments", a.wrap(a.comments))
	mux.HandleFunc("POST /api/tickets/{id}/comments", a.wrap(a.comment))
	mux.HandleFunc("GET /api/admin/users", a.wrap(a.users))
	mux.HandleFunc("PUT /api/admin/users/{id}/role", a.wrap(a.role))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", platform.Token(16))
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" || r.Method == "DELETE" {
			origin := r.Header.Get("Origin")
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" || (origin != "" && origin != platform.Env("APP_ORIGIN", "http://localhost:8080")) {
				send(w, 403, map[string]string{"detail": "Недопустимый источник запроса"})
				return
			}
			media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if r.Method != "DELETE" && media != "application/json" {
				send(w, 415, map[string]string{"detail": "Ожидается application/json"})
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (a *API) wrap(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if e := fn(w, r); e != nil {
			var f failure
			if errors.As(e, &f) {
				send(w, f.code, map[string]string{"detail": f.message})
				return
			}
			slog.Warn("dependency_unavailable", "request_id", w.Header().Get("X-Request-ID"))
			send(w, 503, map[string]string{"detail": "Сервис временно недоступен. Повторите запрос."})
		}
	}
}
func (a *API) auth(r *http.Request) (*pgx.Conn, actor, error) {
	cookie, e := r.Cookie("session")
	if e != nil {
		return nil, actor{}, bad(401, "Войдите в систему")
	}
	c, e := platform.DB(r.Context())
	if e != nil {
		return nil, actor{}, e
	}
	var u actor
	e = c.QueryRow(r.Context(), `SELECT u.id::text,u.username,u.role FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, platform.TokenHash(cookie.Value)).Scan(&u.ID, &u.Username, &u.Role)
	if e != nil {
		c.Close(context.Background())
		if errors.Is(e, pgx.ErrNoRows) {
			e = bad(401, "Сессия истекла")
		}
		return nil, actor{}, e
	}
	return c, u, nil
}
func (a *API) creds(w http.ResponseWriter, r *http.Request) (credentials, error) {
	var v credentials
	if e := decode(w, r, &v); e != nil {
		return v, e
	}
	if !username.MatchString(v.Username) || utf8.RuneCountInString(v.Password) < 12 || utf8.RuneCountInString(v.Password) > 128 {
		return v, bad(422, "Проверьте имя и длину пароля")
	}
	v.Username = strings.ToLower(v.Username)
	return v, nil
}
func (a *API) limit(r *http.Request) error {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	n, e := a.Cache.Eval(r.Context(), `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`, []string{"login:" + host}).Int()
	if e != nil {
		return e
	}
	if n > 20 {
		return bad(429, "Слишком много попыток, подождите минуту")
	}
	return nil
}
func (a *API) register(w http.ResponseWriter, r *http.Request) error {
	v, e := a.creds(w, r)
	if e != nil {
		return e
	}
	if e = a.limit(r); e != nil {
		return e
	}
	hash, e := platform.HashPassword(v.Password)
	if e != nil {
		return e
	}
	c, e := platform.DB(r.Context())
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	_, e = c.Exec(r.Context(), `INSERT INTO users(username,password_hash) VALUES ($1,$2)`, v.Username, hash)
	var pgerr *pgconn.PgError
	if errors.As(e, &pgerr) && pgerr.Code == "23505" {
		return bad(409, "Имя уже занято")
	}
	if e != nil {
		return e
	}
	send(w, 201, map[string]string{"message": "Учетная запись создана"})
	return nil
}
func (a *API) login(w http.ResponseWriter, r *http.Request) error {
	v, e := a.creds(w, r)
	if e != nil {
		return e
	}
	if e = a.limit(r); e != nil {
		return e
	}
	c, e := platform.DB(r.Context())
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	var id, hash string
	e = c.QueryRow(r.Context(), `SELECT id::text,password_hash FROM users WHERE username=$1`, v.Username).Scan(&id, &hash)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if hash == "" {
		hash = a.dummy
	}
	valid := platform.PasswordMatches(v.Password, hash)
	if !valid || id == "" {
		return bad(401, "Неверные учетные данные")
	}
	token := platform.Token(32)
	tx, e := c.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(r.Context(), `DELETE FROM sessions WHERE expires_at<now()`); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO sessions(token_hash,user_id) VALUES ($1,$2)`, platform.TokenHash(token), id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/api", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 3600, Secure: os.Getenv("COOKIE_SECURE") == "true"})
	send(w, 200, map[string]string{"username": v.Username})
	return nil
}
func (a *API) logout(w http.ResponseWriter, r *http.Request) error {
	c, e := platform.DB(r.Context())
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	cookie, _ := r.Cookie("session")
	if cookie != nil {
		if _, e = c.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, platform.TokenHash(cookie.Value)); e != nil {
			return e
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Path: "/api", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	send(w, 204, nil)
	return nil
}
func (a *API) me(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	send(w, 200, u)
	return nil
}
func collect(rows pgx.Rows) ([]map[string]any, error) {
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		vals, e := rows.Values()
		if e != nil {
			return nil, e
		}
		m := map[string]any{}
		for i, f := range rows.FieldDescriptions() {
			m[f.Name] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	tx, e := c.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	var rev string
	if e = tx.QueryRow(r.Context(), `SELECT revision::text FROM users WHERE id=$1`, u.ID).Scan(&rev); e != nil {
		return e
	}
	key := "tickets:v2:" + u.ID + ":" + rev
	if value, err := a.Cache.Get(r.Context(), key).Result(); u.Role == "user" && err == nil && json.Valid([]byte(value)) {
		w.Header().Set("X-Cache", "HIT")
		send(w, 200, json.RawMessage(value))
		return nil
	}
	rows, e := tx.Query(r.Context(), ticketSelect+` WHERE `+visible+` ORDER BY t.created_at DESC,t.id DESC LIMIT 100`, u.ID, u.Role)
	if e != nil {
		return e
	}
	result, e := collect(rows)
	if e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	encoded, e := json.Marshal(result)
	if e != nil {
		return e
	}
	if u.Role == "user" {
		_ = a.Cache.Set(r.Context(), key, encoded, time.Minute).Err()
		w.Header().Set("X-Cache", "MISS")
	} else {
		w.Header().Set("X-Cache", "BYPASS")
	}
	send(w, 200, result)
	return nil
}
func readTicket(w http.ResponseWriter, r *http.Request, update bool) (ticket, error) {
	var v ticket
	// Separate create schema forbids status even if its value is empty.
	if !update {
		var x struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if e := decode(w, r, &x); e != nil {
			return v, e
		}
		v.Title = x.Title
		v.Description = x.Description
	} else {
		if e := decode(w, r, &v); e != nil {
			return v, e
		}
	}
	v.Title = strings.TrimSpace(v.Title)
	v.Description = strings.TrimSpace(v.Description)
	if v.Title == "" || utf8.RuneCountInString(v.Title) > 160 || utf8.RuneCountInString(v.Description) > 2000 {
		return v, bad(422, "Некорректная длина заявки")
	}
	if update && v.Status != "open" && v.Status != "in_progress" && v.Status != "done" {
		return v, bad(422, "Неизвестный статус")
	}
	return v, nil
}
func changed(ctx context.Context, tx pgx.Tx, owner, id, action string) error {
	if _, e := tx.Exec(ctx, `UPDATE users SET revision=revision+1 WHERE id=$1`, owner); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `INSERT INTO outbox(id,owner_id,ticket_id,action) VALUES (gen_random_uuid(),$1,$2,$3)`, owner, id, action)
	return e
}
func (a *API) create(w http.ResponseWriter, r *http.Request) error { return a.mutate(w, r, "created") }
func (a *API) update(w http.ResponseWriter, r *http.Request) error { return a.mutate(w, r, "updated") }
func (a *API) delete(w http.ResponseWriter, r *http.Request) error { return a.mutate(w, r, "deleted") }
func (a *API) mutate(w http.ResponseWriter, r *http.Request, action string) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	id := r.PathValue("id")
	if action != "created" && !uuid.MatchString(id) {
		return bad(422, "Некорректный идентификатор")
	}
	var v ticket
	if action != "deleted" {
		v, e = readTicket(w, r, action == "updated")
		if e != nil {
			return e
		}
	}
	tx, e := c.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	owner := u.ID
	if action != "created" {
		var row ticketAccess
		row, e = lockTicket(r, tx, u)
		if e != nil {
			return e
		}
		if row.Owner != u.ID && u.Role != "admin" {
			return bad(403, "Редактировать и удалять заявку может владелец или администратор")
		}
		owner = row.Owner
	}
	switch action {
	case "created":
		e = tx.QueryRow(r.Context(), `INSERT INTO tickets(owner_id,title,description) VALUES ($1,$2,$3) RETURNING id::text`, owner, v.Title, v.Description).Scan(&id)
	case "updated":
		e = tx.QueryRow(r.Context(), `UPDATE tickets SET title=$1,description=$2,status=$3,updated_at=now() WHERE id=$4 AND owner_id=$5 RETURNING id::text`, v.Title, v.Description, v.Status, id, owner).Scan(&id)
	case "deleted":
		e = tx.QueryRow(r.Context(), `DELETE FROM tickets WHERE id=$1 AND owner_id=$2 RETURNING id::text`, id, owner).Scan(&id)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return bad(404, "Заявка не найдена")
	}
	if e != nil {
		return e
	}
	if e = changed(r.Context(), tx, owner, id, action); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	if action == "deleted" {
		send(w, 204, nil)
	} else {
		code := 200
		if action == "created" {
			code = 201
		}
		send(w, code, map[string]string{"id": id})
	}
	return nil
}
func (a *API) events(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	rows, e := c.Query(r.Context(), `SELECT event_id::text,ticket_id::text,action,processed_at FROM audit a WHERE a.owner_id=$1 OR $2='admin' OR ($2='operator' AND EXISTS(SELECT 1 FROM tickets t WHERE t.id=a.ticket_id AND t.assigned_operator_id=$1)) ORDER BY processed_at DESC LIMIT 30`, u.ID, u.Role)
	if e != nil {
		return e
	}
	result, e := collect(rows)
	if e != nil {
		return e
	}
	send(w, 200, result)
	return nil
}
