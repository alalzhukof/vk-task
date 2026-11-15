package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func empty(string) string { return "" }
func TestGeneratedCredentialsPersistAndRepairScopes(t *testing.T) {
	d := t.TempDir()
	if e := Init(d, false, empty); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "admin/credentials.json")
	before, _ := os.ReadFile(p)
	if e := os.Remove(filepath.Join(d, "api/DB_PASSWORD")); e != nil {
		t.Fatal(e)
	}
	if e := Init(d, true, empty); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("credentials changed")
	}
	b, e := os.ReadFile(filepath.Join(d, "api/DB_PASSWORD"))
	if e != nil || len(b) != 48 {
		t.Fatal("scoped credential not repaired")
	}
	if _, e = os.Stat(filepath.Join(d, "api/PG_PASSWORD")); !os.IsNotExist(e) {
		t.Fatal("API received admin secret")
	}
}
func TestExistingDatabaseRequiresOriginalSecrets(t *testing.T) {
	if Init(t.TempDir(), true, empty) == nil {
		t.Fatal("must refuse regeneration")
	}
}
func TestMigrationAndConflict(t *testing.T) {
	d := t.TempDir()
	v := strings.Repeat("ab", 24)
	lookup := func(string) string { return v }
	if e := Init(d, true, lookup); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(d, "worker/DB_PASSWORD"))
	if string(b) != v {
		t.Fatal("migration changed password")
	}
	if Init(d, true, func(string) string { return strings.Repeat("cd", 24) }) == nil {
		t.Fatal("conflict was ignored")
	}
	b, _ = os.ReadFile(filepath.Join(d, "worker/DB_PASSWORD"))
	if string(b) != v {
		t.Fatal("conflict overwrote stored password")
	}
}
func TestInvalidStoredCredentialsRejected(t *testing.T) {
	d := t.TempDir()
	if e := Init(d, false, empty); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(d, "admin/credentials.json"), []byte(`{}`), 0600)
	if Init(d, false, empty) == nil {
		t.Fatal("accepted corrupt secrets")
	}
}

func TestUpgradeAddsAdminCredentialAndPreservesExisting(t *testing.T) {
	d := t.TempDir()
	if e := Init(d, false, empty); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "admin/credentials.json")
	raw, _ := os.ReadFile(p)
	var old map[string]string
	if e := json.Unmarshal(raw, &old); e != nil {
		t.Fatal(e)
	}
	delete(old, "ADMIN_PASSWORD")
	raw, _ = json.Marshal(old)
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e := Init(d, true, empty); e != nil {
		t.Fatal(e)
	}
	raw, _ = os.ReadFile(p)
	var next map[string]string
	json.Unmarshal(raw, &next)
	for key, value := range old {
		if next[key] != value {
			t.Fatal("changed existing secret", key)
		}
	}
	if len(next["ADMIN_PASSWORD"]) != 48 {
		t.Fatal("missing administrator")
	}
	for _, scope := range []string{"api", "worker"} {
		if _, e := os.Stat(filepath.Join(d, scope, "ADMIN_PASSWORD")); !os.IsNotExist(e) {
			t.Fatal("admin secret leaked to", scope)
		}
	}
}
