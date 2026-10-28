package linkoerr

import (
	"errors"
	"log/slog"
)

type errWithAttrs struct {
	err   error
	attrs []slog.Attr
}

func (e *errWithAttrs) Error() string {
	return e.err.Error()
}

func (e *errWithAttrs) Unwrap() error {
	return e.err
}

// WithAttrs wraps err with structured log attributes. args follow the same
// conventions as slog.Logger.Info: alternating keys and values, or slog.Attrs.
func WithAttrs(err error, args ...any) error {
	if err == nil {
		return nil
	}
	return &errWithAttrs{
		err:   err,
		attrs: argsToAttrs(args),
	}
}

// Attrs returns the attributes attached to every error in err's chain,
// outermost first.
func Attrs(err error) []slog.Attr {
	var attrs []slog.Attr
	for ; err != nil; err = errors.Unwrap(err) {
		if e, ok := err.(*errWithAttrs); ok {
			attrs = append(attrs, e.attrs...)
		}
	}
	return attrs
}

func argsToAttrs(args []any) []slog.Attr {
	return slog.Group("", args...).Value.Group()
}
