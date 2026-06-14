package db

import "time"

// WikiPage is the catalog row for the current state of one wiki page.
// See docs/design/wiki-storage-rearchitecture.md §6.1.
//
// PageID is the stable identity (preserved across rename). Slug is the
// single lookup and display slug returned to clients. HeadBlobSHA backs
// the REST If-Match / ETag contract — it is the git SHA-1 hash of the
// page body (hex-encoded), matching what the legacy code returned.
//
// HeadBlobSHA, BodySize, and BodyInline duplicate fields that exist
// on the head WikiPageRevision row (the one keyed by
// (PageID, HeadRevisionID)). The duplication is intentional: list and
// HEAD-read paths are by far the dominant traffic on this table and
// must serve from a single row without a JOIN to wiki_page_revisions.
// applyChange writes both rows inside the same transaction so they
// can never drift; the only consumer that re-reads the revision row
// is the historical ?ref=<sha> path.
type WikiPage struct {
	PageID          uint64     `gorm:"primaryKey;autoIncrement"`
	RepositoryID    uint       `gorm:"not null;uniqueIndex:idx_wiki_pages_repo_slug,priority:1;index:idx_wiki_pages_repo_updated,priority:1;index:idx_wiki_pages_repo_prefix,priority:1"`
	Repository      Repository `gorm:"foreignKey:RepositoryID"`
	Slug            string     `gorm:"type:varbinary(255);not null;uniqueIndex:idx_wiki_pages_repo_slug,priority:2;index:idx_wiki_pages_repo_prefix,priority:2"`
	Title           string     `gorm:"type:varbinary(1024)"`
	HeadBlobSHA     string     `gorm:"type:char(40);not null"`
	BodySize        int        `gorm:"not null"`
	BodyInline      []byte     // present iff BodySize <= MaxBodyInlineBytes
	HeadRevisionID  uint64     `gorm:"not null"`
	HeadChangesetID uint64     `gorm:"not null"`
	LastAuthorID    *uint
	LastAuthor      *User `gorm:"foreignKey:LastAuthorID"`
	CreatedAt       time.Time
	UpdatedAt       time.Time  `gorm:"index:idx_wiki_pages_repo_updated,priority:2,sort:desc"`
	DeletedAt       *time.Time `gorm:"index"`
}

// TableName keeps the catalog tables in a wiki_-prefixed namespace
// regardless of GORM pluralization rules.
func (WikiPage) TableName() string { return "wiki_pages" }

// WikiPageRevision is one immutable version of a page. See §6.2.
// Each row is keyed by (PageID, RevisionID DESC) so that retrieving
// the most recent revision for a page is a prefix scan. CommitSHA is
// the changeset's immutable commit identity, exposed today by the
// REST history and move responses. idx_wiki_revisions_page_commit
// covers `GetWikiPage?ref=<sha>` lookups with (page_id, commit_sha).
type WikiPageRevision struct {
	PageID                  uint64  `gorm:"primaryKey;autoIncrement:false;index:idx_wiki_revisions_page_commit,priority:1"`
	RevisionID              uint64  `gorm:"primaryKey;autoIncrement:false"`
	ChangesetID             uint64  `gorm:"not null;index:idx_wiki_revisions_changeset"`
	SupersededByChangesetID *uint64 `gorm:"index:idx_wiki_revisions_superseded"`
	BlobSHA                 string  `gorm:"type:char(40)"` // empty for delete rows
	BodySize                int
	BodyInline              []byte // present iff BodySize <= MaxBodyInlineBytes
	SlugAtRev               string `gorm:"type:varbinary(1024);not null"`
	CommitSHA               string `gorm:"type:char(40);not null;index:idx_wiki_revisions_page_commit,priority:2"`
	Op                      string `gorm:"type:char(16);not null"` // create|update|rename|delete|restore|compact
	AuthorID                *uint
	Author                  *User     `gorm:"foreignKey:AuthorID"`
	CommittedAt             time.Time `gorm:"not null"`
}

func (WikiPageRevision) TableName() string { return "wiki_page_revisions" }

