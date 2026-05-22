package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreate_CreatesTemplateWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg, err := LoadOrCreate(path)
	if !errors.Is(err, ErrTemplateCreated) {
		t.Fatalf("expected ErrTemplateCreated, got %v (cfg=%v)", err, cfg)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("template not written: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("template file is empty")
	}
	if got := data[:1]; got[0] != '#' {
		t.Fatalf("template should start with a comment, got %q", string(data[:30]))
	}
}

func TestLoadOrCreate_ParsesValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte(`
postgres:
  host: db.local
  port: 5433
  user: backup
backup:
  directory: /var/backups/pg
  retention_days: 3
  exclude_databases: [template0, template1]
tools:
  pg_dump: /usr/local/bin/pg_dump
  vacuumdb: /usr/local/bin/vacuumdb
  psql: /usr/local/bin/psql
zabbix:
  enabled: true
  server: zbx.local
  port: 10051
  host: pg-backup-host
  timeout_seconds: 5
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}
	if cfg.Postgres.Host != "db.local" || cfg.Postgres.Port != 5433 {
		t.Errorf("postgres parsed wrong: %+v", cfg.Postgres)
	}
	if cfg.Backup.RetentionDays != 3 {
		t.Errorf("retention parsed wrong: %d", cfg.Backup.RetentionDays)
	}
	if len(cfg.Backup.ExcludeDatabases) != 2 {
		t.Errorf("excludes parsed wrong: %v", cfg.Backup.ExcludeDatabases)
	}
	if cfg.Zabbix.Server != "zbx.local" || cfg.Zabbix.Host != "pg-backup-host" {
		t.Errorf("zabbix parsed wrong: %+v", cfg.Zabbix)
	}
}

func TestLoadOrCreate_AppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte(`
backup:
  directory: /tmp/dumps
zabbix:
  enabled: false
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}
	if cfg.Postgres.Port != 5432 || cfg.Postgres.User != "postgres" {
		t.Errorf("postgres defaults not applied: %+v", cfg.Postgres)
	}
	if cfg.Backup.RetentionDays != 7 {
		t.Errorf("retention default not applied: %d", cfg.Backup.RetentionDays)
	}
	if cfg.Tools.PgDump != "pg_dump" {
		t.Errorf("pg_dump default not applied: %s", cfg.Tools.PgDump)
	}
}

func TestValidate_RejectsEmptyBackupDir(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for empty backup.directory")
	}
}

func TestValidate_RejectsZabbixWithoutServer(t *testing.T) {
	c := &Config{}
	c.Backup.Directory = "/tmp/x"
	c.Zabbix.Enabled = true
	c.applyDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error when zabbix.enabled but server missing")
	}
}
