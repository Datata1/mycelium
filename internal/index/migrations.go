package index

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// applyMigrations runs every SQL file under migrations/ in lexicographic order,
// wrapped in transactions. v0.1 is all-or-nothing at the first run; proper
// versioning (tracking applied migrations in `meta`) lands when we add the
// second migration.
func applyMigrations(db *sql.DB) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	if err := ensureMetaTable(db); err != nil {
		return err
	}
	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}

	for _, name := range files {
		if applied[name] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := runMigration(db, name, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}

func ensureMetaTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS applied_migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`)
	return err
}

func appliedMigrations(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM applied_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func runMigration(db *sql.DB, name, sqlText string) error {
	// PRAGMAs like journal_mode can't run inside a transaction in SQLite, so
	// split them out and execute before the transactional body.
	pragmas, rest := splitPragmas(sqlText)
	if pragmas != "" {
		if _, err := db.Exec(pragmas); err != nil {
			return fmt.Errorf("pragmas: %w", err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(rest); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT INTO applied_migrations(name, applied_at) VALUES(?, strftime('%s','now'))`, name); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func splitPragmas(sqlText string) (pragmas, rest string) {
	var pb, rb strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trim), "PRAGMA ") {
			pb.WriteString(line)
			pb.WriteByte('\n')
			continue
		}
		rb.WriteString(line)
		rb.WriteByte('\n')
	}
	return pb.String(), rb.String()
}
