package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	driversql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// gormSlogLogger implements gormlogger.Interface using slog.
// This is inlined here to avoid db depending on internal/logging.
type gormSlogLogger struct {
	cfg gormlogger.Config
}

func (l *gormSlogLogger) Config() gormlogger.Config {
	return l.cfg
}

func (l *gormSlogLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *l
	clone.cfg.LogLevel = level
	return &clone
}

func (l *gormSlogLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.cfg.LogLevel < gormlogger.Info {
		return
	}
	slog.InfoContext(ctx, "gorm info",
		"component", "gorm",
		"message", fmt.Sprintf(msg, data...),
	)
}

func (l *gormSlogLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.cfg.LogLevel < gormlogger.Warn {
		return
	}
	slog.WarnContext(ctx, "gorm warning",
		"component", "gorm",
		"message", fmt.Sprintf(msg, data...),
	)
}

func (l *gormSlogLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.cfg.LogLevel < gormlogger.Error {
		return
	}
	slog.ErrorContext(ctx, "gorm error",
		"component", "gorm",
		"message", fmt.Sprintf(msg, data...),
	)
}

func (l *gormSlogLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.cfg.LogLevel == gormlogger.Silent {
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}

	elapsed := time.Since(begin)
	switch {
	case err != nil && l.cfg.LogLevel >= gormlogger.Error && (!errors.Is(err, gorm.ErrRecordNotFound) || !l.cfg.IgnoreRecordNotFoundError):
		sql, rows := fc()
		slog.ErrorContext(ctx, "gorm query failed",
			"component", "gorm",
			"elapsed_ms", elapsed.Milliseconds(),
			"rows_affected", normalizeRows(rows),
			"sql", sanitizeSQL(sql),
			"error", err,
		)
	case l.cfg.SlowThreshold > 0 && elapsed > l.cfg.SlowThreshold && l.cfg.LogLevel >= gormlogger.Warn:
		sql, rows := fc()
		slog.WarnContext(ctx, "gorm slow query",
			"component", "gorm",
			"elapsed_ms", elapsed.Milliseconds(),
			"slow_threshold_ms", l.cfg.SlowThreshold.Milliseconds(),
			"rows_affected", normalizeRows(rows),
			"sql", sanitizeSQL(sql),
		)
	case l.cfg.LogLevel >= gormlogger.Info:
		sql, rows := fc()
		slog.InfoContext(ctx, "gorm query",
			"component", "gorm",
			"elapsed_ms", elapsed.Milliseconds(),
			"rows_affected", normalizeRows(rows),
			"sql", sanitizeSQL(sql),
		)
	}
}

func normalizeRows(rows int64) any {
	if rows < 0 {
		return nil
	}
	return rows
}

func sanitizeSQL(sql string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(sql)), " ")
}

// Init opens the database connection, runs AutoMigrate and seeds initial data.
// It returns the *gorm.DB so callers can inject it into service.Service.
// It will return an error if the database is not reachable within 30 seconds.
func Init(dsn string) (*gorm.DB, error) {
	// Wrap the entire DB init in a timeout context to prevent indefinite hangs
	// when TiDB Cloud is slow to authenticate (e.g., cluster waking up).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var database *gorm.DB

	type result struct {
		db  *gorm.DB
		err error
	}

	ch := make(chan result, 1)
	go func() {
		dialector, dialect := dialectorForDSN(dsn)
		cfg := &gorm.Config{
			Logger: &gormSlogLogger{cfg: gormlogger.Config{
				LogLevel:                  gormlogger.Warn,
				Colorful:                  false,
				ParameterizedQueries:      true,
				IgnoreRecordNotFoundError: true,
				SlowThreshold:             200 * time.Millisecond,
			}},
		}
		db, err := gorm.Open(dialector, cfg)
		if err != nil {
			err = fmt.Errorf("%s: %w", dialect, err)
		}
		ch <- result{db, err}
	}()

	select {
	case <-ctx.Done():
		// Drain late result so the goroutine isn't blocked forever on ch<-,
		// and close any connection that completes after the timeout.
		go func() {
			if r := <-ch; r.db != nil {
				if sqlDB, err := r.db.DB(); err == nil {
					sqlDB.Close()
				}
			}
		}()
		return nil, fmt.Errorf("db: connection timed out after 30s (check DB_DSN and TiDB cluster status)")
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("db: failed to open: %w", r.err)
		}
		database = r.db
	}

	// Configure connection pool tuning for TiDB/MySQL to prevent PD server timeouts.
	// These settings help manage connection lifecycle and reduce timeout errors.
	if sqlDB, err := database.DB(); err == nil {
		// Set connection pool limits to prevent overwhelming TiDB PD server
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(50)
		// Connections idle for more than 5 minutes are closed
		sqlDB.SetConnMaxIdleTime(5 * time.Minute)
		// Connections are closed after 30 minutes to prevent stale connections
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
		slog.Info("db: connection pool configured", "maxIdle", 10, "maxOpen", 50, "maxIdleTime", "5m", "maxLifetime", "30m")
	} else {
		slog.Warn("db: failed to get underlying sql.DB for pool configuration", "error", err)
	}

	// 10 minutes (600s) was selected based on observed TiDB Cloud wake-up times
	// during cluster cold starts. TiDB Cloud serverless tier can take several
	// minutes to authenticate and become fully available after idle periods.
	// This timeout provides sufficient headroom for migration completion while
	// still failing fast enough to detect genuine connection issues.
	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer migrateCancel()

	migrateDone := make(chan error, 1)
	go func() {
		migrateDone <- Migrate(database)
	}()

	select {
	case <-migrateCtx.Done():
		return nil, fmt.Errorf("db: auto-migrate timed out after 10m")
	case err := <-migrateDone:
		if err != nil {
			return nil, fmt.Errorf("db: auto-migrate failed: %w", err)
		}
	}

	logCapabilities(database)
	slog.Info("db: connected and migrated.")
	return database, nil
}

