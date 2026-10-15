package table

import (
	"context"

	"github.com/FlyTOmeLight/server-go/db"
)

// Migrate installs only models implemented by the current delivery slice.
func Migrate(ctx context.Context, database *db.DB) error {
	migration := database.WithContext(ctx)
	// IDs are case-sensitive, including tenant/project IDs supplied by the resolver.
	// NO PAD collation also preserves opaque request keys ending with spaces.
	if database.Dialector.Name() == "mysql" {
		migration = migration.Set(
			"gorm:table_options",
			"ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin",
		)
	}
	if err := migration.AutoMigrate(
		&Artifact{},
		&Version{},
		&VersionFile{},
		&CreateRequest{},
		&Alias{},
		&RetentionPolicy{},
		&BackgroundTask{},
		&Draft{}, &DraftFile{}, &DraftCreateRequest{}, &ReferenceChange{},
	); err != nil {
		return err
	}
	// server-go disables foreign keys during AutoMigrate; install our owner
	// constraints explicitly so a receipt cannot reference another collection.
	for _, constraint := range []struct {
		model    any
		relation string
	}{
		{model: &Alias{}, relation: "Artifact"},
		{model: &Draft{}, relation: "Artifact"},
		{model: &DraftFile{}, relation: "Draft"},
		{model: &DraftCreateRequest{}, relation: "Artifact"},
		{model: &ReferenceChange{}, relation: "Artifact"},
		{model: &RetentionPolicy{}, relation: "Artifact"},
		{model: &Alias{}, relation: "TargetVersion"},
		{model: &Version{}, relation: "Artifact"},
		{model: &VersionFile{}, relation: "Version"},
		{model: &CreateRequest{}, relation: "Version"},
	} {
		if !migration.Migrator().HasConstraint(constraint.model, constraint.relation) {
			if err := migration.Migrator().
				CreateConstraint(constraint.model, constraint.relation); err != nil {
				return err
			}
		}
	}
	return nil
}
