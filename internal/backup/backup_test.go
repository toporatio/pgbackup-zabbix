package backup

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toporatio/pgbackup-zabbix/internal/config"
)

type fakeRunner struct {
	psqlOutput   string
	psqlErr      error
	pgDumpErr    error
	pgDumpBody   string
	vacuumErr    error
	vacuumOutput string

	calls []string // "name arg1 arg2 ..."
}

func (f *fakeRunner) Run(_ context.Context, name string, _ []string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch {
	case strings.HasSuffix(name, "psql"):
		return []byte(f.psqlOutput), nil, f.psqlErr
	case strings.HasSuffix(name, "pg_dump"):
		// Find the -f path and write a stub file so size can be measured.
		for i, a := range args {
			if a == "-f" && i+1 < len(args) {
				if err := os.WriteFile(args[i+1], []byte(f.pgDumpBody), 0o600); err != nil {
					return nil, nil, err
				}
			}
		}
		return nil, nil, f.pgDumpErr
	case strings.HasSuffix(name, "vacuumdb"):
		return []byte(f.vacuumOutput), nil, f.vacuumErr
	}
	return nil, nil, errors.New("unexpected binary " + name)
}

func newTestCfg(t *testing.T, dir string) *config.Config {
	t.Helper()
	return &config.Config{
		Postgres: config.Postgres{Host: "127.0.0.1", Port: 5432, User: "postgres"},
		Backup: config.Backup{
			Directory:        dir,
			RetentionDays:    2,
			ExcludeDatabases: []string{"postgres", "template0"},
		},
		Tools: config.Tools{PgDump: "/usr/bin/pg_dump", VacuumDB: "/usr/bin/vacuumdb", Psql: "/usr/bin/psql"},
	}
}

func TestOrchestrator_HappyPath(t *testing.T) {
	dir := t.TempDir()
	cfg := newTestCfg(t, dir)

	fr := &fakeRunner{
		psqlOutput: "app1\napp2\npostgres\n",
		pgDumpBody: "FAKEDUMPBODY",
		vacuumOutput: "vacuum: ok",
	}
	now := time.Date(2025, 1, 15, 3, 0, 0, 0, time.UTC)
	o := &Orchestrator{Cfg: cfg, Runner: fr, Log: log.Default(), Clock: func() time.Time { return now }}

	res := o.Run(context.Background())
	if !res.BackupOK || !res.VacuumOK {
		t.Fatalf("expected success, got %+v", res)
	}
	if len(res.Databases) != 2 {
		t.Fatalf("expected 2 dbs (postgres excluded), got %d", len(res.Databases))
	}
	for _, db := range res.Databases {
		if db.Size != int64(len("FAKEDUMPBODY")) {
			t.Errorf("db %s wrong size: %d", db.Database, db.Size)
		}
		want := filepath.Join(dir, db.Database+"_20250115.dump")
		if db.File != want {
			t.Errorf("db %s wrong file: %s", db.Database, db.File)
		}
		if _, err := os.Stat(db.File); err != nil {
			t.Errorf("dump %s not created: %v", db.File, err)
		}
	}
	if res.VacuumOutput != "vacuum: ok" {
		t.Errorf("vacuum output not captured: %q", res.VacuumOutput)
	}
}

func TestOrchestrator_PgDumpFailureMarksUnhealthy(t *testing.T) {
	dir := t.TempDir()
	cfg := newTestCfg(t, dir)
	fr := &fakeRunner{
		psqlOutput: "app1\n",
		pgDumpErr:  errors.New("boom"),
		pgDumpBody: "",
	}
	o := &Orchestrator{Cfg: cfg, Runner: fr, Log: log.Default(), Clock: time.Now}
	res := o.Run(context.Background())
	if res.BackupOK {
		t.Fatal("expected BackupOK=false when pg_dump fails")
	}
	if len(res.Databases) != 1 || res.Databases[0].Err == nil {
		t.Fatalf("expected db-level error, got %+v", res.Databases)
	}
}

func TestOrchestrator_RetentionRemovesOldDumps(t *testing.T) {
	dir := t.TempDir()
	cfg := newTestCfg(t, dir)

	// Create one stale and one fresh dump for db "app1".
	stale := filepath.Join(dir, "app1_20240101.dump")
	fresh := filepath.Join(dir, "app1_20250115.dump")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{
		psqlOutput: "app1\n",
		pgDumpBody: "DUMP",
	}
	o := &Orchestrator{Cfg: cfg, Runner: fr, Log: log.Default(), Clock: time.Now}
	_ = o.Run(context.Background())

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale dump should have been removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh dump must not be removed: %v", err)
	}
}

func TestOrchestrator_SkipExistingDumpIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	cfg := newTestCfg(t, dir)
	now := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	existing := filepath.Join(dir, "app1_20250115.dump")
	if err := os.WriteFile(existing, []byte("existing-blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{
		psqlOutput: "app1\n",
		pgDumpBody: "should-not-overwrite",
	}
	o := &Orchestrator{Cfg: cfg, Runner: fr, Log: log.Default(), Clock: func() time.Time { return now }}
	res := o.Run(context.Background())
	if !res.BackupOK {
		t.Fatalf("expected success, got %+v", res)
	}
	if len(res.Databases) != 1 || !res.Databases[0].Skipped {
		t.Fatalf("expected skipped=true, got %+v", res.Databases)
	}
	data, _ := os.ReadFile(existing)
	if string(data) != "existing-blob" {
		t.Errorf("existing dump must not be overwritten, got %q", data)
	}
}
