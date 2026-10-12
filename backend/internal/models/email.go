package models

// Email snapshots contain immutable headers and MIME structure, never message bodies.
type EmailIdentity struct {
	TaskID             string `gorm:"primaryKey;size:36"`
	ItemID             string `gorm:"primaryKey;size:96"`
	AccountKey         string `gorm:"size:64;not null;index"`
	MessageIDHash      string `gorm:"size:64;index"`
	ContentFingerprint string `gorm:"size:64"`
	SnapshotJSON       string `gorm:"type:text;not null"`
}

type EmailOccurrence struct {
	TaskID      string `gorm:"primaryKey;size:36;index:idx_email_item,priority:1"`
	AccountKey  string `gorm:"primaryKey;size:64"`
	FolderKey   string `gorm:"primaryKey;size:64"`
	UIDValidity uint64 `gorm:"primaryKey"`
	UID         uint64 `gorm:"primaryKey"`
	FolderName  string `gorm:"type:text;not null"`
	ItemID      string `gorm:"size:96;not null;index:idx_email_item,priority:2"`
	PartsJSON   string `gorm:"type:text;not null"`
}

type EmailScanIdentity struct {
	RunID              string `gorm:"primaryKey;size:36"`
	TaskID             string `gorm:"size:36;not null;index"`
	ItemID             string `gorm:"primaryKey;size:96"`
	AccountKey         string `gorm:"size:64;not null"`
	MessageIDHash      string `gorm:"size:64"`
	ContentFingerprint string `gorm:"size:64"`
	SnapshotJSON       string `gorm:"type:text;not null"`
}

type EmailScanOccurrence struct {
	RunID       string `gorm:"primaryKey;size:36"`
	TaskID      string `gorm:"size:36;not null;index"`
	AccountKey  string `gorm:"primaryKey;size:64"`
	FolderKey   string `gorm:"primaryKey;size:64"`
	UIDValidity uint64 `gorm:"primaryKey"`
	UID         uint64 `gorm:"primaryKey"`
	FolderName  string `gorm:"type:text;not null"`
	ItemID      string `gorm:"size:96;not null"`
	PartsJSON   string `gorm:"type:text;not null"`
}
