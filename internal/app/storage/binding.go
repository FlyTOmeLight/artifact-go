package storage

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	table "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
)

// BindingService owns storage-local reference transactions for one stable consumer.
type BindingService struct {
	database   *db.DB
	consumerID string
}

func NewBindingService(database *db.DB, consumerID string) *BindingService {
	return &BindingService{database: database, consumerID: consumerID}
}

var _ ReferenceService = (*BindingService)(nil)

func bindingError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fileError(404, "FileNotFound", "file not found")
	}
	var app *serrors.Error
	if errors.As(err, &app) {
		return err
	}
	return serrors.NewError(503, "StorageUnavailable", "storage database is unavailable")
}

func bindingIdentity(target Scope) scope.Identity {
	return scope.Identity{TenantID: target.TenantID, ProjectID: target.ProjectID, ResourceUserID: target.UserID}
}

func bindingFiles(tx *db.DB, target Scope, ids []string) (map[string]table.StorageFile, error) {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	files := make(map[string]table.StorageFile, len(ids))
	for _, id := range sorted {
		if _, ok := files[id]; ok {
			continue
		}
		row, err := readFile(tx, bindingIdentity(target), id, true)
		if err != nil {
			return nil, err
		}
		files[id] = row
	}
	return files, nil
}

func confirmation(row table.StorageFile) ConfirmationMetadata {
	out := response(row)
	return ConfirmationMetadata{
		Size:              out.Size,
		ContentType:       out.ContentType,
		DeclaredChecksum:  out.DeclaredChecksum,
		OriginalExpiresAt: out.ExpiresAt,
	}
}

func (s *BindingService) Metadata(
	ctx context.Context,
	target Scope,
	ids []string,
) ([]FileMetadata, error) {
	var result []FileMetadata
	err := db.Transaction(ctx, s.database, fileOptions(), func(tx *db.DB) error {
		files, err := bindingFiles(tx, target, ids)
		if err != nil {
			return err
		}
		result = make([]FileMetadata, len(ids))
		for n, id := range ids {
			row := files[id]
			m := confirmation(row)
			result[n] = FileMetadata{FileID: id, State: row.State, ConfirmationMetadata: m}
		}
		return nil
	})
	if err != nil {
		return nil, bindingError(err)
	}
	return result, nil
}

func findReference(
	tx *db.DB,
	consumer string,
	ref Reference,
	target Scope,
) (table.StorageReference, error) {
	var row table.StorageReference
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("consumer_id = ? AND reference_id = ?", consumer, ref.ReferenceID).
		First(&row).
		Error
	if err != nil {
		return row, err
	}
	if row.FileID != ref.FileID || row.TenantID != target.TenantID ||
		row.ScopeKey != bindingIdentity(target).Key() {
		return row, fileError(409, "ReferenceConflict", "reference has a different immutable association")
	}
	return row, nil
}

func sortedReferences(refs []Reference) []Reference {
	out := append([]Reference(nil), refs...)
	sort.Slice(out, func(i, j int) bool { return out[i].ReferenceID < out[j].ReferenceID })
	return out
}

func referenceIDs(refs []Reference) []string {
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.FileID
	}
	return ids
}

func activeReferences(tx *db.DB, id string) (int64, error) {
	var count int64
	err := tx.Model(&table.StorageReference{}).Where("file_id = ? AND state = ?", id, "active").Count(&count).Error
	return count, err
}

func createReference(
	tx *db.DB,
	consumer string,
	target Scope,
	ref Reference,
	state string,
	now time.Time,
) (table.StorageReference, error) {
	row := table.StorageReference{
		ConsumerID:  consumer,
		ReferenceID: ref.ReferenceID,
		FileID:      ref.FileID,
		TenantID:    target.TenantID,
		ScopeKey:    bindingIdentity(target).Key(),
		State:       state,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// A different file may race for this consumer/reference identity. The unique key
	// decides ownership; re-reading validates the immutable association.
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return row, err
	}
	return findReference(tx, consumer, ref, target)
}

