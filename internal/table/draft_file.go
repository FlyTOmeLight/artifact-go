package table

type DraftFile struct {
	DraftID          string `gorm:"size:36;primaryKey;uniqueIndex:uk_draft_file_id,priority:1"`
	LogicalPath      string `gorm:"size:512;primaryKey"`
	FileID           string `gorm:"size:36;not null;uniqueIndex:uk_draft_file_id,priority:2"`
	ReferenceID      string `gorm:"size:36;not null;uniqueIndex:uk_draft_reference"`
	Size             int64  `gorm:"not null"`
	ContentType      string `gorm:"size:256;not null"`
	DeclaredChecksum []byte
	Draft            *Draft `gorm:"foreignKey:DraftID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
