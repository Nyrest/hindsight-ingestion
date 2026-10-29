package models

type GooglePeopleIdentity struct {
	TaskID       string `gorm:"primaryKey;size:36;index:idx_people_identity,priority:1"`
	ResourceName string `gorm:"primaryKey;size:512"`
	ItemID       string `gorm:"size:128;not null;index:idx_people_identity,priority:2"`
	Subject      string `gorm:"size:255;not null"`
}

type GooglePeopleScanAlias struct {
	RunID        string `gorm:"primaryKey;size:36"`
	ResourceName string `gorm:"primaryKey;size:512"`
	TaskID       string `gorm:"size:36;not null;index"`
	ItemID       string `gorm:"size:128;not null"`
	Subject      string `gorm:"size:255;not null"`
}

// Contact payloads are temporary and are removed on commit, abort or recovery.
type GooglePeopleScanContact struct {
	RunID        string `gorm:"primaryKey;size:36"`
	ItemID       string `gorm:"primaryKey;size:128"`
	TaskID       string `gorm:"size:36;not null;index"`
	SnapshotJSON string `gorm:"type:text;not null"`
}
