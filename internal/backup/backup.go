// Package backup orchestrates PostgreSQL dumps and reports the outcome as a
// set of metrics ready to be shipped to Zabbix.
package backup

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/toporatio/pgbackup-zabbix/internal/config"
)

// DBResult is the per-database outcome of a backup attempt.
type DBResult struct {
	Database string
	File     string
	Size     int64 // bytes; 0 on failure
	Skipped  bool  // true when the file already existed
	Err      error
}

// Result aggregates everything we want to publish.
type Result struct {
	StartedAt time.Time
	Duration  time.Duration

	BackupOK bool
	VacuumOK bool

	Databases []DBResult

	// VacuumOutput captures stderr+stdout of vacuumdb, useful for logs.
	VacuumOutput string
}

// Logger is the subset of stdlib logger we depend on.
type Logger interface {
	Printf(format string, v ...any)
}

// Orchestrator wires together Postgres tooling, retention policy and metrics.
type Orchestrator struct {
	Cfg    *config.Config
	Runner Runner
	Log    Logger
	Clock  func() time.Time
}

// New returns an Orchestrator with sane defaults.
func New(cfg *config.Config) *Orchestrator {
	return &Orchestrator{
		Cfg:    cfg,
		Runner: ExecRunner{},
		Log:    log.Default(),
		Clock:  time.Now,
	}
}

// Run executes the full pipeline: ensure directory exists, list databases,
// dump each non-excluded database, drop old archives and finish with a
// vacuumdb -a -z. The returned Result is safe to publish even on partial
// failure.
func (o *Orchestrator) Run(ctx context.Context) Result {
	start := o.Clock()
	res := Result{StartedAt: start, BackupOK: true, VacuumOK: true}

	if err := os.MkdirAll(o.Cfg.Backup.Directory, 0o750); err != nil {
		o.Log.Printf("create backup dir %s: %v", o.Cfg.Backup.Directory, err)
		res.BackupOK = false
		res.Duration = o.Clock().Sub(start)
		return res
	}

	databases, err := o.listDatabases(ctx)
	if err != nil {
		o.Log.Printf("list databases: %v", err)
		res.BackupOK = false
		res.Duration = o.Clock().Sub(start)
		return res
	}
	o.Log.Printf("databases to back up: %v", databases)

	dateStamp := start.Format("20060102")
	for _, db := range databases {
		dbRes := o.dumpDatabase(ctx, db, dateStamp)
		if dbRes.Err != nil {
			res.BackupOK = false
		}
		res.Databases = append(res.Databases, dbRes)
	}

	// Retention cleanup runs even if one of the dumps failed: there is no point
	// in keeping six months of dumps because tonight's run died on a single DB.
	if err := o.cleanupOldDumps(databases); err != nil {
		o.Log.Printf("cleanup: %v", err)
	}

	vacOut, vacErr := o.runVacuum(ctx)
	res.VacuumOutput = vacOut
	if vacErr != nil {
		o.Log.Printf("vacuum failed: %v", vacErr)
		res.VacuumOK = false
	}

	res.Duration = o.Clock().Sub(start)
	return res
}

func (o *Orchestrator) listDatabases(ctx context.Context) ([]string, error) {
	args := []string{
		"-h", o.Cfg.Postgres.Host,
		"-p", strconv.Itoa(o.Cfg.Postgres.Port),
		"-U", o.Cfg.Postgres.User,
		"-Atc", "SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname;",
		"postgres",
	}
	stdout, stderr, err := o.Runner.Run(ctx, o.Cfg.Tools.Psql, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("psql: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}

	exclude := make(map[string]struct{}, len(o.Cfg.Backup.ExcludeDatabases))
	for _, name := range o.Cfg.Backup.ExcludeDatabases {
		exclude[strings.TrimSpace(name)] = struct{}{}
	}

	var dbs []string
	for _, line := range strings.Split(string(stdout), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, skip := exclude[name]; skip {
			continue
		}
		dbs = append(dbs, name)
	}
	return dbs, nil
}

func (o *Orchestrator) dumpDatabase(ctx context.Context, db, dateStamp string) DBResult {
	file := filepath.Join(o.Cfg.Backup.Directory, fmt.Sprintf("%s_%s.dump", db, dateStamp))
	res := DBResult{Database: db, File: file}

	if info, err := os.Stat(file); err == nil {
		o.Log.Printf("dump %s exists, skipping (%d bytes)", file, info.Size())
		res.Skipped = true
		res.Size = info.Size()
		return res
	}

	args := []string{
		"-Fc",
		"-h", o.Cfg.Postgres.Host,
		"-p", strconv.Itoa(o.Cfg.Postgres.Port),
		"-U", o.Cfg.Postgres.User,
		"-f", file,
		db,
	}
	_, stderr, err := o.Runner.Run(ctx, o.Cfg.Tools.PgDump, nil, args...)
	if err != nil {
		res.Err = fmt.Errorf("pg_dump %s: %w (stderr=%s)", db, err, strings.TrimSpace(string(stderr)))
		// Remove any partial file so retention doesn't keep half-broken dumps.
		_ = os.Remove(file)
		return res
	}

	info, statErr := os.Stat(file)
	if statErr != nil {
		res.Err = fmt.Errorf("stat dump %s: %w", file, statErr)
		return res
	}
	res.Size = info.Size()
	return res
}

func (o *Orchestrator) cleanupOldDumps(databases []string) error {
	if o.Cfg.Backup.RetentionDays <= 0 {
		return nil
	}
	cutoff := o.Clock().Add(-time.Duration(o.Cfg.Backup.RetentionDays) * 24 * time.Hour)

	entries, err := os.ReadDir(o.Cfg.Backup.Directory)
	if err != nil {
		return err
	}
	dbSet := make(map[string]struct{}, len(databases))
	for _, db := range databases {
		dbSet[db] = struct{}{}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".dump") {
			continue
		}
		// Format: <db>_<YYYYMMDD>.dump
		idx := strings.LastIndex(name, "_")
		if idx <= 0 {
			continue
		}
		db := name[:idx]
		if _, ok := dbSet[db]; !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			full := filepath.Join(o.Cfg.Backup.Directory, name)
			if err := os.Remove(full); err != nil {
				o.Log.Printf("remove %s: %v", full, err)
			} else {
				o.Log.Printf("removed old dump %s", full)
			}
		}
	}
	return nil
}

func (o *Orchestrator) runVacuum(ctx context.Context) (string, error) {
	args := []string{
		"-a", "-z",
		"-h", o.Cfg.Postgres.Host,
		"-p", strconv.Itoa(o.Cfg.Postgres.Port),
		"-U", o.Cfg.Postgres.User,
	}
	stdout, stderr, err := o.Runner.Run(ctx, o.Cfg.Tools.VacuumDB, nil, args...)
	combined := strings.TrimSpace(string(stdout) + string(stderr))
	return combined, err
}
