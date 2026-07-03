package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/datata1/mycelium/internal/index"
	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/query"
)

func testDaemon(t *testing.T) *Daemon {
	t.Helper()
	ix, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	return &Daemon{Reader: query.NewReader(ix.DB()), Logger: NewStderrLogger()}
}

// callPipe drives handleConn over an in-memory pipe: one request in, one
// response out — the wire protocol without a real socket (which sandboxed
// environments may forbid binding).
func callPipe(t *testing.T, d *Daemon, method string, params any) ipc.Response {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handleConn(context.Background(), server)
	}()

	req := ipc.Request{Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		req.Params = b
	}
	if err := json.NewEncoder(client).Encode(&req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var resp ipc.Response
	if err := json.NewDecoder(bufio.NewReader(client)).Decode(&resp); err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = client.Close()
	<-done
	return resp
}

// TestDispatchErrorCodes proves the daemon maps sentinel errors to wire
// codes: not_found for missing targets, unknown_method for bogus methods,
// bad_params for mistyped params — with the message preserved verbatim.
func TestDispatchErrorCodes(t *testing.T) {
	d := testDaemon(t)

	if resp := callPipe(t, d, ipc.MethodPing, nil); !resp.OK {
		t.Fatalf("ping failed: %s", resp.Error)
	}

	cases := []struct {
		name     string
		method   string
		params   any
		wantCode string
	}{
		{"missing symbol", ipc.MethodGetNeighborhood, ipc.GetNeighborhoodParams{Target: "NoSuchSymbol"}, ipc.CodeNotFound},
		{"bogus method", "bogus_method", nil, ipc.CodeUnknownMethod},
		{"mistyped params", ipc.MethodFindSymbol, map[string]any{"name": 42}, ipc.CodeBadParams},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := callPipe(t, d, c.method, c.params)
			if resp.OK {
				t.Fatal("expected an error response")
			}
			if resp.Code != c.wantCode {
				t.Errorf("code = %q, want %q (error: %s)", resp.Code, c.wantCode, resp.Error)
			}
			if resp.Error == "" {
				t.Error("error message must not be empty")
			}
		})
	}
}

// TestErrorCodeSocketRoundTrip is the same contract end-to-end through a
// real unix socket and ipc.Client: errors.Is matches on the caller side.
func TestErrorCodeSocketRoundTrip(t *testing.T) {
	d := testDaemon(t)

	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("unix socket bind not permitted in this environment: %v", err)
		}
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.handleConn(context.Background(), conn)
		}
	}()

	c := ipc.NewClient(sock)
	if err := c.Call(ipc.MethodPing, nil, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}

	err = c.Call(ipc.MethodGetNeighborhood, ipc.GetNeighborhoodParams{Target: "NoSuchSymbol"}, nil)
	if !errors.Is(err, ipc.ErrNotFound) {
		t.Errorf("missing symbol: want ErrNotFound, got %v", err)
	}
	err = c.Call("bogus_method", nil, nil)
	if !errors.Is(err, ipc.ErrUnknownMethod) {
		t.Errorf("bogus method: want ErrUnknownMethod, got %v", err)
	}
	err = c.Call(ipc.MethodFindSymbol, map[string]any{"name": 42}, nil)
	if !errors.Is(err, ipc.ErrBadParams) {
		t.Errorf("mistyped params: want ErrBadParams, got %v", err)
	}
}
