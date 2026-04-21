package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jdwiederstein/mycelium/internal/ipc"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/watch"
)

// Daemon bundles the long-running process: a file watcher feeding a pipeline,
// and a unix-socket server answering queries via internal/query.
type Daemon struct {
	Pipeline *pipeline.Pipeline
	Reader   *query.Reader
	Watcher  *watch.Watcher
	Socket   string
	Logger   Logger
}

type Logger interface {
	Printf(format string, args ...any)
}

type stderrLogger struct{}

func (stderrLogger) Printf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[daemon] "+format+"\n", args...)
}

// NewStderrLogger is a convenience default.
func NewStderrLogger() Logger { return stderrLogger{} }

// Run starts the watcher, listens on the unix socket, and blocks until ctx
// is cancelled. On shutdown the socket file is removed.
func (d *Daemon) Run(ctx context.Context) error {
	if d.Logger == nil {
		d.Logger = NewStderrLogger()
	}
	if err := os.MkdirAll(filepath.Dir(d.Socket), 0o755); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}
	// Remove stale socket from a previous run. Fresh unix socket per daemon.
	_ = os.Remove(d.Socket)

	ln, err := net.Listen("unix", d.Socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.Socket, err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(d.Socket)
	}()

	if err := d.Watcher.Start(ctx); err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}

	// Close the listener when ctx is done so Accept unblocks.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = d.Watcher.Close()
	}()

	var wg sync.WaitGroup

	// Watcher event pump.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range d.Watcher.Events() {
			d.Logger.Printf("change %s (removed=%v)", ev.RelPath, ev.Removed)
			if _, err := d.Pipeline.HandleChange(ctx, ev.RelPath, ev.AbsPath, ev.Removed); err != nil {
				d.Logger.Printf("handle %s: %v", ev.RelPath, err)
			}
		}
	}()

	d.Logger.Printf("listening on %s", d.Socket)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			d.Logger.Printf("accept error: %v", err)
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			d.handleConn(ctx, c)
		}(conn)
	}
	wg.Wait()
	return nil
}

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	var req ipc.Request
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		writeErr(conn, fmt.Sprintf("decode request: %v", err))
		return
	}
	result, err := d.dispatch(ctx, req)
	if err != nil {
		writeErr(conn, err.Error())
		return
	}
	writeOK(conn, result)
}

func (d *Daemon) dispatch(ctx context.Context, req ipc.Request) (any, error) {
	switch req.Method {
	case ipc.MethodPing:
		return map[string]string{"status": "ok"}, nil

	case ipc.MethodFindSymbol:
		var p ipc.FindSymbolParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.FindSymbol(ctx, p.Name, p.Kind, p.Limit)

	case ipc.MethodGetReferences:
		var p ipc.GetReferencesParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.GetReferences(ctx, p.Target, p.Limit)

	case ipc.MethodListFiles:
		var p ipc.ListFilesParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.ListFiles(ctx, p.Language, p.NameContains, p.Limit)

	case ipc.MethodGetFileOutline:
		var p ipc.GetFileOutlineParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.GetFileOutline(ctx, p.Path)

	case ipc.MethodStats:
		return d.Reader.Stats(ctx)

	case ipc.MethodReindex:
		return d.Pipeline.RunOnce(ctx)

	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}

func unmarshal(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func writeOK(conn net.Conn, result any) {
	payload, err := json.Marshal(result)
	if err != nil {
		writeErr(conn, fmt.Sprintf("marshal result: %v", err))
		return
	}
	resp := ipc.Response{OK: true, Result: payload}
	_ = json.NewEncoder(conn).Encode(&resp)
}

func writeErr(conn net.Conn, msg string) {
	resp := ipc.Response{OK: false, Error: msg}
	_ = json.NewEncoder(conn).Encode(&resp)
}
