package query

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// LastFullScanAt returns when the pipeline last completed a full reconcile
// (walk + upsert + prune), as recorded in index_meta. ok=false means no
// reconcile has ever completed — an index written before the prune pass
// existed, or a daemon that never finished its catch-up scan.
func (r *Reader) LastFullScanAt(ctx context.Context) (t time.Time, ok bool, err error) {
	var v string
	err = r.db.QueryRowContext(ctx,
		`SELECT value FROM index_meta WHERE key = 'last_full_scan_at'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	secs, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return time.Time{}, false, nil
	}
	return time.Unix(secs, 0), true, nil
}
