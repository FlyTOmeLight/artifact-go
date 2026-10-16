package table

import "time"

type StorageTask struct {
	ID             string    `gorm:"size:36;primaryKey"`
	Kind           string    `gorm:"size:32;not null;uniqueIndex:uk_storage_task_target,priority:1"`
	TargetID       string    `gorm:"size:36;not null;uniqueIndex:uk_storage_task_target,priority:2"`
	Payload        []byte    `gorm:"not null"`
	State          string    `gorm:"size:16;not null;index:idx_storage_task_due,priority:1;index:idx_storage_task_lease,priority:1"`
	NextRunAt      time.Time `gorm:"index:idx_storage_task_due,priority:2"`
	NotBefore      time.Time
	LeaseUntil     *time.Time `gorm:"index:idx_storage_task_lease,priority:2"`
	LeaseToken     string     `gorm:"size:36;not null"`
	Attempts       int32      `gorm:"not null"`
	TotalAttempts  int64      `gorm:"not null"`
	RunGeneration  int64      `gorm:"not null"`
	Revision       int64      `gorm:"not null"`
	RecoveryEvents []byte     `gorm:"not null"`
	LastError      string     `gorm:"type:text"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
