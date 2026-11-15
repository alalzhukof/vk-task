package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type actor struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

const visible = `(t.owner_id=$1 OR $2='admin' OR ($2='operator' AND t.assigned_operator_id=$1))`
const ticketSelect = `SELECT t.id::text,t.owner_id::text,u.username AS owner_username,t.title,t.description,t.status,t.created_at,t.updated_at,t.assigned_operator_id::text,o.username AS operator_username,(SELECT count(*) FROM comments c WHERE c.ticket_id=t.id) AS comment_count FROM tickets t JOIN users u ON u.id=t.owner_id LEFT JOIN users o ON o.id=t.assigned_operator_id`

type ticketAccess struct {
	Owner    string
	Operator *string
}

func lockTicket(r *http.Request, tx pgx.Tx, u actor) (ticketAccess, error) {
	var row ticketAccess
	id := r.PathValue("id")
	if !uuid.MatchString(id) {
		return row, bad(422, "Некорректный идентификатор")
	}
	err := tx.QueryRow(r.Context(), `SELECT t.owner_id::text,t.assigned_operator_id::text FROM tickets t WHERE t.id=$3 AND `+visible+` FOR UPDATE`, u.ID, u.Role, id).Scan(&row.Owner, &row.Operator)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, bad(404, "Заявка не найдена")
	}
	return row, err
}
func (a *API) detail(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	if !uuid.MatchString(r.PathValue("id")) {
		return bad(422, "Некорректный идентификатор")
	}
	rows, e := c.Query(r.Context(), ticketSelect+` WHERE t.id=$3 AND `+visible, u.ID, u.Role, r.PathValue("id"))
	if e != nil {
		return e
	}
	out, e := collect(rows)
	if e != nil {
		return e
	}
	if len(out) == 0 {
		return bad(404, "Заявка не найдена")
	}
	send(w, 200, out[0])
	return nil
}
func (a *API) assign(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	if u.Role != "admin" {
		return bad(403, "Назначение доступно администратору")
	}
	var v struct {
		Operator *string `json:"operator_id"`
	}
	if e = decode(w, r, &v); e != nil {
		return e
	}
	if v.Operator != nil && !uuid.MatchString(*v.Operator) {
		return bad(422, "Некорректный оператор")
	}
	tx, e := c.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	// Serializes assignment against operator demotion without locking ticket owners.
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(75684322)`); e != nil {
		return e
	}
	var currentRole string
	if e = tx.QueryRow(r.Context(), `SELECT role FROM users WHERE id=$1`, u.ID).Scan(&currentRole); e != nil {
		return e
	}
	if currentRole != "admin" {
		return bad(403, "Права администратора отозваны")
	}
	row, e := lockTicket(r, tx, u)
	if e != nil {
		return e
	}
	if v.Operator != nil {
		var role string
		e = tx.QueryRow(r.Context(), `SELECT role FROM users WHERE id=$1`, *v.Operator).Scan(&role)
		if errors.Is(e, pgx.ErrNoRows) || e == nil && role != "operator" {
			return bad(422, "Выберите пользователя с ролью оператора")
		}
		if e != nil {
			return e
		}
	}
	same := row.Operator == nil && v.Operator == nil || row.Operator != nil && v.Operator != nil && strings.EqualFold(*row.Operator, *v.Operator)
	if !same {
		if _, e = tx.Exec(r.Context(), `UPDATE tickets SET assigned_operator_id=$1,updated_at=now() WHERE id=$2`, v.Operator, r.PathValue("id")); e != nil {
			return e
		}
		if e = changed(r.Context(), tx, row.Owner, r.PathValue("id"), "assigned"); e != nil {
			return e
		}
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 204, nil)
	return nil
}
func (a *API) status(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	var v struct {
		Status string `json:"status"`
	}
	if e = decode(w, r, &v); e != nil {
		return e
	}
	if v.Status != "open" && v.Status != "in_progress" && v.Status != "done" {
		return bad(422, "Неизвестный статус")
	}
	tx, e := c.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	row, e := lockTicket(r, tx, u)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), `UPDATE tickets SET status=$1,updated_at=now() WHERE id=$2`, v.Status, r.PathValue("id")); e != nil {
		return e
	}
	if e = changed(r.Context(), tx, row.Owner, r.PathValue("id"), "updated"); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 204, nil)
	return nil
}
func (a *API) comments(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	if !uuid.MatchString(r.PathValue("id")) {
		return bad(422, "Некорректный идентификатор")
	}
	// One snapshot includes both the permission decision and comments, including empty lists.
	rows, e := c.Query(r.Context(), `SELECT c.id::text,c.author_id::text,u.username AS author_username,c.body,c.created_at FROM tickets t LEFT JOIN comments c ON c.ticket_id=t.id LEFT JOIN users u ON u.id=c.author_id WHERE t.id=$3 AND `+visible+` ORDER BY c.created_at,c.id`, u.ID, u.Role, r.PathValue("id"))
	if e != nil {
		return e
	}
	out, e := collect(rows)
	if e != nil {
		return e
	}
	if len(out) == 0 {
		return bad(404, "Заявка не найдена")
	}
	if out[0]["id"] == nil {
		out = []map[string]any{}
	}
	send(w, 200, out)
	return nil
}
func (a *API) comment(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	var v struct {
		Body string `json:"body"`
	}
	if e = decode(w, r, &v); e != nil {
		return e
	}
	v.Body = strings.TrimSpace(v.Body)
	if v.Body == "" || utf8.RuneCountInString(v.Body) > 2000 {
		return bad(422, "Комментарий должен содержать от 1 до 2000 символов")
	}
	tx, e := c.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	row, e := lockTicket(r, tx, u)
	if e != nil {
		return e
	}
	var id string
	if e = tx.QueryRow(r.Context(), `INSERT INTO comments(ticket_id,author_id,body) VALUES($1,$2,$3) RETURNING id::text`, r.PathValue("id"), u.ID, v.Body).Scan(&id); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), `UPDATE tickets SET updated_at=now() WHERE id=$1`, r.PathValue("id")); e != nil {
		return e
	}
	if e = changed(r.Context(), tx, row.Owner, r.PathValue("id"), "commented"); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 201, map[string]string{"id": id})
	return nil
}
func (a *API) users(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	if u.Role != "admin" {
		return bad(403, "Управление пользователями доступно администратору")
	}
	q := strings.TrimSpace(r.URL.Query().Get("search"))
	if len(q) > 32 {
		return bad(422, "Слишком длинный поиск")
	}
	role := r.URL.Query().Get("role")
	if role != "" && role != "operator" {
		return bad(422, "Неизвестный фильтр")
	}
	rows, e := c.Query(r.Context(), `SELECT id::text,username,role FROM users WHERE ($1='' OR position(lower($1) in username)>0) AND ($2='' OR role=$2) ORDER BY username LIMIT 100`, q, role)
	if e != nil {
		return e
	}
	out, e := collect(rows)
	if e != nil {
		return e
	}
	send(w, 200, out)
	return nil
}
func (a *API) role(w http.ResponseWriter, r *http.Request) error {
	c, u, e := a.auth(r)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	if u.Role != "admin" {
		return bad(403, "Изменение роли доступно администратору")
	}
	id := r.PathValue("id")
	if !uuid.MatchString(id) {
		return bad(422, "Некорректный идентификатор")
	}
	var v struct {
		Role string `json:"role"`
	}
	if e = decode(w, r, &v); e != nil {
		return e
	}
	if v.Role != "user" && v.Role != "operator" && v.Role != "admin" {
		return bad(422, "Неизвестная роль")
	}
	if strings.EqualFold(id, u.ID) && v.Role != "admin" {
		return bad(409, "Нельзя изменить собственную роль администратора")
	}
	tx, e := c.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(75684322)`); e != nil {
		return e
	}
	// Recheck after the lock: two administrators cannot concurrently demote each other.
	var current string
	if e = tx.QueryRow(r.Context(), `SELECT role FROM users WHERE id=$1`, u.ID).Scan(&current); e != nil {
		return e
	}
	if current != "admin" {
		return bad(403, "Права администратора отозваны")
	}
	var old string
	e = tx.QueryRow(r.Context(), `SELECT role FROM users WHERE id=$1`, id).Scan(&old)
	if errors.Is(e, pgx.ErrNoRows) {
		return bad(404, "Пользователь не найден")
	}
	if e != nil {
		return e
	}
	if old != v.Role {
		var assigned bool
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tickets WHERE assigned_operator_id=$1)`, id).Scan(&assigned); e != nil {
			return e
		}
		if assigned {
			return bad(409, "Сначала переназначьте или снимите назначенные заявки")
		}
		if _, e = tx.Exec(r.Context(), `UPDATE users SET role=$1,revision=revision+1 WHERE id=$2`, v.Role, id); e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO role_changes(actor_id,user_id,old_role,new_role) VALUES($1,$2,$3,$4)`, u.ID, id, old, v.Role); e != nil {
			return e
		}
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 204, nil)
	return nil
}
