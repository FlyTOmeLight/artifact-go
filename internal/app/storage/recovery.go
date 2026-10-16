package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"gorm.io/gorm/clause"

	table "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
)

type RecoveryRequest struct {
	TaskID                   string
	ExpectedRevision         int64
	Operator, Reason, Record string
}

type RecoveryEvent struct {
	Operator           string    `json:"operator"`
	Reason             string    `json:"reason"`
	Record             string    `json:"record"`
	At                 time.Time `json:"at"`
	PreviousRevision   int64     `json:"previousRevision"`
	PreviousGeneration int64     `json:"previousGeneration"`
	PreviousFailures   int32     `json:"previousFailures"`
	PreviousError      string    `json:"previousError"`
	From               string    `json:"from"`
	To                 string    `json:"to"`
}

type RecoveryPreview struct {
	TaskID, Kind, State, LastError, PayloadSHA256, LeaseToken string
	Revision, RunGeneration, TotalAttempts                    int64
	Attempts                                                  int32
	LeaseUntil                                                *time.Time
	NotBefore                                                 time.Time
	Affected                                                  int
}

// Recover supports dry-run and a revision-fenced, transactionally audited requeue.
func RecoverTask(
	ctx context.Context,
	database *db.DB,
	request RecoveryRequest,
	execute bool,
) (*RecoveryPreview, error) {
	if request.TaskID == "" || request.ExpectedRevision <= 0 || strings.TrimSpace(request.Operator) == "" ||
		strings.TrimSpace(request.Reason) == "" ||
		strings.TrimSpace(request.Record) == "" {
		return nil, errors.New("task ID, revision, operator, reason and record are required")
	}
	var preview *RecoveryPreview
	completed := false
	err := workerTransaction(ctx, database, func(tx *db.DB) error {
		var row table.StorageTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&row, "id = ?", request.TaskID).
			Error; err != nil {
			return err
		}
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		if row.State != "failed" || row.Revision != request.ExpectedRevision ||
			(row.LeaseUntil != nil && now.Before(*row.LeaseUntil)) {
			return ErrRecoveryConflict
		}
		hash := sha256.Sum256(row.Payload)
		preview = &RecoveryPreview{
			TaskID:        row.ID,
			Kind:          row.Kind,
			State:         row.State,
			LastError:     row.LastError,
			PayloadSHA256: hex.EncodeToString(hash[:]),
			LeaseToken:    row.LeaseToken,
			Revision:      row.Revision,
			RunGeneration: row.RunGeneration,
			TotalAttempts: row.TotalAttempts,
			Attempts:      row.Attempts,
			LeaseUntil:    row.LeaseUntil,
			NotBefore:     row.NotBefore,
			Affected:      1,
		}
		if !execute {
			return nil
		}
		var events []RecoveryEvent
		if err = json.Unmarshal(row.RecoveryEvents, &events); err != nil {
			return err
		}
		events = append(
			events,
			RecoveryEvent{
				Operator:           request.Operator,
				Reason:             request.Reason,
				Record:             request.Record,
				At:                 now,
				PreviousRevision:   row.Revision,
				PreviousGeneration: row.RunGeneration,
				PreviousFailures:   row.Attempts,
				PreviousError:      row.LastError,
				From:               "failed",
				To:                 "pending",
			},
		)
		row.RecoveryEvents, err = json.Marshal(events)
		if err != nil {
			return err
		}
		row.RunGeneration++
		row.Revision++
		row.Attempts = 0
		row.State = "pending"
		row.NextRunAt = now
		if now.Before(row.NotBefore) {
			row.NextRunAt = row.NotBefore
		}
		row.LeaseToken = ""
		row.LeaseUntil = nil
		if err = tx.Save(&row).Error; err != nil {
			return err
		}
		completed = true
		return nil
	})
	if err != nil && completed {
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var current table.StorageTask
		if readErr := database.WithContext(checkCtx).
			First(&current, "id = ?", request.TaskID).
			Error; readErr == nil {
			var events []RecoveryEvent
			if json.Unmarshal(current.RecoveryEvents, &events) == nil {
				for _, event := range events {
					if event.PreviousRevision == request.ExpectedRevision &&
						event.Operator == request.Operator &&
						event.Reason == request.Reason &&
						event.Record == request.Record {
						return preview, nil
					}
				}
			}
		}
		return preview, errors.New(
			"OperationOutcomeUnknown: inspect task revision and recovery events on the primary database before retrying",
		)
	}
	return preview, err
}
