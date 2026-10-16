// Package backgroundtask owns durable artifact work and fenced database leases.
package backgroundtask

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
)

var (
	ErrOutcomeUnknown   = errors.New("task commit outcome unknown; wait for lease recovery")
	ErrLeaseLost        = errors.New("task lease lost")
	ErrRecoveryConflict = errors.New("task is not failed at expected revision")
)

type Config struct {
	Concurrency   int
	ScanInterval  time.Duration
	LeaseDuration time.Duration
	RenewInterval time.Duration
	MaxFailures   int
}

func DefaultConfig() Config {
	return Config{
		Concurrency:   2,
		ScanInterval:  5 * time.Second,
		LeaseDuration: 60 * time.Second,
		RenewInterval: 15 * time.Second,
		MaxFailures:   20,
	}
}

func (c Config) Validate() error {
	if c.Concurrency < 1 || c.ScanInterval <= 0 || c.LeaseDuration <= 0 || c.RenewInterval <= 0 ||
		c.RenewInterval >= c.LeaseDuration ||
		c.MaxFailures < 1 {
		return errors.New("invalid worker configuration")
	}
	return nil
}

type Worker struct {
	database   *db.DB
	config     Config
	references storage.ReferenceService
}

func New(database *db.DB, config *Config) (*Worker, error) {
	c := DefaultConfig()
	if config != nil {
		c = *config
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Worker{database: database, config: c}, nil
}

func transaction(ctx context.Context, database *db.DB, fn func(*db.DB) error) error {
	return db.Transaction(ctx, database, db.TransactionOptions{Isolation: sql.LevelReadCommitted}, fn)
}

// Claim takes one due task. A crash or expired lease never consumes failure budget.
func (w *Worker) Claim(ctx context.Context) (*table.BackgroundTask, error) {
	var out *table.BackgroundTask
	err := transaction(ctx, w.database, func(tx *db.DB) error {
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		var row table.BackgroundTask
		err = tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("(state = ? AND next_run_at <= ? AND not_before <= ?) OR (state = ? AND lease_until <= ?)", "pending", now, now, "running", now).
			Order("next_run_at, id").
			First(&row).
			Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now, err = table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		until := now.Add(w.config.LeaseDuration)
		row.State = "running"
		row.LeaseToken = uuid.NewString()
		row.LeaseUntil = &until
		row.TotalAttempts++
		row.Revision++
		if err = tx.Save(&row).Error; err != nil {
			return err
		}
		out = &row
		return nil
	})
	return out, err
}

func lease(tx *db.DB, id, token string) (*table.BackgroundTask, time.Time, error) {
	var row table.BackgroundTask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
		return nil, time.Time{}, err
	}
	now, err := table.DatabaseNow(tx)
	if err != nil {
		return nil, now, err
	}
	if row.State != "running" || row.LeaseToken != token || row.LeaseUntil == nil || !now.Before(*row.LeaseUntil) {
		return nil, now, ErrLeaseLost
	}
	return &row, now, nil
}

func (w *Worker) Renew(ctx context.Context, id, token string) error {
	return transaction(ctx, w.database, func(tx *db.DB) error {
		row, now, err := lease(tx, id, token)
		if err != nil {
			return err
		}
		until := now.Add(w.config.LeaseDuration)
		row.LeaseUntil = &until
		row.Revision++
		return tx.Save(row).Error
	})
}

// Fail records only a confirmed execution failure, retaining immutable task payload.
func (w *Worker) Fail(ctx context.Context, id, token string, cause error) error {
	var kind string
	err := transaction(ctx, w.database, func(tx *db.DB) error {
		row, now, err := lease(tx, id, token)
		if err != nil {
			return err
		}
		kind = row.Kind
		row.Attempts++
		row.Revision++
		row.LeaseToken = ""
		row.LeaseUntil = nil
		row.LastError = cause.Error()
		if len(row.LastError) > 1024 {
			row.LastError = strings.ToValidUTF8(row.LastError[:1024], "")
		}
		row.State = "pending"
		if int(row.Attempts) >= w.config.MaxFailures {
			row.State = "failed"
		}
		shift := min(row.Attempts-1, 6)
		delay := min(time.Second*time.Duration(1<<shift), 60*time.Second)
		delay = time.Duration(float64(delay) * (0.5 + rand.Float64()*0.5))
		row.NextRunAt = now.Add(delay)
		if row.NextRunAt.Before(row.NotBefore) {
			row.NextRunAt = row.NotBefore
		}
		return tx.Save(row).Error
	})
	if err == nil {
		telemetry.TaskFailure("artifact", kind)
	}
	return err
}
