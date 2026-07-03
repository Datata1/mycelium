package ipc

import "errors"

// Sentinel errors shared across the wire boundary. The daemon maps them to
// Response.Code via CodeFor; Client.Call maps codes back so callers can
// branch with errors.Is regardless of whether a result came over the socket
// or from a direct in-process read.
var (
	ErrNotFound      = errors.New("not found")
	ErrUnknownMethod = errors.New("unknown method")
	ErrBadParams     = errors.New("bad params")
)

// Machine-readable error codes carried in Response.Code. Additive to the
// wire protocol: old clients ignore the field.
const (
	CodeNotFound      = "not_found"
	CodeUnknownMethod = "unknown_method"
	CodeBadParams     = "bad_params"
)

// CodeFor returns the wire code for err, or "" when it matches no sentinel.
func CodeFor(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrUnknownMethod):
		return CodeUnknownMethod
	case errors.Is(err, ErrBadParams):
		return CodeBadParams
	}
	return ""
}

// wireError is what Client.Call returns for a failed response: the daemon's
// message verbatim, plus the code so errors.Is matches the sentinels.
type wireError struct {
	msg  string
	code string
}

func (e *wireError) Error() string { return e.msg }

func (e *wireError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.code == CodeNotFound
	case ErrUnknownMethod:
		return e.code == CodeUnknownMethod
	case ErrBadParams:
		return e.code == CodeBadParams
	}
	return false
}
