package hook

import (
	"context"
	"fmt"

	"github.com/jdwiederstein/mycelium/internal/ipc"
)

// RunPostCommit is the body of `myco hook post-commit`. It asks the daemon
// to reindex; if the daemon is not running, this is a no-op (the watcher
// would have caught the changes anyway, and a one-shot reindex would compete
// with the user's next edit for file handles).
func RunPostCommit(ctx context.Context, socketPath string) error {
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