// WikiChangeset is the cross-page atomic group. See §6.3. ParentID
// supports the ff-only CAS chain; SynthCommitSHA is the public commit
// identity surfaced through the REST contract and through any future
// git façade.
type WikiChangeset struct {
	ChangesetID             uint64     `gorm:"primaryKey;autoIncrement"`
	RepositoryID            uint       `gorm:"not null;index:idx_wiki_changesets_repo,sort:desc"`
	Repository              Repository `gorm:"foreignKey:RepositoryID"`
	ParentID                *uint64    `gorm:"index:idx_wiki_changesets_parent"`
	SupersededByChangesetID *uint64    `gorm:"index:idx_wiki_changesets_superseded"`
	Message                 LargeText
	AuthorID                *uint
	Author                  *User     `gorm:"foreignKey:AuthorID"`
	CommittedAt             time.Time `gorm:"not null"`
	PageCount               int       `gorm:"not null"`
	Source                  string    `gorm:"type:char(16);not null"` // rest|admin|batch|compact|push|migration
	SynthCommitSHA          string    `gorm:"type:char(40);not null"`
	SynthFormatVer          int16
}

func (WikiChangeset) TableName() string { return "wiki_changesets" }

// WikiRepoHead is the single-row-per-repo serialization point. Every
// changeset commit updates this row under CAS; this replaces the
// in-process per-repo mutex. See §6.3.
type WikiRepoHead struct {
	RepositoryID    uint       `gorm:"primaryKey;autoIncrement:false"`
	Repository      Repository `gorm:"foreignKey:RepositoryID"`
	HeadChangesetID uint64     `gorm:"not null"`
	UpdatedAt       time.Time
}

func (WikiRepoHead) TableName() string { return "wiki_repo_heads" }

// WikiDirIndex is the directory view of the catalog maintained by
// ApplyChangeSet on every mutation. It powers ListWikiPages(path,
// recursive=false), prefix-collision detection, and future tree
// synthesis without ever scanning the page table. See §6.4.
type WikiDirIndex struct {
	RepositoryID uint    `gorm:"primaryKey;autoIncrement:false;index:idx_wiki_dir_repo_parent_kind_name,priority:1"`
	ParentDir    string  `gorm:"primaryKey;type:varbinary(1024);index:idx_wiki_dir_repo_parent_kind_name,priority:2"` // "" = root
	ChildName    string  `gorm:"primaryKey;type:varbinary(255);index:idx_wiki_dir_repo_parent_kind_name,priority:4"`
	ChildKind    string  `gorm:"type:char(8);not null;index:idx_wiki_dir_repo_parent_kind_name,priority:3,sort:desc"` // blob|tree
	PageID       *uint64 // present iff ChildKind == blob
}

func (WikiDirIndex) TableName() string { return "wiki_dir_index" }

// WikiPageLink is one outbound markdown link from src_page_id to
// either a resolved page_id (intra-wiki link) or a still-textual slug
// (dangling / pending). See §6.5.
type WikiPageLink struct {
	RepositoryID uint    `gorm:"not null;index:idx_wiki_links_dst_resolved,priority:1;index:idx_wiki_links_repo_dst_slug,priority:1"`
	SrcPageID    uint64  `gorm:"primaryKey;autoIncrement:false"`
	DstSlug      string  `gorm:"primaryKey;type:varbinary(255);column:dst_slug;index:idx_wiki_links_repo_dst_slug,priority:2"`
	DstPageID    *uint64 `gorm:"index:idx_wiki_links_dst_resolved,priority:2"`
}

func (WikiPageLink) TableName() string { return "wiki_page_links" }

// WikiBlobRef holds the reference count for one content-addressed
// blob in the filesystem CAS. See §6.6. Refcount drops on rename,
// delete, or replacement; entries hitting zero are eligible for GC.
type WikiBlobRef struct {
	BlobSHA   string `gorm:"primaryKey;type:char(40)"`
	Refcount  int64  `gorm:"not null"`
	Size      int    `gorm:"not null"`
	FirstSeen time.Time
	LastSeen  time.Time
}

func (WikiBlobRef) TableName() string { return "wiki_blob_refs" }

// WikiPendingBlob is the WAL for blob writes that have been uploaded
// to the CAS but not yet referenced from a committed changeset. GC
// reclaims rows older than the retention TTL with no matching
// WikiBlobRef. See §6.6.
type WikiPendingBlob struct {
	BlobSHA   string    `gorm:"primaryKey;type:char(40)"`
	WrittenAt time.Time `gorm:"not null;index"`
	Size      int       `gorm:"not null"`
}

func (WikiPendingBlob) TableName() string { return "wiki_pending_blobs" }
