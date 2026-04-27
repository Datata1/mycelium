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

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/gitref"
	"github.com/jdwiederstein/mycelium/internal/ipc"
	"github.com/jdwiederstein/mycelium/internal/pipeline"
	"github.com/jdwiederstein/mycelium/internal/query"
	"github.com/jdwiederstein/mycelium/internal/telemetry"
	"github.com/jdwiederstein/mycelium/internal/watch"
)

// Daemon bundles the long-running process: a file watcher feeding a pipeline,
// and a unix-socket server answering queries via internal/query.
type Daemon struct {
	Pipeline *pipeline.Pipeline
	Reader   *query.Reader
	Embedder embed.Embedder // required for search_semantic; may be Noop
	Watcher  watch.Watcher
	Socket   string
	RepoRoot string // absolute path; lexical search needs it to open files
	// VSSTable is the sqlite-vec virtual table name (or "" when not
	// configured). Plumbed into every Searcher the daemon creates so the
	// KNN fast path can light up.
	VSSTable string
	// Telemetry records per-call timing/byte stats when enabled. Defaults
	// to telemetry.Disabled{} so the dispatcher path is uniform — no
	// nil checks at every call site.
	Telemetry telemetry.Recorder
	// SkillsRegen, when non-nil, is invoked after each debounced batch
	// of file changes settles (v2.5). The argument is the deduplicated
	// set of package directories touched in that batch. cmd/myco wires
	// this to skills.RegenerateAffected when the user has compiled the
	// skills tree at least once. nil = no incremental regen.
	SkillsRegen func(ctx context.Context, packages []string) error
	// SkillsDebounce is the idle window after the last watcher event
	// before SkillsRegen fires. Defaults to 200ms — long enough to
	// coalesce a multi-file save (e.g. `goimports` rewriting five
	// files), short enough to feel snappy on isolated edits.
	SkillsDebounce time.Duration
	Logger         Logger
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

	// v2.5 debounce: collect every package dir touched between watcher
	// events, fire SkillsRegen when the channel is idle for
	// SkillsDebounce. Buffered at 1 so a slow regen doesn't stall the
	// pump; the next fire collapses any new dirs into the same call.
	debounce := d.SkillsDebounce
	if debounce <= 0 {
		debounce = 200 * time.Millisecond
	}
	flushCh := make(chan map[string]struct{}, 1)

	// Watcher event pump.
	wg.Add(1)
	go func() {
		defer wg.Done()
		var (
			pending = map[string]struct{}{}
			timer   *time.Timer
			timerC  <-chan time.Time
		)
		armTimer := func() {
			if timer == nil {
				timer = time.NewTimer(debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
			}
			timerC = timer.C
		}
		for {
			select {
			case ev, ok := <-d.Watcher.Events():
				if !ok {
					if len(pending) > 0 && d.SkillsRegen != nil {
						select {
						case flushCh <- pending:
						default:
						}
					}
					close(flushCh)
					return
				}
				d.Logger.Printf("change %s (removed=%v)", ev.RelPath, ev.Removed)
				if _, err := d.Pipeline.HandleChange(ctx, ev.RelPath, ev.AbsPath, ev.Removed); err != nil {
					d.Logger.Printf("handle %s: %v", ev.RelPath, err)
					continue
				}
				if d.SkillsRegen != nil {
					pending[filepath.ToSlash(filepath.Dir(ev.RelPath))] = struct{}{}
					armTimer()
				}
			case <-timerC:
				timerC = nil
				if len(pending) == 0 {
					continue
				}
				batch := pending
				pending = map[string]struct{}{}
				select {
				case flushCh <- batch:
				default:
					// Previous flush still in flight — merge into the
					// pending pool and re-arm so we try again next idle.
					for k := range batch {
						pending[k] = struct{}{}
					}
					armTimer()
				}
			}
		}
	}()

	// Skills regen worker: serialises SkillsRegen calls so two bursts
	// can't race on the same .mycelium/skills/ tree.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for batch := range flushCh {
			if d.SkillsRegen == nil || len(batch) == 0 {
				continue
			}
			pkgs := make([]string, 0, len(batch))
			for k := range batch {
				pkgs = append(pkgs, k)
			}
			if err := d.SkillsRegen(ctx, pkgs); err != nil {
				d.Logger.Printf("skills regen: %v", err)
			} else {
				d.Logger.Printf("skills regen: %d package(s) checked", len(pkgs))
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
			Tool:        req.Method,
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

	case ipc.MethodSearchSemantic:
		var p ipc.SearchSemanticParams
		if err := unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		paths, err := d.resolveSince(ctx, p.Since)
		if err != nil {
			return nil, err
		}
		s := &query.Searcher{Reader: d.Reader, Embedder: d.Embedder, VSSTable: d.VSSTable}
		return s.SearchSemantic(ctx, p.Query, p.K, p.Kind, p.PathContains, p.Project, paths)

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
		dir := query.Direction(p.Direction)
		if dir == "" {
			dir = query.DirBoth
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

	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
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
