package watchman

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// FileChange is one entry in a subscription delivery: the
// repo-relative path plus exist/new bits that we fold into our own
// Removed semantics at the adapter layer.
type FileChange struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	New    bool   `json:"new"`
}

// Subscription is one active watch on a repo root. It owns the
// channel of FileChange batches streaming from watchman. Close()
// unsubscribes cleanly and tears down the client connection.
type Subscription struct {
	client *Client
	name   string
	root   string
	out    chan []FileChange
	done   chan struct{}
}

// Subscribe opens a watchman connection, calls watch-project, and
// starts a subscription named `subName`. File changes are delivered
// as batches on the returned channel. The channel closes when the
// subscription errors out or the caller Closes the Subscription.
//
// We deliberately keep the expression minimal (type: file) so both
// backends deliver the same event shape; our common wrapper handles
// include/exclude filtering identically for fsnotify and watchman.
func Subscribe(ctx context.Context, sockname, absRoot, subName string) (*Subscription, error) {
	c, err := Dial(sockname)
	if err != nil {
		return nil, err
	}
	sub := &Subscription{
		client: c,
		name:   subName,
		out:    make(chan []FileChange, 64),
		done:   make(chan struct{}),
	}
	// watch-project might return an ancestor directory — if the repo
	// root lives inside an enclosing watched root, watchman will tell
	// us so and give us a relative_path. We use the reported watch
	// root so subscription names line up, and stash the relative_path
	// so we can translate incoming names back to mycelium-relative.
	wp, err := sub.watchProject(absRoot)
	if err != nil {
		c.Close()
		return nil, err
	}
	sub.root = wp.Watch
	// Build the subscription request. `expression: ["type","f"]`
	// keeps directory events off the stream — we don't care about
	// them and they'd just waste filter cycles. `fields` narrows the
	// per-file payload to what we actually consume.
	reqExpr := map[string]any{
		"expression": []any{"type", "f"},
		"fields":     []any{"name", "exists", "new"},
	}
	if wp.RelativePath != "" {
		reqExpr["relative_root"] = wp.RelativePath
	}
	if _, err := c.Send([]any{"subscribe", wp.Watch, subName, reqExpr}); err != nil {
		c.Close()
		return nil, fmt.Errorf("watchman subscribe: %w", err)
	}
	go sub.pump(ctx, wp.RelativePath)
	return sub, nil
}

// Updates is the batch-of-FileChange channel. Each delivery from
// watchman is one slice. Downstream code is expected to fan out.
func (s *Subscription) Updates() <-chan []FileChange { return s.out }

// Errors surfaces read-pump errors from the underlying client. One
// error means the connection is gone; the caller should tear down
// the subscription and report to the daemon.
func (s *Subscription) Errors() <-chan error { return s.client.ReadErrors() }

// Close unsubscribes and closes the connection. Multiple calls are
// safe — subsequent Close()s just return nil.
func (s *Subscription) Close() error {
	select {
	case <-s.done:
		return nil
	default:
	}
	close(s.done)
	// Best-effort unsubscribe. We ignore the response — the server
	// will drop the subscription regardless when the connection
	// closes anyway.
	_, _ = s.client.Send([]any{"unsubscribe", s.root, s.name})
	return s.client.Close()
}

// watchProjectResult mirrors watchman's watch-project reply. `Watch`
// is the absolute path watchman is actually watching (it may be an
// ancestor of the path you asked for); `RelativePath` is how far
// your asked path sits below that root.
type watchProjectResult struct {
	Watch        string `json:"watch"`
	RelativePath string `json:"relative_path"`
	Error        string `json:"error"`
}

func (s *Subscription) watchProject(root string) (watchProjectResult, error) {
	var wp watchProjectResult
	raw, err := s.client.Send([]any{"watch-project", root})
	if err != nil {
		return wp, err
	}
	if err := json.Unmarshal(raw, &wp); err != nil {
		return wp, fmt.Errorf("watch-project parse: %w", err)
	}
	if wp.Error != "" {
		return wp, fmt.Errorf("watch-project: %s", wp.Error)
	}
	if wp.Watch == "" {
		return wp, fmt.Errorf("watch-project: empty watch root in reply")
	}
	return wp, nil
}

// pump drains subscription deliveries from the client, extracts the
// "files" array, and forwards it on s.out. Names reported by watchman
// are already relative to the watched root; we strip the relative_root
// prefix back so callers get mycelium-repo-relative paths.
func (s *Subscription) pump(ctx context.Context, relativeRoot string) {
	defer close(s.out)
	in := s.client.Deliveries()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case pdu, ok := <-in:
			if !ok {
				return
			}
			var env struct {
				Subscription string       `json:"subscription"`
				Files        []FileChange `json:"files"`
				IsFreshInstance bool       `json:"is_fresh_instance"`
			}
			if err := json.Unmarshal(pdu, &env); err != nil {
				// Skip garbled frames rather than tearing down the
				// subscription. If the server is really broken the
				// ReadErrors channel will fire shortly.
				continue
			}
			if env.Subscription != s.name {
				continue
			}
			// Fresh-instance deliveries dump every file in the tree.
			// We don't filter them out: the daemon's catch-up logic
			// already hashes content before re-writing, so dupes cost
			// nothing.
			if len(env.Files) == 0 {
				continue
			}
			// Normalize names to forward slashes and strip the
			// subscription-relative root prefix. filepath.ToSlash
			// handles Windows separators if we ever support them.
			out := env.Files
			if relativeRoot != "" {
				out = make([]FileChange, 0, len(env.Files))
				for _, f := range env.Files {
					f.Name = filepath.ToSlash(f.Name)
					out = append(out, f)
				}
			} else {
				for i := range out {
					out[i].Name = filepath.ToSlash(out[i].Name)
				}
			}
			select {
			case s.out <- out:
			case <-s.done:
				return
			}
		}
	}
}
