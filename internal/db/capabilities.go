package db

import (
	"database/sql"
	"strings"
	"sync"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var (
	tidbSearchCapabilityCache     sync.Map
	vectorDistanceCapabilityCache sync.Map
)

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

// SupportsVectorDistance reports whether VEC_COSINE_DISTANCE can be used by
// search ranking. TiDB deployments are probed separately because older TiDB
// versions can identify as TiDB without exposing vector-distance functions.
// SQLite is also probed so tests can register a lightweight shim without
// making production SQLite a supported vector backend.
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
	case "sqlite":
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

func quietCapabilityDB(database *gorm.DB) *gorm.DB {
	return database.Session(&gorm.Session{Logger: gormlogger.Discard})
}
