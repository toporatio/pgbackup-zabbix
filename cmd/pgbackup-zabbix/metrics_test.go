package main

import (
	"testing"
	"time"

	"github.com/toporatio/pgbackup-zabbix/internal/backup"
	"github.com/toporatio/pgbackup-zabbix/internal/config"
)

func TestBuildMetrics_KeyShape(t *testing.T) {
	cfg := &config.Config{Zabbix: config.Zabbix{Host: "pg-backup-host"}}
	res := backup.Result{
		BackupOK: true,
		VacuumOK: false,
		Duration: 12 * time.Second,
		Databases: []backup.DBResult{
			{Database: "shop", Size: 1024},
			{Database: "warehouse", Size: 2048},
		},
	}
	got := buildMetrics(cfg, res, time.Unix(1700000000, 0))

	want := map[string]string{
		"pg.backup.status":          "1",
		"pg.backup.duration":        "12",
		"pg.vacuum.status":          "0",
		"pg.backup.size[shop]":      "1024",
		"pg.backup.size[warehouse]": "2048",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d metrics, got %d (%v)", len(want), len(got), got)
	}
	for _, m := range got {
		exp, ok := want[m.Key]
		if !ok {
			t.Errorf("unexpected metric key %s", m.Key)
			continue
		}
		if m.Value != exp {
			t.Errorf("metric %s: got %s, want %s", m.Key, m.Value, exp)
		}
		if m.Host != "pg-backup-host" {
			t.Errorf("metric %s: wrong host %s", m.Key, m.Host)
		}
	}
}

func TestBuildMetrics_FailureSetsStatusZero(t *testing.T) {
	cfg := &config.Config{Zabbix: config.Zabbix{Host: "h"}}
	res := backup.Result{BackupOK: false, VacuumOK: true, Duration: time.Second}
	got := buildMetrics(cfg, res, time.Unix(0, 0))
	var found bool
	for _, m := range got {
		if m.Key == "pg.backup.status" {
			found = true
			if m.Value != "0" {
				t.Errorf("expected status=0 on failure, got %s", m.Value)
			}
		}
	}
	if !found {
		t.Fatal("pg.backup.status metric missing")
	}
}
