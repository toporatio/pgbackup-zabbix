// pgbackup-zabbix backs up every non-excluded PostgreSQL database with
// pg_dump, optionally cleans up old dumps and runs vacuumdb. Instead of
// emailing a report (like the original bash script) it ships metrics to a
// Zabbix server using the Zabbix Sender protocol so the operator can build
// dashboards and triggers on top of them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/toporatio/pgbackup-zabbix/internal/backup"
	"github.com/toporatio/pgbackup-zabbix/internal/config"
	"github.com/toporatio/pgbackup-zabbix/internal/zabbix"
)

func main() {
	var (
		cfgPath    = flag.String("config", "", "path to config.yaml (default: alongside the binary)")
		dryRunZbx  = flag.Bool("no-zabbix", false, "disable Zabbix sending even if enabled in config")
		printOnly  = flag.Bool("print-metrics", false, "after the run, print metrics to stdout (in addition to sending)")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)

	if *cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			logger.Fatalf("locate default config path: %v", err)
		}
		*cfgPath = p
	}

	cfg, err := config.LoadOrCreate(*cfgPath)
	if errors.Is(err, config.ErrTemplateCreated) {
		logger.Printf("config template created at %s — edit it and rerun.", *cfgPath)
		os.Exit(2)
	}
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	orch := backup.New(cfg)
	orch.Log = logger
	res := orch.Run(ctx)

	logger.Printf("backup finished in %s (backup_ok=%v vacuum_ok=%v)", res.Duration, res.BackupOK, res.VacuumOK)
	for _, db := range res.Databases {
		if db.Err != nil {
			logger.Printf("  [FAIL] %s: %v", db.Database, db.Err)
		} else if db.Skipped {
			logger.Printf("  [SKIP] %s (already present, %d bytes)", db.Database, db.Size)
		} else {
			logger.Printf("  [ OK ] %s -> %s (%d bytes)", db.Database, db.File, db.Size)
		}
	}

	metrics := buildMetrics(cfg, res, time.Now())
	if *printOnly {
		for _, m := range metrics {
			fmt.Printf("metric: host=%s key=%s value=%s clock=%d\n", m.Host, m.Key, m.Value, m.Clock)
		}
	}

	if cfg.Zabbix.Enabled && !*dryRunZbx {
		addr := net.JoinHostPort(cfg.Zabbix.Server, strconv.Itoa(cfg.Zabbix.Port))
		sender := zabbix.New(addr, time.Duration(cfg.Zabbix.TimeoutSeconds)*time.Second)
		sendCtx, cancelSend := context.WithTimeout(ctx, time.Duration(cfg.Zabbix.TimeoutSeconds+2)*time.Second)
		defer cancelSend()
		resp, err := sender.Send(sendCtx, metrics)
		if err != nil {
			logger.Printf("zabbix send failed: %v", err)
		} else {
			logger.Printf("zabbix response: %s (%s)", resp.Response, resp.Info)
		}
	} else {
		logger.Printf("zabbix sending skipped (enabled=%v, --no-zabbix=%v)", cfg.Zabbix.Enabled, *dryRunZbx)
	}

	if !res.BackupOK {
		os.Exit(1)
	}
}

func buildMetrics(cfg *config.Config, res backup.Result, now time.Time) []zabbix.Metric {
	host := cfg.Zabbix.Host

	metrics := make([]zabbix.Metric, 0, len(res.Databases)+3)
	if res.BackupOK {
		metrics = append(metrics, zabbix.NewInt(host, "pg.backup.status", 1, now))
	} else {
		metrics = append(metrics, zabbix.NewInt(host, "pg.backup.status", 0, now))
	}
	metrics = append(metrics, zabbix.NewFloat(host, "pg.backup.duration", res.Duration.Seconds(), now))
	if res.VacuumOK {
		metrics = append(metrics, zabbix.NewInt(host, "pg.vacuum.status", 1, now))
	} else {
		metrics = append(metrics, zabbix.NewInt(host, "pg.vacuum.status", 0, now))
	}
	for _, db := range res.Databases {
		key := "pg.backup.size[" + db.Database + "]"
		metrics = append(metrics, zabbix.NewInt(host, key, db.Size, now))
	}
	return metrics
}
