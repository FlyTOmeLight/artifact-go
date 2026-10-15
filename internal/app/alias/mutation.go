// Package alias owns conditional references to published versions.
package alias

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func invalid(message string) error { return serrors.NewError(400, "InvalidAliasRequest", message) }

func conflict() error {
	return serrors.NewError(409, "RevisionConflict", "alias revision does not match")
}

func notFound() error { return serrors.NewError(404, "AliasNotFound", "alias not found") }

// NormalizeName applies the same canonical name for direct and publish operations.
func NormalizeName(name string) (string, error) {
	for _, r := range name {
		if r > 127 {
			return "", invalid("alias name must contain only ASCII characters")
		}
	}
	name = strings.ToLower(name)
	if !namePattern.MatchString(name) {
		return "", invalid("alias name must be an ASCII identifier of at most 64 characters")
	}
	return name, nil
}

// PreparePut validates CAS without requiring the not-yet-inserted publish target.
// The caller must hold the owning Artifact row lock through ApplyPut and commit.
func PreparePut(tx *db.DB, artifactID, name string, expectedRevision int64) (*table.Alias, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return nil, err
	}
	if name == "latest" {
		return nil, invalid("latest is read-only")
	}
	if expectedRevision < 0 {
		return nil, invalid("expectedRevision must be nonnegative")
	}
	var row table.Alias
	err = tx.Where("artifact_id = ? AND name = ?", artifactID, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if expectedRevision != 0 {
			return nil, conflict()
		}
		return &table.Alias{ArtifactID: artifactID, Name: name}, nil
	}
	if err != nil {
		return nil, err
	}
	if row.Revision != expectedRevision {
		return nil, conflict()
	}
	if row.Revision == math.MaxInt64 {
		return nil, serrors.NewError(409, "RevisionExhausted", "alias revision exhausted")
	}
	return &row, nil
}

// ApplyPut rechecks the revision and the ready target using the caller's transaction.
func ApplyPut(tx *db.DB, prepared *table.Alias, versionID string) (*table.Alias, error) {
	row, err := PreparePut(tx, prepared.ArtifactID, prepared.Name, prepared.Revision)
	if err != nil {
		return nil, err
	}
	var target table.Version
	if err = tx.Select("id").
		Where("artifact_id = ? AND id = ? AND state = ?", row.ArtifactID, versionID, "ready").
		First(&target).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, serrors.NewError(404, "VersionNotFound", "ready version not found in artifact")
		}
		return nil, err
	}
	row.VersionID = &versionID
	row.DeletedAt = nil
	row.Revision++
	if row.Revision == 1 {
		err = tx.Create(row).Error
	} else {
		err = tx.Model(row).
			Updates(map[string]any{"version_id": versionID, "deleted_at": nil, "revision": row.Revision}).
			Error
	}
	return row, err
}

// Delete marks a live alias deleted in the caller's locked artifact transaction.
func Delete(tx *db.DB, artifactID, name string, expectedRevision int64) (*table.Alias, error) {
	row, err := PreparePut(tx, artifactID, name, expectedRevision)
	if err != nil {
		return nil, err
	}
	if row.Revision == 0 || row.VersionID == nil {
		return nil, notFound()
	}
	now := time.Now().UTC()
	row.VersionID = nil
	row.DeletedAt = &now
	row.Revision++
	err = tx.Model(row).
		Updates(map[string]any{"version_id": nil, "deleted_at": now, "revision": row.Revision}).
		Error
	return row, err
}
