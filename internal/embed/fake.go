package embed

import (
	"context"
	"hash/fnv"
	"math"
)

// Fake is a deterministic embedder for tests. Each input string hashes to a
// fixed-dim vector whose components are derived from the hash — inputs with
// shared substrings will have correlated vectors, giving cosine similarity
// some signal without ever calling out to a model.
type Fake struct {
	Dim       int
	ModelName string
}

func NewFake(dim int) *Fake {
	if dim <= 0 {
		dim = 64
	}
	return &Fake{Dim: dim, ModelName: "fake"}
}

func (f *Fake) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i, s := range inputs {
		out[i] = f.vector(s)
	}
	return out, nil
}

func (f *Fake) Model() string   { return f.ModelName }
func (f *Fake) Dimension() int  { return f.Dim }

// vector: slide a window through the string, hash each window, and add its
// contribution to the corresponding bucket. This produces a stable vector
// where similar strings land close in cosine space.
func (f *Fake) vector(s string) []float32 {
	v := make([]float32, f.Dim)
	if len(s) == 0 {
		return v
	}
	window := 4
	for i := 0; i+window <= len(s); i++ {
		h := fnv.New32a()
		_, _ = h.Write([]byte(s[i : i+window]))
		sum := h.Sum32()
		v[int(sum)%f.Dim] += 1
	}
	// Normalize to unit length so cosine behaves well.
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return v
	}
	n := float32(math.Sqrt(norm))
	for i := range v {
		v[i] /= n
	}
	return v
}
