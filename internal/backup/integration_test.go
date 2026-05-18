//go:build integration

package backup

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/toporatio/pgbackup-zabbix/internal/config"
)

// These tests exercise the real pg_dump/psql/vacuumdb binaries against an
// actual PostgreSQL instance. Skip unless PG_TEST_HOST is provided.
func envOrSkip(t *testing.T) (host string, port int, user, pwd string) {
	t.Helper()
	host = os.Getenv("PG_TEST_HOST")
	if host == "" {
		t.Skip("PG_TEST_HOST not set; skipping integration test")
	}
	portStr := os.Getenv("PG_TEST_PORT")
	if portStr == "" {
		portStr = "5432"
	}
	var err error
	port, err = strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("PG_TEST_PORT not numeric: %v", err)
	}
	user = os.Getenv("PG_TEST_USER")
	if user == "" {
		user = "postgres"
	}
	pwd = os.Getenv("PG_TEST_PASSWORD")
	return
}

// preparePgpass writes a temporary .pgpass and points the postgres tooling at
// it via the PGPASSFILE environment variable inherited by os/exec.
func preparePgpass(t *testing.T, host string, port int, user, pwd string) string {
	t.Helper()
	dir := t.TempDir()
	pgpass := filepath.Join(dir, "pgpass")
	body := fmt.Sprintf("%s:%d:*:%s:%s\n", host, port, user, pwd)
	if err := os.WriteFile(pgpass, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSFILE", pgpass)
	return pgpass
}

func TestIntegration_FullPipeline(t *testing.T) {
	host, port, user, pwd := envOrSkip(t)
	preparePgpass(t, host, port, user, pwd)

	// Locate binaries.
	pgDump := which(t, "pg_dump")
	vacuumDB := which(t, "vacuumdb")
	psql := which(t, "psql")

	// Create two test databases.
	dbs := []string{"itestapp1_" + uniq(), "itestapp2_" + uniq()}
	for _, db := range dbs {
		runPsql(t, psql, host, port, user, "postgres", "CREATE DATABASE "+db)
		t.Cleanup(func() {
			runPsql(t, psql, host, port, user, "postgres", "DROP DATABASE IF EXISTS "+db)
		})
		// Put some data inside so the dump is non-trivial.
		runPsql(t, psql, host, port, user, db, "CREATE TABLE t(id int); INSERT INTO t VALUES (1),(2),(3)")
	}

	dir := t.TempDir()
	cfg := &config.Config{
		Postgres: config.Postgres{Host: host, Port: port, User: user},
		Backup: config.Backup{
			Directory:        dir,
			RetentionDays:    7,
			ExcludeDatabases: []string{"postgres", "template0", "template1"},
		},
		Tools: config.Tools{PgDump: pgDump, VacuumDB: vacuumDB, Psql: psql},
	}
	o := New(cfg)
	o.Log = log.Default()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res := o.Run(ctx)

	if !res.BackupOK {
		t.Fatalf("backup failed: %+v", res)
	}
	if !res.VacuumOK {
		t.Fatalf("vacuum failed: output=%s", res.VacuumOutput)
	}

	// Both of our DBs must appear in the result with non-zero size.
	dumpedDBs := map[string]int64{}
	for _, r := range res.Databases {
		if r.Err != nil {
			t.Errorf("db %s reported err: %v", r.Database, r.Err)
		}
		dumpedDBs[r.Database] = r.Size
	}
	for _, db := range dbs {
		size, ok := dumpedDBs[db]
		if !ok {
			t.Errorf("db %s missing from result", db)
			continue
		}
		if size <= 0 {
			t.Errorf("db %s dump has zero size", db)
		}
		// Dump file must exist on disk.
		expected := filepath.Join(dir, fmt.Sprintf("%s_%s.dump", db, res.StartedAt.Format("20060102")))
		info, err := os.Stat(expected)
		if err != nil {
			t.Errorf("dump file %s missing: %v", expected, err)
			continue
		}
		if info.Size() != size {
			t.Errorf("db %s: reported size %d != file size %d", db, size, info.Size())
		}
	}
}

func uniq() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }

func runPsql(t *testing.T, psql, host string, port int, user, db, sql string) {
	t.Helper()
	r := ExecRunner{}
	_, stderr, err := r.Run(context.Background(), psql, nil,
		"-h", host, "-p", strconv.Itoa(port), "-U", user, "-d", db, "-v", "ON_ERROR_STOP=1", "-c", sql)
	if err != nil {
		t.Fatalf("psql %q: %v (stderr=%s)", sql, err, strings.TrimSpace(string(stderr)))
	}
}

func which(t *testing.T, name string) string {
	t.Helper()
	for _, p := range []string{"/usr/bin/" + name, "/usr/local/bin/" + name} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatalf("binary %s not found in /usr/bin or /usr/local/bin", name)
	return ""
}
