package db

import "time"

// Gist represents a GitHub gist.
type Gist struct {
	ID          string `gorm:"primaryKey;size:32"` // random hex ID like GitHub
	OwnerID     uint   `gorm:"index"`
	Owner       User   `gorm:"foreignKey:OwnerID"`
	Description string `gorm:"type:text"`
	Public      bool   `gorm:"default:true"`
	Files       string `gorm:"type:text"` // JSON map of filename → {content, ...}
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
