package table

// VersionFile is the immutable logical manifest and its independently held reference.
type VersionFile struct {
	VersionID        string `gorm:"size:36;primaryKey;uniqueIndex:uk_version_file_id,priority:1"`
	LogicalPath      string `gorm:"size:512;primaryKey"`
	FileID           string `gorm:"size:36;not null;uniqueIndex:uk_version_file_id,priority:2"`
	ReferenceID      string `gorm:"size:36;not null;uniqueIndex"`
	Size             int64  `gorm:"not null"`
	ContentType      string `gorm:"size:256;not null"`
	DeclaredChecksum []byte
	Version          *Version `gorm:"foreignKey:VersionID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
