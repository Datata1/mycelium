package embed

import "context"

// Noop is the default embedder. It returns ErrNotConfigured, which callers
// translate into a clear "embeddings_not_configured" error for MCP clients.
// Keeping a non-nil Embedder in the pipeline makes the rest of the code
// uniform — no special-casing for "no embedder".
type Noop struct{}

func (Noop) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, ErrNotConfigured
}
func (Noop) Model() string { return "none" }
func (Noop) Dimension() int { return 0 }
