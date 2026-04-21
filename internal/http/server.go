// Package http exposes the daemon's query surface over a loopback HTTP API.
// It's a thin wrapper around the same dispatcher the unix-socket server uses,
// so script clients and agents that don't speak MCP can still query the
// index with a plain `curl`.
package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jdwiederstein/mycelium/internal/ipc"
)

// Dispatcher is the subset of daemon.Daemon that the HTTP server needs.
// We take it as an interface to avoid an import cycle (daemon imports http).
type Dispatcher interface {
	HandleIPC(ctx context.Context, req ipc.Request) (any, error)
}

// Server is a loopback HTTP API. Start binds a listener on 127.0.0.1:<port>;
// Close shuts the server down cleanly. The server exposes two routes:
//
//   POST /rpc            - body: {"method": "...", "params": {...}}
//   POST /<method>       - body: params object; method inferred from path
//
// Both return {"ok": true, "result": ...} or {"ok": false, "error": "..."}.
type Server struct {
	Port       int
	Dispatcher Dispatcher
	Logger     func(format string, args ...any)

	srv  *http.Server
	ln   net.Listener
	once sync.Once
}

func (s *Server) Start(ctx context.Context) error {
	if s.Port <= 0 {
		return nil // HTTP API disabled by config
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", s.handleRPC)
	mux.HandleFunc("/", s.handlePath)

	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(s.Port))
	if err != nil {
		return fmt.Errorf("listen :%d: %w", s.Port, err)
	}
	s.ln = ln
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed && s.Logger != nil {
			s.Logger("http server: %v", err)
		}
	}()
	return nil
}

func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		if s.srv != nil {
			shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err = s.srv.Shutdown(shutCtx)
		}
	})
	return err
}

// Addr returns the bound address after Start, or empty if not started.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// handleRPC accepts the same {method, params} shape as the unix socket.
// Useful for scripts that don't want to construct different URLs per method.
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ipc.Request
	if err := json.NewDecoder(bufio.NewReader(r.Body)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ipc.Response{OK: false, Error: "decode request: " + err.Error()})
		return
	}
	s.dispatch(w, r.Context(), req)
}

// handlePath infers the method from the URL path: POST /find_symbol with the
// params as the JSON body. Lets agents do: curl -d '{"name":"X"}' /find_symbol.
func (s *Server) handlePath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	method := r.URL.Path
	if len(method) > 0 && method[0] == '/' {
		method = method[1:]
	}
	if method == "" {
		http.Error(w, "missing method in path", http.StatusBadRequest)
		return
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r.Body); err != nil {
		writeJSON(w, http.StatusBadRequest, ipc.Response{OK: false, Error: "read body: " + err.Error()})
		return
	}
	req := ipc.Request{Method: method, Params: buf.Bytes()}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage("{}")
	}
	s.dispatch(w, r.Context(), req)
}

func (s *Server) dispatch(w http.ResponseWriter, ctx context.Context, req ipc.Request) {
	result, err := s.Dispatcher.HandleIPC(ctx, req)
	if err != nil {
		writeJSON(w, http.StatusOK, ipc.Response{OK: false, Error: err.Error()})
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ipc.Response{OK: false, Error: "marshal: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ipc.Response{OK: true, Result: payload})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
