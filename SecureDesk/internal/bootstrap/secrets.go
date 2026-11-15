// Package bootstrap initializes scoped, persistent credentials before services start.
package bootstrap

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"securedesk/internal/platform"
)

var Keys = []string{"PG_PASSWORD", "API_DB_PASSWORD", "WORKER_DB_PASSWORD", "REPL_PASSWORD", "REDIS_PASSWORD", "MQ_PASSWORD", "MQ_COOKIE", "ADMIN_PASSWORD"}
var scopes = map[string]map[string]string{
	"api":    {"DB_PASSWORD": "API_DB_PASSWORD", "REDIS_PASSWORD": "REDIS_PASSWORD"},
	"worker": {"DB_PASSWORD": "WORKER_DB_PASSWORD", "MQ_PASSWORD": "MQ_PASSWORD"},
	"db":     {"PG_PASSWORD": "PG_PASSWORD", "API_DB_PASSWORD": "API_DB_PASSWORD", "WORKER_DB_PASSWORD": "WORKER_DB_PASSWORD", "REPL_PASSWORD": "REPL_PASSWORD", "ADMIN_PASSWORD": "ADMIN_PASSWORD"},
	"login":  {"ADMIN_PASSWORD": "ADMIN_PASSWORD"},
	"redis":  {"REDIS_PASSWORD": "REDIS_PASSWORD"},
	"rabbit": {"MQ_PASSWORD": "MQ_PASSWORD", "MQ_COOKIE": "MQ_COOKIE"},
}

// Init keeps a canonical set first, then repairs any interrupted distribution.
// Existing database volumes must never receive silently regenerated credentials.
func Init(base string, existingDatabase bool, lookup func(string) string) error {
	path := filepath.Join(base, "admin", "credentials.json")
	values := map[string]string{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err = json.Unmarshal(b, &values); err != nil {
			return errors.New("invalid stored credentials")
		}
	case os.IsNotExist(err):
		for _, key := range Keys {
			values[key] = lookup(key)
			if values[key] == "" {
				if existingDatabase && key != "ADMIN_PASSWORD" {
					return fmt.Errorf("existing database requires original %s for migration", key)
				}
				values[key] = platform.Token(24)
			}
		}
	default:
		return err
	}
	for _, key := range Keys {
		// Upgrade the original seven-key installation without changing any infrastructure credential.
		if key == "ADMIN_PASSWORD" && values[key] == "" {
			values[key] = platform.Token(24)
		}
		v := values[key]
		if _, e := hex.DecodeString(v); e != nil || len(v) != 48 {
			return fmt.Errorf("invalid credential format: %s", key)
		}
		if old := lookup(key); old != "" && old != v {
			return fmt.Errorf("credential conflict: %s; stored values were preserved", key)
		}
	}
	data, _ := json.Marshal(values)
	if err = atomicWrite(path, data, 0600); err != nil {
		return err
	}
	for role, mapping := range scopes {
		for filename, key := range mapping {
			if err = atomicWrite(filepath.Join(base, role, filename), []byte(values[key]), 0444); err != nil {
				return err
			}
		}
	}
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".credential-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Chmod(mode)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
