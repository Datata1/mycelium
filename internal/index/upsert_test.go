package index

import (
	"context"
	"testing"
)

// A parser upgrade changes parse output for unchanged file contents. The
// upsert must report Changed so symbols/refs get rewritten on the next scan.
func TestUpsertFile_ParseHashChangeRewrites(t *testing.T) {
	ix := openTestIndex(t)
	ctx := context.Background()

	upsert := func(contentHash, parseHash []byte) UpsertFileResult {
		t.Helper()
		tx, err := ix.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		res, err := ix.UpsertFile(ctx, tx, "pkg/a.go", "go", 10, 1, contentHash, parseHash, 0)
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return res
	}

	if res := upsert([]byte{1}, []byte{10}); !res.Changed || res.WasPresent {
		t.Fatalf("first insert: got %+v, want Changed && !WasPresent", res)
	}
	if res := upsert([]byte{1}, []byte{10}); res.Changed {
		t.Fatalf("identical hashes: got Changed, want unchanged")
	}
	if res := upsert([]byte{1}, []byte{11}); !res.Changed {
		t.Fatalf("parse_hash changed with same content_hash: got unchanged, want Changed")
	}
	if res := upsert([]byte{2}, []byte{11}); !res.Changed {
		t.Fatalf("content_hash changed: got unchanged, want Changed")
	}
}
