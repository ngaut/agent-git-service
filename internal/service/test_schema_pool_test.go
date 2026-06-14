package service

import (
	"sync"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"

	"gorm.io/gorm"
)

var servicePackageSchemaTemplate struct {
	once sync.Once
	pool *testdb.SchemaPool
	err  error
}

func openMigratedServiceTestDB(tb testing.TB) (*gorm.DB, func()) {
	tb.Helper()
	return servicePackageTemplatePool(tb).Open(tb)
}

func openVectorServiceTestDB(tb testing.TB) (*gorm.DB, func()) {
	tb.Helper()
	return servicePackageTemplatePool(tb).Open(tb)
}

func servicePackageTemplatePool(tb testing.TB) *testdb.SchemaPool {
	tb.Helper()
	servicePackageSchemaTemplate.once.Do(func() {
		gdb, cleanup := testdb.OpenRaw(tb, "service_pkg_template")
		_ = cleanup
		var templateDB string
		if err := gdb.Raw("SELECT DATABASE()").Scan(&templateDB).Error; err != nil {
			servicePackageSchemaTemplate.err = err
			return
		}
		if err := db.Migrate(gdb); err != nil {
			servicePackageSchemaTemplate.err = err
			return
		}
		db.InitVector(gdb, 3)
		servicePackageSchemaTemplate.pool = &testdb.SchemaPool{
			TemplateDB: templateDB,
			Prefix:     "service_pkg",
		}
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if servicePackageSchemaTemplate.err != nil {
		tb.Fatalf("prepare service package schema template: %v", servicePackageSchemaTemplate.err)
	}
	if servicePackageSchemaTemplate.pool == nil {
		tb.Fatal("service package schema pool was not initialized")
	}
	return servicePackageSchemaTemplate.pool
}
