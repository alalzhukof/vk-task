// ops is a container-only operator CLI. Application containers never get the Docker socket.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"securedesk/internal/platform"
	"sort"
	"strings"
	"time"
)

type result struct {
	Check  string `json:"check"`
	Status string `json:"status"`
	Detail any    `json:"detail"`
}

var results []result
var prefix []string
var hostDir string
var project = platform.Env("PROJECT_NAME", "securedesk")

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func require(ok bool, message string) {
	if !ok {
		panic(errors.New(message))
	}
}
func record(check string, detail any) {
	results = append(results, result{check, "PASS", detail})
	fmt.Println("PASS:", check)
}
func command(env []string, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "docker", args...)
	c.Env = append(os.Environ(), env...)
	c.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("docker %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
func docker(args ...string) string {
	b, e := command(nil, nil, args...)
	must(e)
	return strings.TrimSpace(string(b))
}
func compose(args ...string) string { return docker(append(append([]string{}, prefix...), args...)...) }
func composeBytes(input []byte, args ...string) []byte {
	b, e := command(nil, input, append(append([]string{}, prefix...), args...)...)
	must(e)
	return b
}
func sql(service, query string, db ...string) string {
	name := "desk"
	if len(db) > 0 {
		name = db[0]
	}
	return compose("exec", "-T", service, "psql", "-U", "postgres", "-d", name, "-At", "-v", "ON_ERROR_STOP=1", "-c", query)
}
func attempt(fn func() bool) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return fn()
}
func wait(fn func() bool, seconds int) {
	end := time.Now().Add(time.Duration(seconds) * time.Second)
	for time.Now().Before(end) {
		if attempt(fn) {
			return
		}
		time.Sleep(time.Second)
	}
	panic(errors.New("condition timed out"))
}
func primary() string {
	for _, s := range []string{"pg-primary", "pg-standby"} {
		if attempt(func() bool { return sql(s, "SELECT pg_is_in_recovery()") == "f" }) {
			return s
		}
	}
	panic(errors.New("no writable PostgreSQL server"))
}
func initializeCompose() {
	id := docker("ps", "-q", "--filter", "label=com.docker.compose.project="+project, "--filter", "label=com.docker.compose.service=front")
	require(id != "", "start the application with docker compose up first")
	hostDir = docker("inspect", "--format", `{{index .Config.Labels "com.docker.compose.project.working_dir"}}`, strings.Fields(id)[0])
	require(filepath.IsAbs(hostDir), "missing Compose working directory")
	prefix = []string{"compose", "--project-directory", hostDir, "--env-file", "/dev/null", "-f", "/workspace/compose.yaml", "-p", project}
}

type client struct{ h *http.Client }

