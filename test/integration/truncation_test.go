// Integration tests for truncation transparency: a result that hit its
// limit must say so via Hints, and an uncapped result must not.
package integration_test

import (
	"context"
	"strings"
	"testing"
)

func hasTruncationHint(hints []string) bool {
	for _, h := range hints {
		if strings.Contains(h, "more exist") {
			return true
		}
	}
	return false
}

func TestIntegration_TruncationHints(t *testing.T) {
	t.Parallel()
	_, reader := setupGraphFixture(t)
	ctx := context.Background()

	t.Run("find_symbol_capped", func(t *testing.T) {
		// Substring "e" matches several fixture symbols; limit 1 must
		// truncate and say so.
		res, err := reader.FindSymbol(ctx, "e", "", "", 1, nil, "")
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if len(res.Matches) != 1 {
			t.Fatalf("matches = %d, want exactly 1 (limit)", len(res.Matches))
		}
		if !hasTruncationHint(res.Hints) {
			t.Errorf("expected truncation hint on capped result; hints = %v", res.Hints)
		}
	})

	t.Run("find_symbol_uncapped", func(t *testing.T) {
		res, err := reader.FindSymbol(ctx, "e", "", "", 500, nil, "")
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if len(res.Matches) == 0 {
			t.Fatal("expected matches for substring \"e\"")
		}
		if hasTruncationHint(res.Hints) {
			t.Errorf("unexpected truncation hint on uncapped result; hints = %v", res.Hints)
		}
	})

	t.Run("list_files_capped", func(t *testing.T) {
		res, err := reader.ListFiles(ctx, "", "", "", 1, nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(res.Matches) != 1 {
			t.Fatalf("matches = %d, want exactly 1 (limit)", len(res.Matches))
		}
		if !hasTruncationHint(res.Hints) {
			t.Errorf("expected truncation hint on capped result; hints = %v", res.Hints)
		}
	})

	t.Run("list_files_uncapped", func(t *testing.T) {
		res, err := reader.ListFiles(ctx, "", "", "", 500, nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if hasTruncationHint(res.Hints) {
			t.Errorf("unexpected truncation hint on uncapped result; hints = %v", res.Hints)
		}
	})
}
