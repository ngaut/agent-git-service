package service

import "gorm.io/gorm"

// Preload chain helpers reduce duplication across service methods that
// need consistent association loading. Each helper applies the same set
// of Preload calls that its callers previously inlined.

func preloadIssue(q *gorm.DB) *gorm.DB {
	return q.Preload("Author").Preload("Repository").Preload("Repository.Owner").Preload("Labels").
		Preload("Milestone").Preload("Milestone.Creator")
}

func preloadIssueForRESTList(q *gorm.DB) *gorm.DB {
	return q.Preload("Author").Preload("Labels").Preload("Milestone").Preload("Milestone.Creator")
}

func preloadPRFull(q *gorm.DB) *gorm.DB {
	return q.Preload("Author").Preload("Repository").Preload("Repository.Owner").
		Preload("HeadRepository").Preload("HeadRepository.Owner").Preload("Labels").
		Preload("Milestone").Preload("Milestone.Creator")
}

func preloadPRForRESTIssueList(q *gorm.DB) *gorm.DB {
	return q.Preload("Author").Preload("Labels").Preload("Milestone").Preload("Milestone.Creator")
}

func preloadRelease(q *gorm.DB) *gorm.DB {
	return q.Preload("Author").Preload("Repository").Preload("Repository.Owner").Preload("Assets")
}

func preloadRepoFull(q *gorm.DB) *gorm.DB {
	return q.Preload("Owner").Preload("Parent").Preload("Parent.Owner").Preload("Labels")
}

func preloadRepoMinimal(q *gorm.DB) *gorm.DB {
	return q.Preload("Owner").Preload("Parent").Preload("Parent.Owner")
}

func preloadIssueComment(q *gorm.DB) *gorm.DB {
	return q.Preload("Author").Preload("Repository").Preload("Repository.Owner")
}

func preloadMilestone(q *gorm.DB) *gorm.DB {
	return q.Preload("Creator").Preload("Repository").Preload("Repository.Owner")
}
