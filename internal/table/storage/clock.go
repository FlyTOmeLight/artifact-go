package table

import (
	"time"

	"github.com/FlyTOmeLight/server-go/db"
)

// DatabaseNow reads wall time after the caller has obtained its required locks.
func DatabaseNow(tx *db.DB) (time.Time, error) {
	query := "SELECT clock_timestamp()"
	if tx.Dialector.Name() == "mysql" {
		query = "SELECT UTC_TIMESTAMP(6)"
	}
	var now time.Time
	err := tx.Raw(query).Scan(&now).Error
	return now.UTC(), err
}
