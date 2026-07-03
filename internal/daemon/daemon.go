package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/registry"
	"github.com/datata1/mycelium/internal/service"
	"github.com/datata1/mycelium/internal/telemetry"
	"github.com/datata1/mycelium/internal/watch"
)

// Daemon bundles the long-running process: a file watcher feeding a pipeline,
// and a unix-socket server answering queries via internal/service.
type Daemon struct {
	Pipeline *pipeline.Pipeline
	Service  *service.Service
	Watcher  watch.Watcher
	Socket   string
	// Telemetry records per-call timing/byte stats when enabled. Defaults
	// to telemetry.Disabled{} so the dispatcher path is uniform — no
	// nil checks at every call site.
	Telemetry telemetry.Recorder
	// Log defaults to a text handler on stderr; set it to share a root
	// logger (or silence the daemon in tests).
	Log *slog.Logger
}

// log is the nil-safe accessor so handleConn works on a bare Daemon.
func (d *Daemon) log() *slog.Logger {
	if d.Log == nil {
		return discardLog
	}
	return d.Log
}

var discardLog = slog.New(slog.DiscardHandler)

// Run starts the watcher, listens on the unix socket, and blocks until ctx
// is cancelled. On shutdown the socket file is removed.
func (d *Daemon) Run(ctx context.Context) error {
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "daemon")
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

	// Watcher event pump: forward each change into the pipeline.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range d.Watcher.Events() {
			d.Log.Info("file change", "path", ev.RelPath, "removed", ev.Removed)
			if _, err := d.Pipeline.HandleChange(ctx, ev.RelPath, ev.AbsPath, ev.Removed); err != nil {
				d.Log.Error("handle change", "path", ev.RelPath, "err", err)
			}
		}
	}()

	d.Log.Info("listening", "socket", d.Socket)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			d.Log.Warn("accept", "err", err)
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
		d.writeErr(conn, fmt.Errorf("%w: decode request: %v", ipc.ErrBadParams, err))
		return
	}
	result, err := d.dispatch(ctx, req)
	if err != nil {
		d.writeErr(conn, err)
		return
	}
	d.writeOK(conn, result)
}

// HandleIPC is the daemon dispatcher exposed to other transports (HTTP).
// Keeps one source of truth for method routing.
func (d *Daemon) HandleIPC(ctx context.Context, req ipc.Request) (any, error) {
	return d.dispatch(ctx, req)
}

// dispatch wraps dispatchInner with v2.2 telemetry recording. The byte
// counts are slightly approximate — input is the raw JSON params slice
// the client sent, output is the marshaled result. We marshal here even
// though the transports also marshal, so the numbers are stable across
// unix/HTTP transports. Cost is ~microseconds on small payloads; tools
// returning multi-MB results would notice it, but those are uncommon
// and the extra fidelity in the log is worth it.
func (d *Daemon) dispatch(ctx context.Context, req ipc.Request) (any, error) {
	start := time.Now()
	result, err := d.dispatchInner(ctx, req)
	if d.Telemetry != nil {
		outBytes := 0
		if err == nil && result != nil {
			if b, mErr := json.Marshal(result); mErr == nil {
				outBytes = len(b)
			}
		}
		_ = d.Telemetry.Record(telemetry.Record{
			Timestamp:   start,
			Tool:        string(req.Method),
			InputBytes:  len(req.Params),
			OutputBytes: outBytes,
			DurationMS:  time.Since(start).Milliseconds(),
			OK:          err == nil,
		})
	}
	return result, err
}

func (d *Daemon) dispatchInner(ctx context.Context, req ipc.Request) (any, error) {
	switch req.Method {
	case ipc.MethodPing:
		return map[string]string{"status": "ok"}, nil

	// Reindex is the one write-path method; it stays outside the read
	// registry so the Service can remain read-only by construction.
	case ipc.MethodReindex:
		return d.Pipeline.RunOnce(ctx)
	}
	tool, ok := registry.Lookup(req.Method)
	if !ok {
		return nil, fmt.Errorf("%w %q", ipc.ErrUnknownMethod, req.Method)
	}
	return tool.Handle(ctx, d.Service, req.Params)
}

func (d *Daemon) writeOK(conn net.Conn, result any) {
	payload, err := json.Marshal(result)
	if err != nil {
		d.writeErr(conn, fmt.Errorf("marshal result: %w", err))
		return
	}
	resp := ipc.Response{OK: true, Result: payload}
	if err := json.NewEncoder(conn).Encode(&resp); err != nil {
		d.log().Warn("write response", "err", err)
	}
}

func (d *Daemon) writeErr(conn net.Conn, callErr error) {
	resp := ipc.Response{OK: false, Error: callErr.Error(), Code: ipc.CodeFor(callErr)}
	if err := json.NewEncoder(conn).Encode(&resp); err != nil {
		d.log().Warn("write response", "err", err)
	}
}