func newClient() *client {
	jar, e := cookiejar.New(nil)
	must(e)
	return &client{&http.Client{Jar: jar, Timeout: 18 * time.Second}}
}
func (c *client) request(method, path string, data any, want int) []byte {
	var body io.Reader
	if data != nil {
		b, e := json.Marshal(data)
		must(e)
		body = bytes.NewReader(b)
	}
	r, e := http.NewRequest(method, "http://front:8080/api"+path, body)
	must(e)
	r.Header.Set("Content-Type", "application/json")
	res, e := c.h.Do(r)
	must(e)
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	must(e)
	require(res.StatusCode == want, fmt.Sprintf("%s %s returned %d, expected %d", method, path, res.StatusCode, want))
	return b
}
func (c *client) login() *client {
	v := map[string]string{"username": "ops_" + platform.Token(6), "password": "Local_test_password_2026!"}
	c.request("POST", "/register", v, 201)
	c.request("POST", "/login", v, 200)
	return c
}
func (c *client) create(title string) string {
	b := c.request("POST", "/tickets", map[string]string{"title": title}, 201)
	var v map[string]string
	must(json.Unmarshal(b, &v))
	return v["id"]
}
func (c *client) contains(path, key, id string) bool {
	var rows []map[string]any
	must(json.Unmarshal(c.request("GET", path, nil, 200), &rows))
	for _, r := range rows {
		if r[key] == id {
			return true
		}
	}
	return false
}
func syncReplication() {
	p := primary()
	wait(func() bool { return strings.Contains(sql(p, "SELECT state FROM pg_stat_replication"), "streaming") }, 90)
	sql(p, "ALTER SYSTEM SET synchronous_standby_names = 'FIRST 1 (standby)'")
	sql(p, "SELECT pg_reload_conf()")
	wait(func() bool {
		return sql(p, "SELECT sync_state FROM pg_stat_replication WHERE application_name='standby'") == "sync"
	}, 30)
	record("Synchronous PostgreSQL replication", sql(p, "SELECT application_name,state,sync_state FROM pg_stat_replication"))
}
func firewall() {
	for _, probe := range []struct {
		s, addr string
		allow   bool
	}{{"api", "172.29.20.10:5432", true}, {"api", "172.29.30.10:6379", true}, {"api", "172.29.40.11:5672", false}, {"worker", "172.29.20.10:5432", true}, {"worker", "172.29.40.11:5672", true}, {"worker", "172.29.40.11:15672", false}, {"worker", "172.29.30.10:6379", false}, {"api", "1.1.1.1:443", false}} {
		got := compose("exec", "-T", probe.s, "/usr/local/bin/check", "tcp", probe.addr)
		require((got == "true") == probe.allow, "unexpected network access")
		record("Network "+probe.s+" -> "+probe.addr, map[bool]string{true: "ALLOW", false: "DENY"}[probe.allow])
	}
	rule := []string{"OUTPUT", "-p", "tcp", "--dport", "5432", "-j", "REJECT"}
	compose(append([]string{"exec", "-T", "api-net", "iptables", "-I"}, rule...)...)
	func() {
		defer compose(append([]string{"exec", "-T", "api-net", "iptables", "-D"}, rule...)...)
		newClient().request("GET", "/health", nil, 503)
		record("Deny legitimate database traffic", map[string]int{"http_status": 503})
	}()
	wait(func() bool { newClient().request("GET", "/health", nil, 200); return true }, 30)
	record("Restore legitimate database traffic", "HTTP 200")
	for _, s := range []string{"api-net", "worker-net"} {
		must(os.WriteFile("evidence/"+s+"-rules.txt", []byte(compose("exec", "-T", s, "iptables-save")), 0644))
	}
}
func delivery() {
	c := newClient().login()
	var id string
	compose("stop", "worker")
	func() {
		defer compose("start", "worker")
		id = c.create("Durable outbox while worker is stopped")
		require(sql(primary(), "SELECT count(*) FROM outbox WHERE ticket_id='"+id+"' AND published_at IS NULL") == "1", "missing pending event")
		record("Outbox survives stopped worker", "committed pending event")
	}()
	wait(func() bool { return c.contains("/events", "ticket_id", id) }, 60)
	compose("exec", "-T", "worker", "/usr/local/bin/check", "duplicate", id)
	record("Idempotent consumer", "one audit row after three duplicate publications")
}
func cacheOutage() {
	c := newClient().login()
	id := c.create("Redis outage fallback")
	compose("stop", "redis")
	func() {
		defer compose("start", "redis")
		require(c.contains("/tickets", "id", id), "ticket lost")
		newClient().request("POST", "/login", map[string]string{"username": "unknown", "password": "Some_password_2026!"}, 503)
		record("Redis outage", map[string]int{"read_fallback": 200, "login_fail_closed": 503})
	}()
	wait(func() bool {
		return compose("exec", "-T", "redis", "sh", "-c", `REDISCLI_AUTH="$(cat /run/securedesk/REDIS_PASSWORD)" redis-cli ping`) == "PONG"
	}, 30)
}
func queueRows(service string) []map[string]any {
	raw := compose("exec", "-T", "--user", "rabbitmq", service, "rabbitmqctl", "list_queues", "name", "leader", "members", "online", "--formatter=json")
	i := strings.Index(raw, "[")
	require(i >= 0, "missing queue result")
	var rows []map[string]any
	must(json.Unmarshal([]byte(raw[i:]), &rows))
	return rows
}
func rabbitFailover() {
	c := newClient().login()
	leader := ""
	for _, r := range queueRows("rabbit1") {
		if r["name"] == "desk.events" {
			leader = r["leader"].(string)
		}
	}
	parts := strings.Split(leader, "@")
	require(len(parts) == 2, "missing queue leader")
	s := parts[1]
	require(s == "rabbit1" || s == "rabbit2" || s == "rabbit3", "unexpected broker")
	start := time.Now()
	compose("kill", "-s", "SIGKILL", s)
	func() {
		defer compose("start", s)
		id := c.create("Broker leader failure")
		wait(func() bool { return c.contains("/events", "ticket_id", id) }, 90)
		record("RabbitMQ leader failure", map[string]any{"killed": leader, "event_delivered": true, "seconds": seconds(start)})
	}()
	wait(func() bool {
		compose("exec", "-T", "--user", "rabbitmq", s, "rabbitmq-diagnostics", "-q", "check_running")
		for _, r := range queueRows(s) {
			if r["name"] == "desk.events" {
				return len(r["online"].([]any)) == 3
			}
		}
		return false
	}, 90)
}
func seconds(start time.Time) float64 { return float64(time.Since(start).Milliseconds()/10) / 100 }
func pgFailover() {
	p := primary()
	standby := "pg-primary"
	if p == standby {
		standby = "pg-standby"
	}
	require(sql(standby, "SELECT pg_is_in_recovery()") == "t", "standby required")
	syncReplication()
	c := newClient().login()
	id := c.create("Survives primary crash")
	start := time.Now()
	compose("kill", "-s", "SIGKILL", p)
	sql(standby, "SELECT pg_promote(true,30)")
	sql(standby, "ALTER SYSTEM SET synchronous_standby_names = ''")
	sql(standby, "SELECT pg_reload_conf()")
	wait(func() bool { newClient().request("GET", "/health", nil, 200); return true }, 60)
	require(c.contains("/tickets", "id", id), "confirmed ticket lost")
	c.request("PUT", "/tickets/"+id, map[string]string{"title": "Writable promoted standby", "status": "done"}, 200)
	record("PostgreSQL primary SIGKILL and promotion", map[string]any{"killed": p, "promoted": standby, "ticket_preserved": true, "write_after_promotion": true, "seconds": seconds(start)})
}
func rejoin() {
	p := primary()
	target := "pg-primary"
	source := "172.29.20.11"
	if p == target {
		target = "pg-standby"
		source = "172.29.20.10"
	}
	for _, s := range strings.Fields(compose("ps", "--status", "running", "--services")) {
		require(s != target, "old primary must remain stopped until pg_rewind completes")
	}
	script := `set -eu
export PGPASSWORD="$(cat /run/securedesk/PG_PASSWORD)"
REPL_PASSWORD="$(cat /run/securedesk/REPL_PASSWORD)"
printf '\nwal_log_hints = on\n' >> "$PGDATA/postgresql.auto.conf"
gosu postgres postgres --single -D "$PGDATA" -c wal_log_hints=on desk < /dev/null
gosu postgres pg_rewind --target-pgdata="$PGDATA" --source-server="host=$SOURCE user=postgres dbname=desk" --progress
printf "\nprimary_conninfo = 'host=%s user=replicator password=%s application_name=standby'\nsynchronous_standby_names = ''\n" "$SOURCE" "$REPL_PASSWORD" >> "$PGDATA/postgresql.auto.conf"
touch "$PGDATA/standby.signal"
chown postgres:postgres "$PGDATA/standby.signal" "$PGDATA/postgresql.auto.conf"
`
	compose("run", "--rm", "--no-deps", "-T", "-e", "SOURCE="+source, "--entrypoint", "sh", target, "-c", script)
	compose("up", "-d", "--no-deps", target)
	wait(func() bool { return sql(target, "SELECT pg_is_in_recovery()") == "t" }, 90)
	syncReplication()
	record("Rejoin old primary safely", map[string]string{"primary": p, "standby": target, "method": "pg_rewind"})
}
func backup() {
	must(os.MkdirAll("backups", 0700))
	path := "backups/desk-" + time.Now().UTC().Format("20060102-150405") + ".dump"
	b := composeBytes(nil, "exec", "-T", primary(), "pg_dump", "-U", "postgres", "-d", "desk", "-Fc")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(e)
	_, e = f.Write(b)
	must(e)
	must(f.Close())
	hash := fmt.Sprintf("%x", sha256.Sum256(b))
	must(os.WriteFile(strings.TrimSuffix(path, ".dump")+".sha256", []byte(hash+"  "+filepath.Base(path)+"\n"), 0600))
	record("Database backup", map[string]any{"file": path, "sha256": hash, "bytes": len(b)})
}
func copyBlocks(text string) map[string][]string {
	rows := map[string][]string{}
	current := ""
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "COPY "):
			current = line
			rows[current] = []string{}
		case line == `\.`:
			current = ""
		case current != "":
			rows[current] = append(rows[current], line)
		}
	}
	for _, v := range rows {
		sort.Strings(v)
	}
	return rows
}
func restore(path string) {
	require(path != "", "restore requires --file backups/NAME.dump")
	p := primary()
	target := "restore_" + platform.Token(4)
	b, e := os.ReadFile(path)
	must(e)
	sumPath := strings.TrimSuffix(path, ".dump") + ".sha256"
	checksum, e := os.ReadFile(sumPath)
	must(e)
	require(strings.Fields(string(checksum))[0] == fmt.Sprintf("%x", sha256.Sum256(b)), "backup checksum mismatch")
	compose("exec", "-T", p, "createdb", "-U", "postgres", target)
	composeBytes(b, "exec", "-T", p, "pg_restore", "-U", "postgres", "--exit-on-error", "--no-owner", "--no-privileges", "-d", target)
	source := string(composeBytes(b, "exec", "-T", p, "pg_restore", "--data-only", "-f", "-"))
	actual := compose("exec", "-T", p, "pg_dump", "-U", "postgres", "-d", target, "--data-only")
	want := copyBlocks(source)
	require(len(want) >= 5 && reflect.DeepEqual(want, copyBlocks(actual)), "restored rows differ from dump")
	counts := map[string]string{}
	for _, table := range strings.Fields(sql(p, "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename", target)) {
		counts[table] = sql(p, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize(), target)
	}
	record("Restore into isolated database", map[string]any{"database": target, "rows": counts, "all_tables_exact_match": true, "tables_compared": len(want)})
}
func credentials() map[string]string {
	b, e := os.ReadFile("/run/admin/credentials.json")
	must(e)
	var v map[string]string
	must(json.Unmarshal(b, &v))
	return v
}
func freshBroker() {
	v := credentials()
	env := []string{"RABBITMQ_DEFAULT_PASS=" + v["MQ_PASSWORD"], "RABBITMQ_ERLANG_COOKIE=" + v["MQ_COOKIE"], "MQ_PASSWORD=" + v["MQ_PASSWORD"]}
	network := project + "-fresh-" + platform.Token(3)
	docker("network", "create", "--internal", network)
	defer docker("network", "rm", network)
	image := strings.Fields(compose("images", "-q", "rabbit1"))[0]
	names := []string{}
	defer func() {
		for _, name := range names {
			docker("rm", "-f", "-v", name)
		}
	}()
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("%s-rabbit%d", network, i)
		host := fmt.Sprintf("rabbit%d", i)
		_, e := command(env, nil, "run", "-d", "--name", name, "--hostname", host, "--network", network, "--network-alias", host, "-e", "RABBITMQ_DEFAULT_USER=desk", "-e", "RABBITMQ_DEFAULT_PASS", "-e", "RABBITMQ_ERLANG_COOKIE", "-e", "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS=+S 2:2", "-v", filepath.Join(hostDir, "infra/rabbitmq/rabbitmq.conf")+":/etc/rabbitmq/rabbitmq.conf:ro", image)
		must(e)
		names = append(names, name)
	}
	wait(func() bool {
		for _, name := range names {
			docker("exec", "--user", "rabbitmq", name, "rabbitmq-diagnostics", "-q", "check_running")
		}
		return true
	}, 120)
	_, e := command(env, nil, "run", "--rm", "--network", network, "-e", "MQ_PASSWORD", "-e", "MQ_HOSTS=rabbit1,rabbit2,rabbit3", "securedesk-app:local", "/usr/local/bin/check", "broker")
	must(e)
	raw := docker("exec", "--user", "rabbitmq", names[0], "rabbitmqctl", "list_queues", "name", "members", "online", "--formatter=json")
	var rows []map[string]any
	must(json.Unmarshal([]byte(raw[strings.Index(raw, "["):]), &rows))
	for _, r := range rows {
		if r["name"] == "desk.events" {
			require(len(r["members"].([]any)) == 3 && len(r["online"].([]any)) == 3, "quorum must have three members")
			record("Fresh three-node broker cluster", r)
			return
		}
	}
	panic(errors.New("queue missing"))
}
func pack() {
	allowedDirs := map[string]bool{"cmd": true, "internal": true, "db": true, "infra": true, "web": true, "scripts": true, "docs": true, "evidence": true}
	allowedFiles := map[string]bool{}
	for _, p := range []string{"README.md", "Makefile", "compose.yaml", "Dockerfile", "go.mod", "go.sum", "package.json", "package-lock.json", ".gitignore", ".dockerignore"} {
		allowedFiles[p] = true
	}
	secrets := credentials()
	must(os.MkdirAll("deliverables", 0755))
	path := "deliverables/SecureDesk-Docker-source.zip"
	f, e := os.Create(path)
	must(e)
	z := zip.NewWriter(f)
	count := 0
	e = filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if p == "evidence/package.json" {
			return nil
		}
		first := strings.Split(filepath.ToSlash(p), "/")[0]
		if d.IsDir() {
			if !allowedDirs[first] || d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if !allowedDirs[first] && !allowedFiles[p] || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, v := range secrets {
			if v != "" && bytes.Contains(b, []byte(v)) {
				return fmt.Errorf("secret found in %s", p)
			}
		}
		w, err := z.Create("SecureDesk/" + filepath.ToSlash(p))
		if err == nil {
			_, err = w.Write(b)
		}
		count++
		return err
	})
	must(e)
	must(z.Close())
	must(f.Close())
	r, e := zip.OpenReader(path)
	must(e)
	for _, entry := range r.File {
		c, e := entry.Open()
		must(e)
		_, e = io.Copy(io.Discard, c)
		must(e)
		must(c.Close())
	}
	must(r.Close())
	b, e := os.ReadFile(path)
	must(e)
	hash := fmt.Sprintf("%x", sha256.Sum256(b))
	must(os.WriteFile(strings.TrimSuffix(path, ".zip")+".sha256", []byte(hash+"  "+filepath.Base(path)+"\n"), 0644))
	record("Verified source archive", map[string]any{"files": count, "bytes": len(b), "sha256": hash})
}
func run() (code int) {
	if len(os.Args) < 2 {
		fmt.Println("Usage: ops sync|firewall|delivery|cache-outage|rabbit-failover|pg-failover|rejoin|backup|restore --file PATH|fresh-broker|package")
		return 2
	}
	action := os.Args[1]
	if action == "admin-credentials" {
		fmt.Println("Username: securedesk_admin")
		fmt.Println("Password:", credentials()["ADMIN_PASSWORD"])
		return 0
	}
	must(os.MkdirAll("evidence", 0755))
	defer func() {
		if e := recover(); e != nil {
			fmt.Fprintln(os.Stderr, e)
			results = append(results, result{action, "FAIL", fmt.Sprint(e)})
			code = 1
		}
		b, _ := json.MarshalIndent(results, "", "  ")
		_ = os.WriteFile("evidence/"+action+".json", b, 0644)
	}()
	if action != "package" {
		initializeCompose()
	}
	switch action {
	case "sync":
		syncReplication()
	case "firewall":
		firewall()
	case "delivery":
		delivery()
	case "cache-outage":
		cacheOutage()
	case "rabbit-failover":
		rabbitFailover()
	case "pg-failover":
		pgFailover()
	case "rejoin":
		rejoin()
	case "backup":
		backup()
	case "restore":
		require(len(os.Args) == 4 && os.Args[2] == "--file", "use restore --file backups/NAME.dump")
		restore(os.Args[3])
	case "fresh-broker":
		freshBroker()
	case "package":
		pack()
	default:
		panic(errors.New("unknown action"))
	}
	return 0
}
func main() { os.Exit(run()) }
