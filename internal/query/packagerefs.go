package query

import (
	"context"
	"database/sql"
	"path"
	"sort"
)

// PackageRefAgg is one row in the "top callers" / "top callees" lists
// rendered into SKILL.md by `internal/skills`. Path is a directory
// (the parent of the source or destination file), RefCount is the sum
// of refs pointing into or out of that directory from/to the queried
// package.
type PackageRefAgg struct {
	Path     string `json:"path"`
	RefCount int    `json:"ref_count"`
}

// PackageRefAggregates returns two ranked lists — inbound (other
// directories that reference symbols in pkgDir) and outbound
// (directories that pkgDir references) — both keyed by the parent
// directory of the participating file. Refs of kind=import are
// excluded; they inflate counts without telling the reader anything
// about call structure. Self-edges (src and dst both in pkgDir) are
// dropped — SKILL.md already lists in-package symbols.
//
// Aggregation by parent directory happens in Go; the rows are small
// (one per ref against in-repo symbols) and Go is friendlier than
// nested SQL substr/instr for the directory-only-no-subpackages
// filter.
//
// Used only by `internal/skills`; lives here because `internal/query`
// is the project's sole reader.
func (r *Reader) PackageRefAggregates(ctx context.Context, pkgDir string, limit int) (inbound, outbound []PackageRefAgg, err error) {
	if limit <= 0 {
		limit = 20
	}
	pkgDir = path.Clean(pkgDir)

	inbound, err = aggRefs(ctx, r.db, `
		SELECT srcF.path, dstF.path
		FROM refs r
		JOIN files srcF      ON srcF.id = r.src_file_id
		JOIN symbols dstS    ON dstS.id = r.dst_symbol_id
		JOIN files dstF      ON dstF.id = dstS.file_id
		WHERE r.kind != 'import'
		  AND r.dst_symbol_id IS NOT NULL
		  AND dstF.path LIKE ? || '/%'`,
		pkgDir, pkgDir, true)
	if err != nil {
		return nil, nil, err
	}

	outbound, err = aggRefs(ctx, r.db, `
		SELECT srcF.path, dstF.path
		FROM refs r
		JOIN files srcF      ON srcF.id = r.src_file_id
		JOIN symbols dstS    ON dstS.id = r.dst_symbol_id
		JOIN files dstF      ON dstF.id = dstS.file_id
		WHERE r.kind != 'import'
		  AND r.dst_symbol_id IS NOT NULL
		  AND srcF.path LIKE ? || '/%'`,
		pkgDir, pkgDir, false)
	if err != nil {
		return nil, nil, err
	}

	if len(inbound) > limit {
		inbound = inbound[:limit]
	}
	if len(outbound) > limit {
		outbound = outbound[:limit]
	}
	return inbound, outbound, nil
}

// aggRefs runs q with one bound argument (the SQL LIKE prefix), then
// aggregates by the appropriate side. inboundSide=true groups by the
// source path's parent directory and gates each row on the dst being
// directly in pkgDir (not in a subpackage). outboundSide does the
// mirror image.
func aggRefs(ctx context.Context, db *sql.DB, q, sqlArg, pkgDir string, inboundSide bool) ([]PackageRefAgg, error) {
	rows, err := db.QueryContext(ctx, q, sqlArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var srcPath, dstPath string
		if err := rows.Scan(&srcPath, &dstPath); err != nil {
			return nil, err
		}
		var bucket, gateDir string
		if inboundSide {
			bucket = path.Dir(srcPath)
			gateDir = path.Dir(dstPath)
		} else {
			bucket = path.Dir(dstPath)
			gateDir = path.Dir(srcPath)
		}
		// Strict: dst (inbound) / src (outbound) must be *directly* in
		// pkgDir, not in a subpackage. Subpackages get their own SKILL.md.
		if gateDir != pkgDir {
			continue
		}
		// Drop self-edges — same package on both sides.
		if bucket == pkgDir {
			continue
		}
		counts[bucket]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PackageRefAgg, 0, len(counts))
	for d, n := range counts {
		out = append(out, PackageRefAgg{Path: d, RefCount: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RefCount != out[j].RefCount {
			return out[i].RefCount > out[j].RefCount
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}
