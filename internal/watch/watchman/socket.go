package watchman

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SockEnv is the override variable — if set, we skip the one-shot
// `watchman get-sockname` dance and connect directly to the listed
// path. Useful in containerized setups where the watchman daemon is
// already running under a known socket.
const SockEnv = "MYCELIUM_WATCHMAN_SOCK"

// GetSocketPath returns the path to the watchman unix socket.
//
//   1. $MYCELIUM_WATCHMAN_SOCK wins if set (covers exotic setups).
//   2. Otherwise: run `watchman get-sockname --no-pretty`, parse
//      the JSON, return the `sockname` field.
//
// Runs with a 5-second timeout so a hung watchman daemon can't wedge
// daemon startup.
func GetSocketPath(ctx context.Context) (string, error) {
	if v := strings.TrimSpace(os.Getenv(SockEnv)); v != "" {
		return v, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "watchman", "get-sockname", "--no-pretty")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("watchman get-sockname: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("watchman get-sockname: %w", err)
	}
	var resp struct {
		Sockname string `json:"sockname"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("watchman get-sockname: parse: %w (out=%q)", err, string(out))
	}
	if resp.Error != "" {
		return "", fmt.Errorf("watchman get-sockname: %s", resp.Error)
	}
	if resp.Sockname == "" {
		return "", fmt.Errorf("watchman get-sockname: empty sockname")
	}
	return resp.Sockname, nil
}
