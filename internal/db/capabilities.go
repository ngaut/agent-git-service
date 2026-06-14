package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type capabilities struct {
	Dialect        string
	TiDBSearch     bool
	TiDBFullText   bool
	VectorDistance bool
}

var (
	tidbSearchCapabilityCache     sync.Map
	tidbFullTextCapabilityCache   sync.Map
	vectorDistanceCapabilityCache sync.Map
	tidbFullTextProbeSeq          atomic.Uint64
)

func detectCapabilities(database *gorm.DB) capabilities {
	caps := capabilities{}
	if database == nil || database.Dialector == nil {
		return caps
	}
	caps.Dialect = database.Dialector.Name()
	caps.TiDBSearch = SupportsTiDBSearch(database)
	caps.TiDBFullText = SupportsTiDBFullText(database)
	caps.VectorDistance = SupportsVectorDistance(database)
	return caps
}

func logCapabilities(database *gorm.DB) {
	caps := detectCapabilities(database)
	slog.Info("db: capabilities detected",
		"dialect", caps.Dialect,
		"tidbSearch", caps.TiDBSearch,
		"tidbFullText", caps.TiDBFullText,
		"vectorDistance", caps.VectorDistance,
	)
}

// SupportsTiDBSearch reports whether the database is TiDB rather than plain
// MySQL. TiDB search paths use TiDB-only SQL such as FTS_MATCH_WORD, VECTOR,
// and VEC_COSINE_DISTANCE, so mysql dialect alone is not a sufficient signal.
func SupportsTiDBSearch(database *gorm.DB) bool {
	if database == nil || database.Dialector.Name() != "mysql" || database.DryRun {
		return false
	}
	key := capabilityCacheKey(database)
	if cached, ok := tidbSearchCapabilityCache.Load(key); ok {
		return cached.(bool)
	}

	supported := detectTiDB(database)
	tidbSearchCapabilityCache.Store(key, supported)
	return supported
}

// SupportsTiDBFullText reports whether TiDB full-text indexes and
// FTS_MATCH_WORD queries are available.
func SupportsTiDBFullText(database *gorm.DB) bool {
	if database == nil || database.Dialector.Name() != "mysql" || database.DryRun {
		return false
	}
	if !SupportsTiDBSearch(database) {
		return false
	}
	key := capabilityCacheKey(database)
	if cached, ok := tidbFullTextCapabilityCache.Load(key); ok {
		return cached.(bool)
	}

	supported := probeTiDBFullText(database)
	tidbFullTextCapabilityCache.Store(key, supported)
	return supported
}

// SupportsVectorDistance reports whether VEC_COSINE_DISTANCE can be used by
// search ranking. TiDB deployments are probed separately because older TiDB
// versions can identify as TiDB without exposing vector-distance functions.
func SupportsVectorDistance(database *gorm.DB) bool {
	if database == nil || database.DryRun {
		return false
	}
	key := capabilityCacheKey(database)
	switch database.Dialector.Name() {
	case "mysql":
		if !SupportsTiDBSearch(database) {
			return false
		}
		if cached, ok := vectorDistanceCapabilityCache.Load(key); ok {
			return cached.(bool)
		}
		supported := probeVectorDistance(database)
		vectorDistanceCapabilityCache.Store(key, supported)
		return supported
	default:
		return false
	}
}

func capabilityCacheKey(database *gorm.DB) any {
	if sqlDB, err := database.DB(); err == nil && sqlDB != nil {
		return sqlDB
	}
	return database
}

func detectTiDB(database *gorm.DB) bool {
	database = quietCapabilityDB(database)
	var tidbVersion sql.NullString
	if err := database.Raw("SELECT tidb_version()").Scan(&tidbVersion).Error; err == nil {
		return true
	}

	var version sql.NullString
	if err := database.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		return false
	}
	return isTiDBVersionString(version.String)
}

func isTiDBVersionString(version string) bool {
	return strings.Contains(strings.ToLower(version), "tidb")
}

func probeVectorDistance(database *gorm.DB) bool {
	database = quietCapabilityDB(database)
	var distance float64
	err := database.Raw("SELECT COALESCE(VEC_COSINE_DISTANCE(?, ?), 0)", "[0]", "[0]").Scan(&distance).Error
	return err == nil
}

func probeTiDBFullText(database *gorm.DB) bool {
	database = quietCapabilityDB(database)
	table := fmt.Sprintf("_ags_fts_probe_%d_%d", time.Now().UnixNano(), tidbFullTextProbeSeq.Add(1))
	index := "idx_" + table + "_body"
	tableIdent := mysqlIdent(table)

	_ = database.Exec("DROP TABLE IF EXISTS " + tableIdent).Error
	defer database.Exec("DROP TABLE IF EXISTS " + tableIdent)

	if err := database.Exec(fmt.Sprintf("CREATE TABLE %s (`id` BIGINT PRIMARY KEY AUTO_RANDOM, `body` TEXT)", tableIdent)).Error; err != nil {
		return false
	}
	if err := database.Exec(fmt.Sprintf("INSERT INTO %s (`body`) VALUES (?)", tableIdent), "this is a test document").Error; err != nil {
		return false
	}

	for _, ddl := range fullTextIndexDDLs(table, index, "body") {
		if err := database.Exec(ddl).Error; err != nil {
			continue
		}
		if probeTiDBFullTextQuery(database, table) {
			return true
		}
	}
	return false
}

func probeTiDBFullTextQuery(database *gorm.DB, table string) bool {
	var score float64
	tableIdent := mysqlIdent(table)
	result := database.Raw(fmt.Sprintf(
		"SELECT FTS_MATCH_WORD('test', `body`) FROM %s WHERE FTS_MATCH_WORD('test', `body`) LIMIT 1",
		tableIdent,
	)).Scan(&score)
	return result.Error == nil && result.RowsAffected > 0
}

func quietCapabilityDB(database *gorm.DB) *gorm.DB {
	return database.Session(&gorm.Session{Logger: gormlogger.Discard})
}