func dialectorForDSN(dsn string) (gorm.Dialector, string) {
	return mysql.Open(runtimeMySQLDSN(dsn)), "mysql"
}

func runtimeMySQLDSN(dsn string) string {
	raw := strings.TrimSpace(dsn)
	cfg, err := driversql.ParseDSN(raw)
	if err != nil {
		// Preserve the original initialization error for malformed DSNs.
		return raw
	}
	if preservesDriverSideParameterBinding(raw, cfg) {
		return raw
	}
	// GORM sends parameterized one-shot queries throughout the request path.
	// Driver-side interpolation retains escaping while avoiding a server-side
	// prepare/execute/close cycle for every statement.
	cfg.InterpolateParams = true
	optimized := cfg.FormatDSN()
	if _, err := driversql.ParseDSN(optimized); err != nil {
		return raw
	}
	return optimized
}

func preservesDriverSideParameterBinding(dsn string, cfg *driversql.Config) bool {
	params, hasQuery, queryOK := mysqlDSNQueryParams(dsn)
	if !hasQuery {
		return false
	}
	if !queryOK {
		return true
	}
	if hasFalseBoolParam(params, "interpolateParams") {
		return true
	}
	for _, value := range params["charset"] {
		for _, charset := range strings.Split(value, ",") {
			name, ok := canonicalMySQLCharset(charset)
			if !ok || unsafeMySQLCharsets[name] {
				return true
			}
		}
	}
	for _, value := range params["collation"] {
		if unsafeMySQLCollations[strings.ToLower(value)] {
			return true
		}
	}
	if len(cfg.Params) > 0 {
		return true
	}
	return false
}

func mysqlDSNQueryParams(dsn string) (url.Values, bool, bool) {
	slash := strings.LastIndexByte(dsn, '/')
	if slash == -1 {
		return nil, false, true
	}
	queryStart := strings.IndexByte(dsn[slash+1:], '?')
	if queryStart == -1 {
		return nil, false, true
	}
	params, err := url.ParseQuery(dsn[slash+1+queryStart+1:])
	if err != nil {
		return nil, true, false
	}
	return params, true, true
}

func hasFalseBoolParam(params url.Values, key string) bool {
	for _, value := range params[key] {
		switch value {
		case "0", "false", "FALSE", "False":
			return true
		}
	}
	return false
}

func canonicalMySQLCharset(charset string) (string, bool) {
	charset = strings.ToLower(strings.TrimSpace(charset))
	if charset == "" {
		return "", false
	}
	for _, r := range charset {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "", false
		}
	}
	return charset, true
}

var unsafeMySQLCharsets = map[string]bool{
	"big5":    true,
	"cp932":   true,
	"gb18030": true,
	"gb2312":  true,
	"gbk":     true,
	"sjis":    true,
}

var unsafeMySQLCollations = map[string]bool{
	"big5_chinese_ci":        true,
	"sjis_japanese_ci":       true,
	"gbk_chinese_ci":         true,
	"big5_bin":               true,
	"gb2312_chinese_ci":      true,
	"gb2312_bin":             true,
	"gbk_bin":                true,
	"sjis_bin":               true,
	"cp932_japanese_ci":      true,
	"cp932_bin":              true,
	"gb18030_chinese_ci":     true,
	"gb18030_bin":            true,
	"gb18030_unicode_520_ci": true,
}

