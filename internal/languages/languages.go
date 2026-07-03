// Package languages is the single registration point for built-in language
// support. The daemon, the one-shot indexer, and tests all build their
// parser registry and resolver set here, so adding a language is one edit
// in one file — see docs/adding-a-language.md for the full walkthrough.
package languages

import (
	"log/slog"
	"os"

	"github.com/datata1/mycelium/internal/parser"
	"github.com/datata1/mycelium/internal/parser/golang"
	"github.com/datata1/mycelium/internal/parser/python"
	"github.com/datata1/mycelium/internal/parser/typescript"
	"github.com/datata1/mycelium/internal/pipeline"
	goresolver "github.com/datata1/mycelium/internal/resolver/golang"
	pyresolver "github.com/datata1/mycelium/internal/resolver/python"
	tsresolver "github.com/datata1/mycelium/internal/resolver/typescript"
)

// Registry returns a parser registry with one parser per enabled language.
// Unknown names are ignored (config validation rejects them upstream).
func Registry(enabled []string) *parser.Registry {
	reg := parser.NewRegistry()
	for _, lang := range enabled {
		switch lang {
		case "go":
			reg.Register(golang.New())
		case "typescript":
			reg.Register(typescript.New())
		case "python":
			reg.Register(python.New())
		}
	}
	return reg
}

// Resolvers constructs one ref resolver per enabled language, keyed the way
// pipeline.Pipeline.Resolvers expects. Languages without a resolver fall
// back to textual-only resolution automatically. Go needs an up-front
// go/packages load — when that fails the language degrades to textual
// resolution rather than erroring. A nil log defaults to stderr.
func Resolvers(repoRoot string, enabled []string, log *slog.Logger) map[string]pipeline.Resolver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	out := map[string]pipeline.Resolver{}
	for _, lang := range enabled {
		switch lang {
		case "go":
			r := goresolver.New(repoRoot)
			errCount, err := r.Load()
			if err != nil {
				log.Warn("go-types unavailable; falling back to textual resolution", "err", err)
				continue
			}
			if errCount > 0 {
				log.Warn("go-types loaded with package errors (inspect via `myco doctor`)", "errors", errCount)
			}
			out["go"] = r
		case "typescript":
			out["typescript"] = tsresolver.New()
		case "python":
			out["python"] = pyresolver.New()
		}
	}
	return out
}
