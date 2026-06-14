package db

import (
	"fmt"
	"strings"
)

func fullTextIndexDDLs(table, index, column string) []string {
	base := fullTextIndexDDLFor(table, index, column)
	return []string{
		base + " ADD_COLUMNAR_REPLICA_ON_DEMAND",
		base,
	}
}

func fullTextIndexDDLFor(table, index, column string) string {
	return fmt.Sprintf(
		"ALTER TABLE %s ADD FULLTEXT INDEX %s (%s) WITH PARSER MULTILINGUAL",
		mysqlIdent(table),
		mysqlIdent(index),
		mysqlIdent(column),
	)
}

func mysqlIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
