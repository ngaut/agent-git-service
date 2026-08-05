package db

import "testing"

type retiredWikiDirIndexFixture struct {
	RepositoryID uint   `gorm:"primaryKey;autoIncrement:false"`
	ParentDir    string `gorm:"primaryKey;type:varbinary(1024)"`
	ChildName    string `gorm:"primaryKey;type:varbinary(255)"`
	ChildKind    string `gorm:"type:char(8);not null"`
}

func (retiredWikiDirIndexFixture) TableName() string {
	return retiredWikiDirIndexTable
}

func TestDropRetiredWikiDirIndex(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.AutoMigrate(&retiredWikiDirIndexFixture{}); err != nil {
		t.Fatalf("create retired wiki directory index: %v", err)
	}
	if !gdb.Migrator().HasTable(retiredWikiDirIndexTable) {
		t.Fatal("expected retired wiki directory index fixture")
	}

	if err := DropRetiredWikiDirIndex(gdb); err != nil {
		t.Fatalf("drop retired wiki directory index: %v", err)
	}
	if gdb.Migrator().HasTable(retiredWikiDirIndexTable) {
		t.Fatal("retired wiki directory index still exists")
	}

	if err := DropRetiredWikiDirIndex(gdb); err != nil {
		t.Fatalf("repeat retired wiki directory index drop: %v", err)
	}
}

func TestMigrateDropsRetiredWikiDirIndex(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.AutoMigrate(&retiredWikiDirIndexFixture{}); err != nil {
		t.Fatalf("create retired wiki directory index: %v", err)
	}

	if err := Migrate(gdb); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if gdb.Migrator().HasTable(retiredWikiDirIndexTable) {
		t.Fatal("Migrate kept retired wiki directory index")
	}
}
