package query

import (
	"fmt"

	"github.com/datata1/mycelium/internal/ipc"
)

// ErrNotFound marks lookups whose target (symbol, file, path endpoint) is
// not in the index. It aliases the ipc sentinel so errors.Is works
// identically for daemon-socket results and direct Reader calls.
var ErrNotFound = ipc.ErrNotFound

// notFound builds an ErrNotFound whose message reaches the caller verbatim
// (including hints and path suggestions), without the sentinel text
// appended the way fmt.Errorf("...: %w") would.
func notFound(format string, args ...any) error {
	return &notFoundError{msg: fmt.Sprintf(format, args...)}
}

type notFoundError struct{ msg string }

func (e *notFoundError) Error() string        { return e.msg }
func (e *notFoundError) Is(target error) bool { return target == ErrNotFound }
