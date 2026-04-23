package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"

	"github.com/mattn/go-sqlite3"
)

// vssState tracks whether the current DB handle has sqlite-vec loaded AND
// whether vss_chunks exists. Both flags live on the Index so search paths
// can branch without re-checking every query.
type vssState struct {
	enabled   bool // extension loaded successfully into every conn
	tableName string
	dim       int // dimension of embeddings stored in the virtual table
}

// driverOnce ensures we register the extension-enabled driver at most once
// per process, keyed by the extension path. database/sql disallows duplicate
// driver names, so the key lookup matters.
var (
	driverMu   sync.Mutex
	driverReg  = map[string]string{} // extPath -> registered driver name
	nextDriver = 0
)

// registerDriverWithExt returns a unique sql.Register'd driver name whose
// ConnectHook loads the given sqlite-vec extension. If the file doesn't
// exist, returns "" and a nil error — caller should fall back to the
// default "sqlite3" driver.
func registerDriverWithExt(extPath string) (string, error) {
	if extPath == "" {
		return "", nil
	}
	if _, err := os.Stat(extPath); err != nil {
		return "", fmt.Errorf("stat %s: %w", extPath, err)
	}
	driverMu.Lock()
	defer driverMu.Unlock()
	if name, ok := driverReg[extPath]; ok {
		return name, nil
	}
	name := fmt.Sprintf("sqlite3_vec_%d", nextDriver)
	nextDriver++
	sql.Register(name, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			// LoadExtension wraps enable_load_extension and disables it
			// again on return, so we don't have to manage that lifecycle.
			return conn.LoadExtension(extPath, "")
		},
	})
	driverReg[extPath] = name
	return name, nil
}

// probeVecVersion checks the extension actually loaded by issuing a
// trivial SELECT. Cheap safety belt — if the driver registration succeeded
// but the .so/.dylib is from a different project, we'll see the error here.
func probeVecVersion(db *sql.DB) (string, error) {
	var v string
	if err := db.QueryRowContext(context.Background(), `SELECT vec_version()`).Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// EnsureVSS creates the vss_chunks virtual table if it doesn't exist and
// backfills rows from any already-embedded chunks. Safe to call on every
// daemon start; on subsequent runs it's a no-op after the CREATE IF NOT
// EXISTS. Noop when sqlite-vec isn't loaded.
func (ix *Index) EnsureVSS(ctx context.Context, dim int) error {
	if !ix.vss.enabled || dim <= 0 {
		return nil
	}
	name := fmt.Sprintf("vss_chunks_%d", dim)
	createSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(embedding float[%d])`, name, dim)
	if _, err := ix.db.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create vss_chunks: %w", err)
	}
	ix.vss.tableName = name
	ix.vss.dim = dim

	// Backfill. We only insert rows that aren't already in vss_chunks —
	// on a hot restart this is typically a no-op; on first-boot-with-vec
	// this populates everything the brute-force path already embedded.
	// vec0 rowids mirror chunks.id so JOIN semantics stay simple.
	backfillSQL := fmt.Sprintf(`
		INSERT INTO %s(rowid, embedding)
		SELECT c.id, c.embedding FROM chunks c
		WHERE c.embedding IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM %s v WHERE v.rowid = c.id)`, name, name)
	if _, err := ix.db.ExecContext(ctx, backfillSQL); err != nil {
		return fmt.Errorf("backfill vss_chunks: %w", err)
	}
	return nil
}

// VSSAvailable reports whether the vec0 fast path is usable. False means
// searches fall back to brute-force cosine (correct, just slower).
func (ix *Index) VSSAvailable() bool { return ix.vss.enabled && ix.vss.tableName != "" }

// VSSTableName returns the active vec0 table name, or "" if disabled.
func (ix *Index) VSSTableName() string { return ix.vss.tableName }

// upsertVSS pushes a freshly-computed embedding into the vec0 table. Called
// from WriteEmbedding after the main UPDATE succeeds. Safe no-op when
// vec0 isn't configured.
func (ix *Index) upsertVSS(ctx context.Context, tx *sql.Tx, chunkID int64, packed []byte) error {
	if !ix.VSSAvailable() {
		return nil
	}
	// vec0 rowids are externally supplied. INSERT OR REPLACE keeps the
	// table eventually-consistent with chunks.embedding on re-embeds.
	stmt := fmt.Sprintf(`INSERT OR REPLACE INTO %s(rowid, embedding) VALUES(?, ?)`, ix.vss.tableName)
	_, err := tx.ExecContext(ctx, stmt, chunkID, packed)
	return err
}
