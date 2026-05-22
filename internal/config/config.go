// Package config loads runtime configuration from a YAML file located next to
// the binary. When the file is missing it is created from a built-in template
// so that the operator only has to edit the generated file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Postgres holds the parameters required to talk to the cluster.
type Postgres struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
}

// Backup describes filesystem layout and retention policy for dumps.
type Backup struct {
	Directory       string   `yaml:"directory"`
	RetentionDays   int      `yaml:"retention_days"`
	ExcludeDatabases []string `yaml:"exclude_databases"`
}

// Tools collects absolute paths to the external binaries the utility shells
// out to. Keeping them configurable mirrors the original bash script and lets
// the operator override them on hosts where PATH lookup is unreliable.
type Tools struct {
	PgDump    string `yaml:"pg_dump"`
	VacuumDB  string `yaml:"vacuumdb"`
	Psql      string `yaml:"psql"`
}

// Zabbix carries the trapper endpoint and the host identifier under which the
// metrics will be registered on the Zabbix server.
type Zabbix struct {
	Enabled  bool   `yaml:"enabled"`
	Server   string `yaml:"server"`
	Port     int    `yaml:"port"`
	Host     string `yaml:"host"`
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// Config is the top-level shape persisted in config.yaml.
type Config struct {
	Postgres Postgres `yaml:"postgres"`
	Backup   Backup   `yaml:"backup"`
	Tools    Tools    `yaml:"tools"`
	Zabbix   Zabbix   `yaml:"zabbix"`
}

// Template is the content materialised on first start when the file is missing.
const Template = `# pgbackup-zabbix configuration
# This file is generated automatically on first run. Edit it and re-run the binary.

postgres:
  host: 127.0.0.1
  port: 5432
  user: postgres
  # Password is NOT stored here. Provide it via ~/.pgpass for the user that runs the binary.
  # Format of ~/.pgpass:  hostname:port:database:username:password   (chmod 600)

backup:
  directory: /mnt/DATABASES/
  retention_days: 7
  exclude_databases:
    - postgres
    - template0
    - template1

tools:
  pg_dump: /usr/bin/pg_dump
  vacuumdb: /usr/bin/vacuumdb
  psql: /usr/bin/psql

zabbix:
  enabled: true
  server: 127.0.0.1
  port: 10051
  # Host name as configured on the Zabbix server (must match the "Host name" field, not the visible name).
  host: pg-backup-host
  timeout_seconds: 10
`

// DefaultPath returns the path of the config file co-located with the running binary.
func DefaultPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "config.yaml"), nil
}

// LoadOrCreate reads the config from path. If the file does not exist the
// template is written and ErrTemplateCreated is returned alongside a partially
// populated Config so that the caller can decide whether to abort or run with
// defaults. Any other I/O or parse error is propagated.
func LoadOrCreate(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if writeErr := os.WriteFile(path, []byte(Template), 0o600); writeErr != nil {
			return nil, fmt.Errorf("write template %s: %w", path, writeErr)
		}
		return nil, ErrTemplateCreated
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ErrTemplateCreated signals that a fresh template was written and the caller
// should stop and ask the operator to edit it.
var ErrTemplateCreated = errors.New("config template created; edit it and rerun")

func (c *Config) applyDefaults() error {
	if c.Postgres.Host == "" {
		c.Postgres.Host = "127.0.0.1"
	}
	if c.Postgres.Port == 0 {
		c.Postgres.Port = 5432
	}
	if c.Postgres.User == "" {
		c.Postgres.User = "postgres"
	}
	if c.Backup.RetentionDays == 0 {
		c.Backup.RetentionDays = 7
	}
	if c.Tools.PgDump == "" {
		c.Tools.PgDump = "pg_dump"
	}
	if c.Tools.VacuumDB == "" {
		c.Tools.VacuumDB = "vacuumdb"
	}
	if c.Tools.Psql == "" {
		c.Tools.Psql = "psql"
	}
	if c.Zabbix.Port == 0 {
		c.Zabbix.Port = 10051
	}
	if c.Zabbix.TimeoutSeconds == 0 {
		c.Zabbix.TimeoutSeconds = 10
	}
	return nil
}

// Validate performs cheap sanity checks before we start spawning external processes.
func (c *Config) Validate() error {
	if c.Backup.Directory == "" {
		return errors.New("backup.directory is required")
	}
	if c.Zabbix.Enabled {
		if c.Zabbix.Server == "" {
			return errors.New("zabbix.server is required when zabbix.enabled=true")
		}
		if c.Zabbix.Host == "" {
			return errors.New("zabbix.host is required when zabbix.enabled=true")
		}
	}
	return nil
}
