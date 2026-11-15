// Package platform contains infrastructure shared by the independently deployed services.
package platform

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/scrypt"
)

func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Secret supports environment overrides for tests and scoped read-only Docker volumes.
func Secret(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	b, _ := os.ReadFile(filepath.Join(Env("SECRETS_DIR", "/run/securedesk"), key))
	return strings.TrimSpace(string(b))
}
func Token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func TokenHash(token string) string {
	b := sha256.Sum256([]byte(token))
	return hex.EncodeToString(b[:])
}
func HashPassword(password string) (string, error) {
	salt := Token(16)
	key, err := scrypt.Key([]byte(password), mustDecode(salt), 16384, 8, 1, 64)
	return salt + ":" + hex.EncodeToString(key), err
}
func mustDecode(s string) []byte { b, _ := hex.DecodeString(s); return b }

// The encoding intentionally remains compatible with previously created accounts.
func PasswordMatches(password, encoded string) bool {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 {
		return false
	}
	salt, e := hex.DecodeString(parts[0])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := hex.DecodeString(parts[1])
	if e != nil || len(want) != 64 {
		return false
	}
	got, e := scrypt.Key([]byte(password), salt, 16384, 8, 1, 64)
	return e == nil && subtle.ConstantTimeCompare(got, want) == 1
}
func DB(ctx context.Context) (*pgx.Conn, error) {
	if Secret("DB_PASSWORD") == "" {
		return nil, errors.New("DB_PASSWORD required")
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(Env("DB_USER", "desk_api"), Secret("DB_PASSWORD")), Host: Env("DB_HOSTS", "172.29.20.10,172.29.20.11"), Path: "/desk"}
	q := u.Query()
	q.Set("sslmode", "disable")
	q.Set("target_session_attrs", "read-write")
	q.Set("connect_timeout", "2")
	q.Set("statement_timeout", "8000")
	q.Set("lock_timeout", "3000")
	u.RawQuery = q.Encode()
	return pgx.Connect(ctx, u.String())
}
func Redis() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: Env("REDIS_ADDR", "172.29.30.10:6379"), Password: Secret("REDIS_PASSWORD"), DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, MaxRetries: -1})
}
func TCP(address string) bool {
	c, e := net.DialTimeout("tcp", address, 2*time.Second)
	if e != nil {
		return false
	}
	c.Close()
	return true
}
