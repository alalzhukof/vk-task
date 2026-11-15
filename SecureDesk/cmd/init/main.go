package main

import (
	"context"
	"fmt"
	"os"
	"securedesk/internal/bootstrap"
	"securedesk/internal/migrations"
	"securedesk/internal/platform"
	"time"
)

func run() error {
	if len(os.Args) == 2 && os.Args[1] == "sync" {
		os.Setenv("DB_USER", "postgres")
		os.Setenv("DB_PASSWORD", platform.Secret("PG_PASSWORD"))
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		for ctx.Err() == nil {
			c, err := platform.DB(ctx)
			if err == nil {
				var n int
				err = c.QueryRow(ctx, "SELECT count(*) FROM pg_stat_replication WHERE application_name='standby' AND state='streaming'").Scan(&n)
				if err == nil && n == 1 {
					_, err = c.Exec(ctx, "ALTER SYSTEM SET synchronous_standby_names = 'FIRST 1 (standby)'")
					if err == nil {
						_, err = c.Exec(ctx, "SELECT pg_reload_conf()")
					}
					if err == nil {
						err = c.QueryRow(ctx, "SELECT count(*) FROM pg_stat_replication WHERE application_name='standby' AND sync_state='sync'").Scan(&n)
					}
					if err == nil && n == 1 {
						err = migrations.Apply(ctx, c, platform.Secret("ADMIN_PASSWORD"))
						c.Close(context.Background())
						if err != nil {
							return err
						}
						fmt.Println("PASS: synchronous replication enabled")
						fmt.Println("PASS: database migrations and administrator ready")
						return nil
					}
					c.Close(context.Background())
				} else {
					c.Close(context.Background())
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		return fmt.Errorf("synchronous replication did not become ready")
	}
	existing := false
	for _, p := range []string{"/existing/primary/PG_VERSION", "/existing/standby/PG_VERSION"} {
		if _, err := os.Stat(p); err == nil {
			existing = true
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := bootstrap.Init("/secrets", existing, os.Getenv); err != nil {
		return err
	}
	fmt.Println("PASS: persistent credentials ready; values are not printed")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
