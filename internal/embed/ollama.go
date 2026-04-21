package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ollama uses the local Ollama server's /api/embeddings endpoint.
// Endpoint defaults to http://localhost:11434. The server must have the
// requested model pulled (`ollama pull nomic-embed-text`).
type Ollama struct {
	Endpoint   string
	ModelName  string
	Dim        int
	HTTPClient *http.Client
}

func NewOllama(endpoint, model string, dim int) *Ollama {
	return &Ollama{
		Endpoint:  endpoint,
		ModelName: model,
		Dim:       dim,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (o *Ollama) Model() string { return o.ModelName }
func (o *Ollama) Dimension() int { return o.Dim }

type ollamaEmbedReq struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResp struct {
	Embedding []float32 `json:"embedding"`
}

// Embed issues one HTTP call per input. Ollama's native endpoint is
// single-prompt; a caller that wants throughput should batch at the worker
// pool layer instead of on the wire. We can adopt the newer /api/embed
// (plural) endpoint once it's widely available in installed Ollama versions.
func (o *Ollama) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	url := strings.TrimRight(o.Endpoint, "/") + "/api/embeddings"
	for i, text := range inputs {
		v, err := o.embedOne(ctx, url, text)
		if err != nil {
			return nil, fmt.Errorf("ollama embed[%d]: %w", i, err)
		}
		if len(v) != o.Dim {
			return nil, fmt.Errorf("ollama embed: got dim %d, expected %d", len(v), o.Dim)
		}
		out[i] = v
	}
	return out, nil
}

func (o *Ollama) embedOne(ctx context.Context, url, text string) ([]float32, error) {
	body, _ := json.Marshal(ollamaEmbedReq{Model: o.ModelName, Prompt: text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama http %d: %s", resp.StatusCode, string(b))
	}
	var out ollamaEmbedResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Embedding, nil
}
