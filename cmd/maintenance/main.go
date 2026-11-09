// Command maintenance requeues failed artifact tasks with a durable audit record.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/logger"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/doctor"
	"github.com/FlyTOmeLight/artifact-go/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	action := flag.String("action", "recover", "maintenance action (recover or doctor)")
	domain := flag.String("domain", "artifact", "task domain (artifact or storage)")
	configPath := flag.String("db-config", "", "path to private JSON db.Config")
	task := flag.String("task-id", "", "failed task ID")
	revision := flag.Int64("expected-revision", 0, "revision from inspection")
	operator := flag.String("operator", "", "operator identity")
	reason := flag.String("reason", "", "recovery reason")
	record := flag.String("record", "", "ticket or operation record")
	execute := flag.Bool("execute", false, "apply requeue; omitted means dry-run")
	limit := flag.Int("limit", doctor.DefaultLimit, "maximum candidate rows per doctor check (1-10000)")
	flag.Parse()
	if *action != "recover" && *action != "doctor" {
		return fmt.Errorf("unsupported maintenance action %q", *action)
	}
	if *action == "doctor" {
		if *execute {
			return errors.New("doctor is read-only; --execute is not supported")
		}
		if *limit < 1 || *limit > doctor.MaxLimit {
			return fmt.Errorf("doctor limit must be between 1 and %d", doctor.MaxLimit)
		}
	}
	if *domain != "artifact" && *domain != "storage" {
		return fmt.Errorf("unsupported task domain %q", *domain)
	}
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		return err
	}
	var cfg db.Config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.Driver != db.DriverPostgreSQL && cfg.Driver != db.DriverMySQL {
		return errors.New("unsupported database driver")
	}
	if cfg.Driver == db.DriverMySQL && cfg.DSNParams == "" {
		cfg.DSNParams = "charset=utf8mb4&parseTime=true&loc=UTC&interpolateParams=true"
	}
	cfg.AutoMigrate = false
	if err = (service.Config{DB: cfg}).Validate(); err != nil {
		return err
	}
	log, err := logger.NewLogger(&logger.Config{Level: "error", Outputs: []logger.Output{{Type: "console"}}})
	if err != nil {
		return err
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(logger.WithContext(context.Background(), log), 30*time.Second)
	defer cancel()
	database, err := db.NewDB(ctx, &cfg)
	if err != nil {
		return err
	}
	sqlDB, err := database.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if *action == "doctor" {
		report, err := doctor.Run(ctx, database, *domain, *limit)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if *domain == "storage" {
		result, err := storage.RecoverTask(
			ctx,
			database,
			storage.RecoveryRequest{
				TaskID:           *task,
				ExpectedRevision: *revision,
				Operator:         *operator,
				Reason:           *reason,
				Record:           *record,
			},
			*execute,
		)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Applied bool
			Before  *storage.RecoveryPreview
		}{Applied: *execute, Before: result})
	}
	worker, err := backgroundtask.New(database, nil)
	if err != nil {
		return err
	}
	result, err := worker.Recover(
		ctx,
		backgroundtask.RecoveryRequest{
			TaskID:           *task,
			ExpectedRevision: *revision,
			Operator:         *operator,
			Reason:           *reason,
			Record:           *record,
		},
		*execute,
	)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Applied bool
		Before  *backgroundtask.RecoveryPreview
	}{Applied: *execute, Before: result})
}
