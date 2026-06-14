package service_test

import (
	"testing"

	"github.com/ngaut/agent-git-service/internal/testharness"
	"gorm.io/gorm"
)

func openMigratedServiceTestDB(tb testing.TB) *gorm.DB {
	tb.Helper()
	gdb, cleanup := testharness.OpenServiceDB(tb)
	tb.Cleanup(cleanup)
	return gdb
}
