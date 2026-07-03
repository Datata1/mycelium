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

	"github.com/datata1/mycelium/internal/gitref"
	"github.com/datata1/mycelium/internal/ipc"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/telemetry"
	"github.com/datata1/mycelium/internal/watch"
)

// Daemon bundles the long-running process: a file watcher feeding a pipeline,
// and a unix-socket server answering queries via internal/query.
type Daemon struct {
	Pipeline *pipeline.Pipeline
	Reader   *query.Reader
	Watcher  watch.Watcher
	Socket   string
	RepoRoot string // absolute path; lexical search needs it to open files
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

	case ipc.MethodFindSymbol:
		var p ipc.FindSymbolParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		paths, err := d.resolveSince(ctx, p.Since)
		if err != nil {
			return nil, err
		}
		return d.Reader.FindSymbol(ctx, p.Name, p.Kind, p.Project, p.Limit, paths, p.Focus)

	case ipc.MethodGetReferences:
		var p ipc.GetReferencesParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		paths, err := d.resolveSince(ctx, p.Since)
		if err != nil {
			return nil, err
		}
		return d.Reader.GetReferences(ctx, p.Target, p.Project, p.Limit, paths)

	case ipc.MethodListFiles:
		var p ipc.ListFilesParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		paths, err := d.resolveSince(ctx, p.Since)
		if err != nil {
			return nil, err
		}
		return d.Reader.ListFiles(ctx, p.Language, p.NameContains, p.Project, p.Limit, paths)

	case ipc.MethodGetFileOutline:
		var p ipc.GetFileOutlineParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.GetFileOutline(ctx, p.Path, p.Focus)

	case ipc.MethodStats:
		return d.Reader.Stats(ctx)

	case ipc.MethodReindex:
		return d.Pipeline.RunOnce(ctx)

	case ipc.MethodSearchLexical:
		var p ipc.SearchLexicalParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		paths, err := d.resolveSince(ctx, p.Since)
		if err != nil {
			return nil, err
		}
		return d.Reader.SearchLexical(ctx, p.Pattern, p.PathContains, p.Project, p.K, d.RepoRoot, paths)

	case ipc.MethodGetFileSummary:
		var p ipc.GetFileSummaryParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.GetFileSummary(ctx, p.Path)

	case ipc.MethodGetNeighborhood:
		var p ipc.GetNeighborhoodParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		dir, err := query.ParseDirection(p.Direction)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ipc.ErrBadParams, err)
		}
		return d.Reader.GetNeighborhood(ctx, p.Target, p.Project, p.Depth, dir, p.Focus)

	case ipc.MethodImpactAnalysis:
		var p ipc.ImpactAnalysisParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		paths, err := d.resolveSince(ctx, p.Since)
		if err != nil {
			return nil, err
		}
		return d.Reader.ImpactAnalysis(ctx, p.Target, p.Kind, p.Project, p.Depth, paths)

	case ipc.MethodCriticalPath:
		var p ipc.CriticalPathParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.CriticalPath(ctx, p.From, p.To, p.Project, p.Depth, p.K)

	case ipc.MethodReadFocused:
		var p ipc.ReadFocusedParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.ReadFocused(ctx, d.RepoRoot, p.Path, p.Focus)

	case ipc.MethodFindDocumentKey:
		var p ipc.FindDocumentKeyParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		return d.Reader.FindDocumentKey(ctx, p.Key, p.Kind, p.Project, p.Limit)

	default:
		return nil, fmt.Errorf("%w %q", ipc.ErrUnknownMethod, req.Method)
	}
}

// resolveSince turns the optional git-ref string into a resolved path
// list. Empty ref -> nil (unscoped). Git errors surface to the caller
// rather than silently becoming an empty filter.
func (d *Daemon) resolveSince(ctx context.Context, ref string) ([]string, error) {
	if ref == "" {
		return nil, nil
	}
	return gitref.ResolveSince(ctx, d.RepoRoot, ref)
}

func unmarshal(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %v", ipc.ErrBadParams, err)
	}
	return nil
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