// Migrate runs AutoMigrate for all application models on the given DB.
// It is called by Init and can be reused by tests or embedders.
func Migrate(database *gorm.DB) error {
	if err := MigrateWikiSlugColumnsBeforeAutoMigrate(database); err != nil {
		return err
	}
	if err := database.AutoMigrate(
		&User{},
		&OrganizationMember{},
		&OrganizationInvitation{},
		&OutsideCollaborator{},
		&AgentBinding{},
		&AgentInvite{},
		&Team{},
		&TeamMember{},
		&TeamRepository{},
		&Repository{},
		&IssuePRNumberCounter{},
		&RepoRedirect{},
		&Token{},
		&UserIdentity{},
		&DeviceCode{},
		&DeviceCodeAuditLog{},
		&AuthorizationCode{},
		&Label{},
		&DeployKey{},
		&Milestone{},
		&Issue{},
		&IssueComment{},
		&IssueEvent{},
		&IssueReference{},
		&PagesConfig{},
		&PagesBuild{},
		&AuditLogEntry{},
		&PullRequest{},
		&LinkedBranch{},
		&Release{},
		&ReleaseAsset{},
		&Variable{},
		&Secret{},
		&Environment{},
		&SSHKey{},
		&GPGKey{},
		&SSHSigningKey{},
		&Project{},
		&ProjectField{},
		&ProjectItem{},
		&ProjectRepoLink{},
		&Workflow{},
		&WorkflowRun{},
		&WorkflowRunJob{},
		&Artifact{},
		&ActionCache{},
		&Ruleset{},
		&ReviewRequest{},
		&PullRequestReview{},
		&PRReviewComment{},
		&Autolink{},
		&Star{},
		&Gist{},
		&CommitStatus{},
		&Webhook{},
		&HookDelivery{},
		&Deployment{},
		&DeploymentStatus{},
		&BranchProtection{},
		&DependabotAlert{},
		&RepositoryInvitation{},
		&Collaborator{},
		&Reaction{},
		&Notification{},
		&WikiPageLabel{},
		&WikiPageIndex{},
		&WikiIndexState{},
		&WikiBacklink{},
		&WikiPageHistory{},
		&WikiSearchDocument{},
		&WikiSearchProjectionTask{},
		&WikiPage{},
		&WikiPageRevision{},
		&WikiChangeset{},
		&WikiRepoHead{},
		&WikiGitRepairObligation{},
		&WikiCompactionJob{},
		&WikiPageLink{},
		&WikiBlobRef{},
		&WikiPendingBlob{},
	); err != nil {
		return err
	}
	if err := DropRetiredWikiDirIndex(database); err != nil {
		return err
	}
	if err := MigrateIssueCommentPinningColumns(database); err != nil {
		return err
	}
	if err := MigrateIssueCommentThreadingColumns(database); err != nil {
		return err
	}
	// Enforce the user_kind column contract.
	if err := MigrateUserKind(database); err != nil {
		return err
	}
	if err := MigrateTeamOrgSlugIndex(database); err != nil {
		return err
	}
	if err := MigrateTeamMemberLookupIndexes(database); err != nil {
		return err
	}
	if err := MigrateLabelLookupIndexes(database); err != nil {
		return err
	}
	if err := MigrateIssueSearch(database); err != nil {
		return err
	}
	if err := MigrateWikiSearch(database); err != nil {
		return err
	}
	if err := MigrateWikiSlugColumns(database); err != nil {
		return err
	}
	if err := MigrateWikiChangesetSourceGit(database); err != nil {
		return err
	}
	// Add unique index on (project_id, content_id, type) to prevent duplicate items
	return MigrateProjectItemUniqueIndex(database)
}

// InitVector attempts to add VECTOR(dims) columns to the issues and
// pull_requests tables for semantic search, then best-effort vector indexes
// for TiDB-backed deployments that expose VEC_COSINE_DISTANCE. It uses raw SQL
// because GORM does not natively support TiDB's VECTOR type.
//
// This is a best-effort operation: unsupported deployments are skipped by
// capability probe, and DDL errors are logged without blocking startup.
func InitVector(database *gorm.DB, dims int) {
	if dims <= 0 || !SupportsVectorDistance(database) {
		return
	}
	// Cap at 16384 — TiDB supports up to ~16K dimensions.
	// This prevents a misconfigured Embedder.Dimensions() from creating
	// an absurd VECTOR(9223372036854775807) column that TiDB would reject.
	if dims > 16384 {
		slog.Warn("db: InitVector: dims capped to 16384", "requested", dims)
		dims = 16384
	}
	migrator := database.Migrator()
	tables := []string{"issues", "pull_requests"}
	for _, table := range tables {
		if migrator.HasColumn(table, "embedding") {
			slog.Debug("db: InitVector: embedding column already exists", "table", table)
			continue
		}
		sql := fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `embedding` VECTOR(%d)", table, dims)
		if err := database.Exec(sql).Error; err != nil {
			msg := strings.ToLower(err.Error())
			if migrator.HasColumn(table, "embedding") ||
				strings.Contains(msg, "duplicate column") ||
				strings.Contains(msg, "already exists") {
				slog.Debug("db: InitVector: embedding column already exists", "table", table)
				continue
			}
			slog.Warn("db: InitVector: "+table, "error", err)
		} else {
			slog.Debug("db: InitVector: added embedding column", "table", table, "dims", dims)
		}
	}

	ensureWikiSearchVector(database, dims)
	ensureVectorIndexes(database)
}
