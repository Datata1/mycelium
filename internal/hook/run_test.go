package hook

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"

	"github.com/datata1/mycelium/internal/ipc"
)

func TestRun_NoopWhenDaemonDown(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	if err := Run(context.Background(), sock); err != nil {
		t.Fatalf("daemon-down Run should be a silent no-op; got %v", err)
	}
}

func TestRun_SendsOneReindex(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot bind unix socket in this environment: %v", err)
	}
	defer l.Close()

	methods := make(chan ipc.Method, 8)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			var req ipc.Request
			if err := json.NewDecoder(conn).Decode(&req); err == nil {
				methods <- req.Method
				resp := ipc.Response{OK: true, Result: json.RawMessage(`{}`)}
				_ = json.NewEncoder(conn).Encode(&resp)
			}
			conn.Close()
		}
	}()

	if err := Run(context.Background(), sock); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Run pings first (IsReachable), then issues the reindex.
	var got []ipc.Method
	close(methods)
	for m := range methods {
		got = append(got, m)
	}
	var reindexes int
	for _, m := range got {
		if m == ipc.MethodReindex {
			reindexes++
		}
	}
	if reindexes != 1 {
		t.Errorf("daemon saw %d reindex calls (methods: %v), want exactly 1", reindexes, got)
	}
}
