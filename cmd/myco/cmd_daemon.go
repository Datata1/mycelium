package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/datata1/mycelium/internal/daemon"
	mychttp "github.com/datata1/mycelium/internal/http"
	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/parser/python"
	"github.com/datata1/mycelium/internal/parser/typescript"
	"github.com/datata1/mycelium/internal/pipeline"
	"github.com/datata1/mycelium/internal/query"
	"github.com/datata1/mycelium/internal/repo"
	"github.com/datata1/mycelium/internal/telemetry"
	"github.com/datata1/mycelium/internal/watch"
)

func newDaemonCmd() *cobra.Command {
	var backendOverride string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived indexer + query server for this repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runDaemon(ctx, backendOverride)
		},
	}
	cmd.Flags().StringVar(&backendOverride, "watcher-backend", "",
		"override watcher backend (fsnotify | watchman); defaults to config")
	return cmd
}

func runDaemon(ctx context.Context, backendOverride string) error {
	rc, err := loadRepoCtx()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dlog := log.With("component", "daemon")

	// v4 T2: bump RLIMIT_NOFILE soft → hard before anything opens fds.
	// Failure is non-fatal; daemon continues at the original limit and
	// the doctor check will warn when fd usage gets close.
	if soft, hard, rerr := daemon.RaiseFileDescriptorLimit(); rerr != nil {
		dlog.Warn("could not raise RLIMIT_NOFILE; continuing at default", "err", rerr)
	} else if soft > 0 {
		dlog.Info("RLIMIT_NOFILE raised", "soft", soft, "hard", hard)
	}

	// Write a PID file so `myco doctor` can probe /proc/<pid>/fd for
	// fd-headroom warnings. Best-effort; failure to write doesn't stop
	// the daemon. MkdirAll here because openIndex hasn't run yet.
	pidPath := filepath.Join(rc.AbsStateDir(), "daemon.pid")
	_ = os.MkdirAll(rc.AbsStateDir(), 0o755)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		dlog.Warn("could not write pid file", "path", pidPath, "err", err)
	} else {
		defer os.Remove(pidPath)
	}

	ix, err := openIndex(rc)
	if err != nil {
		return err
	}
	defer ix.Close()

	reg := parser.NewRegistry()
	for _, lang := range rc.Cfg.Languages {
		switch lang {
		case "go":
			reg.Register(golang.New())
		case "typescript":
			reg.Register(typescript.New())
		case "python":
			reg.Register(python.New())
		}
	}

	w := repo.NewWalker(rc.Root, rc.Cfg.Include, rc.Cfg.Exclude, rc.Cfg.Index.MaxFileSizeKB)
	resolvers := loadResolvers(rc.Root, rc.Cfg.Languages)
	wss, projFor, err := buildWorkspaces(ctx, rc, ix)
	if err != nil {
		return err
	}
	p := &pipeline.Pipeline{
		Index: ix, Registry: reg, Walker: w,
		Resolvers: resolvers, Workspaces: wss, FileProjectFor: projFor,
		Documents: buildDocumentRegistry(),
	}

	// Catch-up scan before accepting connections so the index reflects
	// any changes that happened while the daemon was down.
	dlog.Info("catch-up scan starting (this may take a while on first run)")
	if rep, err := p.RunOnce(ctx); err != nil {
		return fmt.Errorf("catch-up scan: %w", err)
	} else {
		dlog.Info("catch-up done",
			"scanned", rep.FilesScanned, "changed", rep.FilesChanged, "duration", rep.Duration)
	}

	backend := watch.Backend(rc.Cfg.Watcher.Backend)
	if backendOverride != "" {
		backend = watch.Backend(backendOverride)
	}
	wat, err := watch.New(watch.Options{
		Root:          rc.Root,
		Include:       rc.Cfg.Include,
		Exclude:       rc.Cfg.Exclude,
		MaxFileSizeKB: rc.Cfg.Index.MaxFileSizeKB,
		DebounceMS:    rc.Cfg.Watcher.DebounceMS,
		CoalesceMS:    rc.Cfg.Watcher.CoalesceMS,
		Backend:       backend,
	})
	if err != nil {
		return err
	}

	// v2.2: opt-in telemetry. Open failures fall back to Disabled rather
	// than aborting daemon startup — observability shouldn't gate
	// availability.
	var rec telemetry.Recorder = telemetry.Disabled{}
	if rc.Cfg.Telemetry.Enabled {
		path := rc.Cfg.Telemetry.Path
		if path == "" {
			path = filepath.Join(rc.AbsStateDir(), "telemetry.jsonl")
		}
		fr, err := telemetry.Open(path)
		if err != nil {
			dlog.Warn("telemetry disabled", "err", err)
		} else {
			sessionFile := filepath.Join(rc.AbsStateDir(), "current_session.json")
			fr.SetSessionFile(sessionFile)
			rec = fr
			dlog.Info("telemetry on", "path", path)
			defer fr.Close()
		}
	}

	d := &daemon.Daemon{
		Pipeline:  p,
		Reader:    query.NewReader(ix.DB()),
		Watcher:   wat,
		Socket:    rc.AbsSocketPath(),
		RepoRoot:  rc.Root,
		Telemetry: rec,
		Log:       dlog,
	}

	// Start the HTTP transport alongside the unix socket. Disabled when
	// config.daemon.http_port = 0.
	if rc.Cfg.Daemon.HTTPPort > 0 {
		httpSrv := &mychttp.Server{
			Port:       rc.Cfg.Daemon.HTTPPort,
			Dispatcher: d,
			Log:        log.With("component", "http"),
		}
		if err := httpSrv.Start(ctx); err != nil {
			return err
		}
		dlog.Info("http api listening", "addr", fmt.Sprintf("127.0.0.1:%d", rc.Cfg.Daemon.HTTPPort))
		defer httpSrv.Close()
	}

	return d.Run(ctx)
}
