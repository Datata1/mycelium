package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/languages"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/repo"
	"github.com/datata1/mycelium/internal/service"
)

const (
	eqService = `package auth

// Login authenticates a user.
func Login(user string) error { return nil }

type Session struct{ ID string }
`
	eqCaller = `package app

import "example/auth"

func Run() error { return auth.Login("bob") }
`
)

// fixtureService indexes two Go files into a temp SQLite index and
// returns a read-only Service plus the repo root.
func fixtureService(t *testing.T) (*service.Service, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range map[string]string{
		"auth/service.go": eqService,
		"app/run.go":      eqCaller,
	} {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	ix, err := index.Open(filepath.Join(root, ".mycelium", "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	p := &pipeline.Pipeline{
		Index:    ix,
		Registry: languages.Registry([]string{"go"}),
		Walker:   repo.NewWalker(root, []string{"**/*.go"}, nil, 0),
	}
	if _, err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("index: %v", err)
	}
	return service.NewReadOnly(ix, root, nil), root
}

// TestDualPathEquivalence is the permanent guard on the CLI dual path
// (plans/refac/04): every query goes over the daemon socket when the
// daemon is up, else through a local Service. Both must return
// byte-identical JSON. The daemon-up side is driven over an in-memory
// pipe through the real handleConn→dispatch→registry chain (no socket
// bind, so it runs everywhere); the daemon-down side calls the Service
// directly, exactly as the CLI fallback does.
func TestDualPathEquivalence(t *testing.T) {
	svc, _ := fixtureService(t)
	d := &Daemon{Service: svc}
	ctx := context.Background()
	// Each case pairs the wire method+params (daemon-up path) with the
	// direct Service call the CLI fallback makes when the daemon is down.
	cases := []struct {
		name   string
		method ipc.Method
		params any
		direct func() (any, error)
	}{
		{"stats", ipc.MethodStats, nil,
			func() (any, error) { return svc.Stats(ctx) }},
		{"find_symbol", ipc.MethodFindSymbol, ipc.FindSymbolParams{Name: "Login"},
			func() (any, error) { return svc.FindSymbol(ctx, ipc.FindSymbolParams{Name: "Login"}) }},
		{"find_symbol_miss", ipc.MethodFindSymbol, ipc.FindSymbolParams{Name: "Login", Kind: "interface"},
			func() (any, error) {
				return svc.FindSymbol(ctx, ipc.FindSymbolParams{Name: "Login", Kind: "interface"})
			}},
		{"get_references", ipc.MethodGetReferences, ipc.GetReferencesParams{Target: "Login"},
			func() (any, error) { return svc.GetReferences(ctx, ipc.GetReferencesParams{Target: "Login"}) }},
		{"get_file_summary", ipc.MethodGetFileSummary, ipc.GetFileSummaryParams{Path: "auth/service.go"},
			func() (any, error) {
				return svc.GetFileSummary(ctx, ipc.GetFileSummaryParams{Path: "auth/service.go"})
			}},
		{"get_neighborhood", ipc.MethodGetNeighborhood, ipc.GetNeighborhoodParams{Target: "Run"},
			func() (any, error) { return svc.GetNeighborhood(ctx, ipc.GetNeighborhoodParams{Target: "Run"}) }},
		{"list_files", ipc.MethodListFiles, ipc.ListFilesParams{},
			func() (any, error) { return svc.ListFiles(ctx, ipc.ListFilesParams{}) }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Daemon-up path: through handleConn over an in-memory pipe.
			resp := callPipe(t, d, c.method, c.params)
			if !resp.OK {
				t.Fatalf("socket path error: %s", resp.Error)
			}
			overSocket := resp.Result

			// Daemon-down path: the local Service call the CLI falls back to.
			result, err := c.direct()
			if err != nil {
				t.Fatalf("direct service call: %v", err)
			}
			direct, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal direct: %v", err)
			}

			if normalizeJSON(t, overSocket) != normalizeJSON(t, direct) {
				t.Errorf("dual-path mismatch for %s\nsocket: %s\ndirect: %s",
					c.name, overSocket, direct)
			}
		})
	}
}

// normalizeJSON round-trips through a generic decode so key ordering and
// whitespace can't cause spurious mismatches.
func normalizeJSON(t *testing.T, b []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	return string(out)
}
