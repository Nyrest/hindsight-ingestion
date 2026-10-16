package models

import "time"

type SteamSchema struct {
	AppID       uint32 `gorm:"primaryKey;autoIncrement:false"`
	Language    string `gorm:"primaryKey;size:32"`
	PayloadJSON string
	UpdatedAt   time.Time
}

type SteamPlayerSnapshot struct {
	TaskID        string `gorm:"primaryKey;size:36"`
	CredentialKey string `gorm:"primaryKey;size:64"`
	SteamID       string `gorm:"primaryKey;size:20"`
	AppID         uint32 `gorm:"primaryKey;autoIncrement:false"`
	Language      string `gorm:"primaryKey;size:32"`
	PayloadJSON   string
	UpdatedAt     time.Time
}

type SteamAPIUsage struct {
	KeyHash  string `gorm:"primaryKey;size:64"`
	Day      string `gorm:"primaryKey;size:10"`
	Requests int64  `gorm:"not null;default:0"`
}
