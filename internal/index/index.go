package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jdwiederstein/mycelium/internal/parser"
)


// Index is the write-side handle to the SQLite-backed knowledge base.
// Open() applies migrations; Close() should be called on shutdown.
type Index struct {
	db   *sql.DB
	path string
}

// Open creates (or opens) the index file, ensures the parent directory exists,
// and runs any pending migrations.
func Open(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir index parent: %w", err)
	}
	db, err := sql.Open("sqlite3", path+"?_fk=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := applyMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Index{db: db, path: path}, nil
}

func (ix *Index) Close() error { return ix.db.Close() }
func (ix *Index) Path() string { return ix.path }

// DB exposes the underlying handle for the read-side query package.
// Write-side callers must go through the methods on Index instead.
func (ix *Index) DB() *sql.DB { return ix.db }

// UpsertFileResult reports what UpsertFile did, so the caller can skip embed work
// on unchanged files.
type UpsertFileResult struct {
	FileID     int64
	Changed    bool
	WasPresent bool
}

// UpsertFile inserts or updates a file row if its content_hash changed.
// Returns the row id and whether the file's content changed.
func (ix *Index) UpsertFile(ctx context.Context, tx *sql.Tx, path, language string, sizeBytes int64, mtimeNS int64, contentHash, parseHash []byte) (UpsertFileResult, error) {
	var id int64
	var existingHash []byte
	err := tx.QueryRowContext(ctx, `SELECT id, content_hash FROM files WHERE path = ?`, path).Scan(&id, &existingHash)
	switch {
	case err == sql.ErrNoRows:
		res, insErr := tx.ExecContext(ctx, `
			INSERT INTO files(path, language, size_bytes, mtime_ns, content_hash, parse_hash, last_indexed_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			path, language, sizeBytes, mtimeNS, contentHash, parseHash, time.Now().Unix())
		if insErr != nil {
			return UpsertFileResult{}, fmt.Errorf("insert file: %w", insErr)
		}
		newID, _ := res.LastInsertId()
		return UpsertFileResult{FileID: newID, Changed: true, WasPresent: false}, nil
	case err != nil:
		return UpsertFileResult{}, fmt.Errorf("select file: %w", err)
	}
	if bytesEqual(existingHash, contentHash) {
		return UpsertFileResult{FileID: id, Changed: false, WasPresent: true}, nil
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE files
		SET language = ?, size_bytes = ?, mtime_ns = ?, content_hash = ?, parse_hash = ?, last_indexed_at = ?
		WHERE id = ?`,
		language, sizeBytes, mtimeNS, contentHash, parseHash, time.Now().Unix(), id)
	if err != nil {
		return UpsertFileResult{}, fmt.Errorf("update file: %w", err)
	}
	return UpsertFileResult{FileID: id, Changed: true, WasPresent: true}, nil
}

// ReplaceFileSymbols deletes all existing symbols for a file and inserts the
// new set. v0.1 uses full replace per file; smarter diffing (by symbol_hash) is
// a later optimization. Cascaded deletes clear refs and chunks automatically.
func (ix *Index) ReplaceFileSymbols(ctx context.Context, tx *sql.Tx, fileID int64, symbols []parser.Symbol) (map[string]int64, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM symbols WHERE file_id = ?`, fileID); err != nil {
		return nil, fmt.Errorf("delete old symbols: %w", err)
	}
	ids := make(map[string]int64, len(symbols))
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO symbols(file_id, name, qualified, kind, start_line, start_col, end_line, end_col,
		                    signature, docstring, visibility, parent_id, symbol_hash)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare insert symbol: %w", err)
	}
	defer stmt.Close()

	// First pass: insert without parent_id. Second pass wires up parent_id for
	// methods. v0.1 keeps parent_id null when the receiver type lives in another
	// file; those links get resolved by the ref resolution pass in v0.2.
	for _, s := range symbols {
		res, err := stmt.ExecContext(ctx,
			fileID, s.Name, s.Qualified, string(s.Kind),
			s.StartLine, s.StartCol, s.EndLine, s.EndCol,
			nullString(s.Signature), nullString(s.Docstring), nullString(string(s.Visibility)),
			nil,
			s.Hash)
		if err != nil {
			return nil, fmt.Errorf("insert symbol %s: %w", s.Qualified, err)
		}
		id, _ := res.LastInsertId()
		ids[s.Qualified] = id
	}
	for _, s := range symbols {
		if s.ParentName == "" {
			continue
		}
		// Try to find the parent type within the same file first; cross-file
		// lookup can happen later.
		parentQualified := qualifiedParent(s.Qualified, s.ParentName)
		parentID, ok := ids[parentQualified]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE symbols SET parent_id = ? WHERE id = ?`, parentID, ids[s.Qualified]); err != nil {
			return nil, fmt.Errorf("wire parent for %s: %w", s.Qualified, err)
		}
	}
	return ids, nil
}

// ReplaceFileRefs deletes all refs sourced from a file and inserts new ones.
// dst_symbol_id is left null at this stage; resolution happens afterwards.
func (ix *Index) ReplaceFileRefs(ctx context.Context, tx *sql.Tx, fileID int64, symbolIDs map[string]int64, refs []parser.Reference) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM refs WHERE src_file_id = ?`, fileID); err != nil {
		return fmt.Errorf("delete old refs: %w", err)
	}
	if len(refs) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO refs(src_file_id, src_symbol_id, dst_symbol_id, dst_name, dst_short, kind, line, col, resolved)
		VALUES(?, ?, NULL, ?, ?, ?, ?, ?, 0)`)
	if err != nil {
		return fmt.Errorf("prepare insert ref: %w", err)
	}
	defer stmt.Close()
	for _, r := range refs {
		var srcID interface{}
		if r.SrcSymbolQualified != "" {
			if id, ok := symbolIDs[r.SrcSymbolQualified]; ok {
				srcID = id
			}
		}
		if _, err := stmt.ExecContext(ctx, fileID, srcID, r.DstName, shortName(r.DstName), string(r.Kind), r.Line, r.Col); err != nil {
			return fmt.Errorf("insert ref: %w", err)
		}
	}
	return nil
}

// shortName returns the segment after the final "." in a dotted name, or the
// whole string if there is no dot. "r.FindSymbol" -> "FindSymbol";
// "fmt.Println" -> "Println"; "Printf" -> "Printf".
func shortName(dotted string) string {
	for i := len(dotted) - 1; i >= 0; i-- {
		if dotted[i] == '.' {
			return dotted[i+1:]
		}
	}
	return dotted
}

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func qualifiedParent(childQualified, parentName string) string {
	// child is "pkg.Type.Method" -> parent is "pkg.Type". For top-level methods
	// without a "." we can't construct a parent; return empty.
	last := -1
	for i := len(childQualified) - 1; i >= 0; i-- {
		if childQualified[i] == '.' {
			last = i
			break
		}
	}
	if last < 0 {
		return ""
	}
	return childQualified[:last]
}
