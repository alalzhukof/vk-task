// Package migrations upgrades existing and fresh databases in the same transaction.
package migrations

import (
	"context"
	_ "embed"
	"fmt"
	"github.com/jackc/pgx/v5"
	"securedesk/internal/platform"
)

//go:embed 002_roles.sql
var roles string

func Apply(ctx context.Context, c *pgx.Conn, password string) error {
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(75684321)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var applied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=2)`).Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, roles); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(2)`); err != nil {
			return err
		}
	}
	var admins int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin'`).Scan(&admins); err != nil {
		return err
	}
	if admins == 0 {
		if len(password) < 12 {
			return fmt.Errorf("administrator credential missing")
		}
		hash, e := platform.HashPassword(password)
		if e != nil {
			return e
		}
		// A pre-existing public account with this name must never be silently promoted.
		if _, err = tx.Exec(ctx, `INSERT INTO users(username,password_hash,role) VALUES('securedesk_admin',$1,'admin')`, hash); err != nil {
			return fmt.Errorf("cannot create initial administrator: %w", err)
		}
	}
	return tx.Commit(ctx)
}
