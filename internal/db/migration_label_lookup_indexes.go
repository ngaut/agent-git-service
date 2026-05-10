package db

import "gorm.io/gorm"

type issueLabelLookupIndex struct {
	IssueID uint `gorm:"column:issue_id;index:idx_issue_labels_label_issue,priority:2"`
	LabelID uint `gorm:"column:label_id;index:idx_issue_labels_label_issue,priority:1"`
}

func (issueLabelLookupIndex) TableName() string { return "issue_labels" }

type prLabelLookupIndex struct {
	PullRequestID uint `gorm:"column:pull_request_id;index:idx_pr_labels_label_pr,priority:2"`
	LabelID       uint `gorm:"column:label_id;index:idx_pr_labels_label_pr,priority:1"`
}

func (prLabelLookupIndex) TableName() string { return "pr_labels" }

// MigrateLabelLookupIndexes adds reverse lookup indexes for label join tables so
// label-filtered issue/PR listings can probe by label_id efficiently.
func MigrateLabelLookupIndexes(database *gorm.DB) error {
	return database.AutoMigrate(&issueLabelLookupIndex{}, &prLabelLookupIndex{})
}
