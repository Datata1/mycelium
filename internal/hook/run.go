package hook

import (
	"context"
	"fmt"

	"github.com/datata1/mycelium/internal/ipc"
)

// Run is the body of every `myco hook <name>` command. It asks the daemon
// to reindex; RunOnce reconciles fully (upsert + prune), so one generic
// ping covers commits, checkouts, merges, and rewrites alike. If the
// daemon is not running this is a no-op — the startup catch-up scan
// reconciles the same changes, and a one-shot reindex here would compete
// with the user's next git operation for file handles.
func Run(ctx context.Context, socketPath string) error {
	c := ipc.NewClient(socketPath)
	if !c.IsReachable() {
		return nil
	}
	var out any
	if err := c.Call(ipc.MethodReindex, nil, &out); err != nil {
		return fmt.Errorf("reindex via daemon: %w", err)
	}
	_ = ctx
	return nil
}
