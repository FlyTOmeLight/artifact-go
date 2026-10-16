package table

import (
	"context"

	"github.com/FlyTOmeLight/server-go/db"
)

// Migrate installs the storage domain independently from artifact tables.
func Migrate(ctx context.Context, database *db.DB) error {
	migration := database.WithContext(ctx)
	if database.Dialector.Name() == "mysql" {
		migration = migration.Set(
			"gorm:table_options",
			"ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin",
		)
	}
	return migration.AutoMigrate(&StorageFile{}, &StorageTask{}, &StorageReference{})
}