func (s *BindingService) Bind(
	ctx context.Context,
	target Scope,
	refs []Reference,
) ([]BindResult, error) {
	var result []BindResult
	completed := false
	err := db.Transaction(ctx, s.database, fileOptions(), func(tx *db.DB) error {
		completed = false
		files, err := bindingFiles(tx, target, referenceIDs(refs))
		if err != nil {
			return err
		}
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		for _, ref := range sortedReferences(refs) {
			row, e := findReference(tx, s.consumerID, ref, target)
			if e == nil {
				if row.State == "released" {
					return fileError(409, "ReferenceReleased", "released reference cannot be bound")
				}
				continue
			}
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			file := files[ref.FileID]
			bindable := file.State == "ready" && file.ExpiresAt.After(now)
			if file.State == "bound" {
				count, e := activeReferences(tx, file.ID)
				if e != nil {
					return e
				}
				bindable = count > 0
			}
			if !bindable {
				return fileError(409, "FileNotBindable", "file cannot accept a new reference")
			}
			row, e = createReference(tx, s.consumerID, target, ref, "active", now)
			if e != nil {
				return e
			}
			if row.State == "released" {
				return fileError(409, "ReferenceReleased", "released reference cannot be bound")
			}
			if file.State != "bound" {
				if e = tx.Model(&table.StorageFile{}).
					Where("id = ?", file.ID).
					Update("state", "bound").
					Error; e != nil {
					return e
				}
				file.State = "bound"
				files[file.ID] = file
			}
		}
		result = make([]BindResult, len(refs))
		for n, r := range refs {
			result[n] = BindResult{
				ReferenceID: r.ReferenceID,
				FileID:      r.FileID,
				State:       "active",
				File:        confirmation(files[r.FileID]),
			}
		}
		completed = true
		return nil
	})
	if err != nil {
		if completed {
			return nil, fileError(503, "StorageOutcomeUnknown", "binding commit could not be confirmed")
		}
		return nil, bindingError(err)
	}
	return result, nil
}

func referenceStatus(ref Reference, row table.StorageReference, file table.StorageFile) Status {
	out := Status{ReferenceID: ref.ReferenceID, FileID: ref.FileID, State: row.State}
	if row.State == "released" {
		required := row.ReleaseRequiresDeletion
		out.ReleaseRequiresDeletion = &required
		out.ReleaseComplete = !required || file.DeletedAt != nil
	}
	return out
}

func (s *BindingService) Release(
	ctx context.Context,
	target Scope,
	refs []Reference,
) ([]Status, error) {
	var result []Status
	completed := false
	err := db.Transaction(ctx, s.database, fileOptions(), func(tx *db.DB) error {
		completed = false
		files, err := bindingFiles(tx, target, referenceIDs(refs))
		if err != nil {
			return err
		}
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		rows := make(map[string]table.StorageReference, len(refs))
		for _, ref := range sortedReferences(refs) {
			row, e := findReference(tx, s.consumerID, ref, target)
			if errors.Is(e, gorm.ErrRecordNotFound) {
				row, e = createReference(tx, s.consumerID, target, ref, "released", now)
			}
			if e != nil {
				return e
			}
			if row.State == "active" {
				row.State = "released"
				row.UpdatedAt = now
				if e = tx.Save(&row).Error; e != nil {
					return e
				}
				count, e := activeReferences(tx, ref.FileID)
				if e != nil {
					return e
				}
				if count == 0 {
					row.ReleaseRequiresDeletion = true
					if e = tx.Save(&row).Error; e != nil {
						return e
					}
					file := files[ref.FileID]
					// The file is locked and its final active reference has just been released.
					if e = MarkDeleting(tx, &file); e != nil {
						return e
					}
					files[file.ID] = file
				}
			}
			rows[ref.ReferenceID] = row
		}
		result = make([]Status, len(refs))
		for n, r := range refs {
			result[n] = referenceStatus(r, rows[r.ReferenceID], files[r.FileID])
		}
		completed = true
		return nil
	})
	if err != nil {
		if completed {
			return nil, fileError(503, "StorageOutcomeUnknown", "release commit could not be confirmed")
		}
		return nil, bindingError(err)
	}
	return result, nil
}

func (s *BindingService) Statuses(
	ctx context.Context,
	target Scope,
	refs []Reference,
) ([]Status, error) {
	var result []Status
	err := db.Transaction(ctx, s.database, fileOptions(), func(tx *db.DB) error {
		files, err := bindingFiles(tx, target, referenceIDs(refs))
		if err != nil {
			return err
		}
		rows := make(map[string]table.StorageReference, len(refs))
		for _, ref := range sortedReferences(refs) {
			row, e := findReference(tx, s.consumerID, ref, target)
			if errors.Is(e, gorm.ErrRecordNotFound) {
				row.State = "absent"
			} else if e != nil {
				return e
			}
			rows[ref.ReferenceID] = row
		}
		result = make([]Status, len(refs))
		for n, r := range refs {
			result[n] = referenceStatus(r, rows[r.ReferenceID], files[r.FileID])
		}
		return nil
	})
	if err != nil {
		return nil, bindingError(err)
	}
	return result, nil
}
