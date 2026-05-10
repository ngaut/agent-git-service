package db

import (
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// MigrateTeamOrgSlugIndex ensures the unique index on teams is scoped to the org.
// Older schema versions had a globally-unique slug index, which breaks admins
// team creation for multiple orgs.
//
// This migration checks if the index already exists with the correct shape
// (composite unique index on organization_id and slug) before attempting to
// drop/recreate. This prevents TiDB Error 1553 when the index is required
// by a foreign key constraint.
func MigrateTeamOrgSlugIndex(database *gorm.DB) error {
	migrator := database.Migrator()

	// Check if the index already exists with the correct shape
	if migrator.HasIndex(&Team{}, "idx_org_slug") {
		// Verify the index has the correct columns (organization_id, slug)
		hasCorrectShape, err := hasCorrectIndexShape(database, "teams", "idx_org_slug", []string{"organization_id", "slug"})
		if err != nil {
			return fmt.Errorf("checking index shape: %w", err)
		}
		if hasCorrectShape {
			// Index already exists with correct shape, nothing to do
			return nil
		}
		// Index exists but has wrong shape, drop it
		if err := migrator.DropIndex(&Team{}, "idx_org_slug"); err != nil {
			return err
		}
	}
	return migrator.CreateIndex(&Team{}, "idx_org_slug")
}

// hasCorrectIndexShape checks if an index exists with the expected columns in the correct order.
// It detects the database backend and uses the appropriate query syntax.
func hasCorrectIndexShape(db *gorm.DB, tableName, indexName string, expectedColumns []string) (bool, error) {
	dialect := db.Dialector.Name()

	switch dialect {
	case "sqlite":
		return hasCorrectIndexShapeSQLite(db, tableName, indexName, expectedColumns)
	case "postgres":
		return hasCorrectIndexShapePostgres(db, tableName, indexName, expectedColumns)
	default:
		// MySQL/TiDB use SHOW INDEX syntax.
		return hasCorrectIndexShapeMySQL(db, tableName, indexName, expectedColumns)
	}
}

// hasCorrectIndexShapeSQLite checks index shape using SQLite PRAGMA commands.
// SQLite PRAGMAs cannot be parameterized, so identifier substitution flows
// exclusively through safePragmaRows which rejects anything non-identifier.
func hasCorrectIndexShapeSQLite(db *gorm.DB, tableName, indexName string, expectedColumns []string) (bool, error) {
	// First, check if the index exists using PRAGMA index_list
	rows, err := safePragmaRows(db, "index_list", tableName)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	indexExists := false
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  sql.NullString
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return false, err
		}
		if name == indexName {
			indexExists = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	if !indexExists {
		return false, nil
	}

	// Get the columns in the index using PRAGMA index_info
	rows, err = safePragmaRows(db, "index_info", indexName)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var (
			seq     int
			colID   sql.NullInt64
			colName sql.NullString
		)
		if err := rows.Scan(&seq, &colID, &colName); err != nil {
			return false, err
		}
		if colName.Valid {
			columns = append(columns, colName.String)
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	// Compare columns
	if len(columns) != len(expectedColumns) {
		return false, nil
	}
	for i, col := range columns {
		if !strings.EqualFold(col, expectedColumns[i]) {
			return false, nil
		}
	}
	return true, nil
}

// safePragmaRows runs `PRAGMA <name>(<ident>)` after validating the identifier.
// SQLite PRAGMAs do not accept parameters, so every caller must route identifier
// substitution through this helper to keep the injection guard centralized.
func safePragmaRows(db *gorm.DB, pragma, ident string) (*sql.Rows, error) {
	if !isValidSQLIdentifier(pragma) {
		return nil, fmt.Errorf("invalid PRAGMA name %q", pragma)
	}
	if !isValidSQLIdentifier(ident) {
		return nil, fmt.Errorf("invalid identifier %q", ident)
	}
	return db.Raw(fmt.Sprintf("PRAGMA %s(%s)", pragma, ident)).Rows()
}

// isValidSQLIdentifier checks if a string is a valid SQL identifier
// (alphanumeric + underscore, starting with letter or underscore)
func isValidSQLIdentifier(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
				return false
			}
		}
	}
	return true
}

// hasCorrectIndexShapeMySQL checks index shape using MySQL/TiDB SHOW INDEX syntax.
func hasCorrectIndexShapeMySQL(db *gorm.DB, tableName, indexName string, expectedColumns []string) (bool, error) {
	// Use SHOW INDEX to get index column information
	// TiDB returns: Table, Non_unique, Key_name, Seq_in_index, Column_name, Collation,
	// Cardinality, Sub_part, Packed, Null, Index_type, Comment, Index_comment,
	// Visible, Expression, Clustered, Global
	rows, err := db.Raw(fmt.Sprintf("SHOW INDEX FROM `%s` WHERE Key_name = ?", tableName), indexName).Rows()
	if err != nil {
		return false, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var (
			table        string
			nonUnique    string
			keyName      string
			seqInIndex   int
			columnName   string
			collation    sql.NullString
			cardinality  sql.NullInt64
			subPart      sql.NullInt64
			packed       sql.NullString
			null         string
			indexType    string
			comment      string
			indexComment string
			visible      string
			expression   sql.NullString
			clustered    string
			global       string
		)
		err := rows.Scan(
			&table, &nonUnique, &keyName, &seqInIndex, &columnName,
			&collation, &cardinality, &subPart, &packed, &null,
			&indexType, &comment, &indexComment, &visible, &expression,
			&clustered, &global,
		)
		if err != nil {
			return false, err
		}
		columns = append(columns, columnName)
	}

	if err := rows.Err(); err != nil {
		return false, err
	}

	// Compare columns
	if len(columns) != len(expectedColumns) {
		return false, nil
	}
	for i, col := range columns {
		if !strings.EqualFold(col, expectedColumns[i]) {
			return false, nil
		}
	}
	return true, nil
}

// hasCorrectIndexShapePostgres checks index shape using pg_indexes so PostgreSQL
// does not attempt MySQL-only SHOW INDEX syntax during migrations.
func hasCorrectIndexShapePostgres(db *gorm.DB, tableName, indexName string, expectedColumns []string) (bool, error) {
	var indexDef string
	err := db.Raw(`
SELECT indexdef
FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = ?
  AND indexname = ?
`, tableName, indexName).Row().Scan(&indexDef)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	open := strings.LastIndex(indexDef, "(")
	close := strings.LastIndex(indexDef, ")")
	if open == -1 || close == -1 || close <= open {
		return false, nil
	}
	rawColumns := strings.Split(indexDef[open+1:close], ",")
	columns := make([]string, 0, len(rawColumns))
	for _, raw := range rawColumns {
		col := strings.TrimSpace(strings.Trim(raw, `"`))
		if col == "" {
			return false, nil
		}
		columns = append(columns, col)
	}

	if len(columns) != len(expectedColumns) {
		return false, nil
	}
	for i, col := range columns {
		if !strings.EqualFold(col, expectedColumns[i]) {
			return false, nil
		}
	}
	return true, nil
}
