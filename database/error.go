package database

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/golang-migrate/migrate/v5/internal/otelconv"
)

// Error should be used for errors involving queries ran against the database
type Error struct {
	// Optional: the line number
	Line uint

	// Query is a query excerpt
	Query []byte

	// Err is a useful/helping error message for humans
	Err string

	// OrigErr is the underlying error. It is reachable through Unwrap, so
	// whatever a driver puts here becomes visible to every caller's errors.Is
	// and errors.As.
	OrigErr error
}

func (e Error) Error() string {
	if len(e.Err) == 0 {
		return fmt.Sprintf("%v in line %v: %s", e.OrigErr, e.Line, e.Query)
	}
	return fmt.Sprintf("%v in line %v: %s (details: %v)", e.Err, e.Line, e.Query, e.OrigErr)
}

// Unwrap returns the underlying error so errors.Is and errors.As see through an
// Error to its cause.
func (e Error) Unwrap() error { return e.OrigErr }

// RedactedError is an Error with the migration query removed. It is used when
// an error is attached to telemetry, where the migration body must not appear:
// migrations routinely contain data (backfills, seed rows) that should not be
// shipped to an observability backend, and full migration files would bloat
// span attributes.
type RedactedError struct {
	// Line is the line number the original error referred to.
	Line uint

	// Err is the human readable message of the original error.
	Err string

	// OrigErr is the underlying error.
	OrigErr error
}

func (e RedactedError) Error() string {
	if len(e.Err) == 0 {
		return fmt.Sprintf("%v in line %v", e.OrigErr, e.Line)
	}
	return fmt.Sprintf("%v in line %v (details: %v)", e.Err, e.Line, e.OrigErr)
}

// Unwrap returns the underlying error so errors.Is and errors.As keep working
// across redaction.
func (e RedactedError) Unwrap() error { return e.OrigErr }

// RedactError returns an error safe to attach to telemetry. If err is (or
// wraps) an Error, the migration query is stripped; any other error is returned
// unchanged. It never returns nil for a non-nil err.
//
// Both *Error and Error are matched: drivers return either, and the pointer form
// is by far the more common, so checking only one would leave most migration
// bodies unredacted.
func RedactError(err error) error {
	if err == nil {
		return nil
	}

	// Check the pointer form first: with Error.Unwrap in place, a value target
	// would otherwise keep unwrapping past a *Error and match something deeper.
	var ptrErr *Error
	if errors.As(err, &ptrErr) && ptrErr != nil {
		return redactedFrom(*ptrErr)
	}
	var valErr Error
	if errors.As(err, &valErr) {
		return redactedFrom(valErr)
	}
	return err
}

func redactedFrom(e Error) RedactedError {
	return RedactedError{
		Line:    e.Line,
		Err:     e.Err,
		OrigErr: e.OrigErr,
	}
}

// ErrorType returns a low cardinality value for the error.type attribute,
// layering this package's lock sentinels over the shared classification.
// Callers with their own sentinels layer those on top of this.
func ErrorType(err error) string {
	switch {
	case errors.Is(err, ErrLocked):
		return "locked"
	case errors.Is(err, ErrNotLocked):
		return "not_locked"
	}
	return otelconv.ErrorType(err)
}

var (
	quotedKVRegex  = regexp.MustCompile(`password='[^']*'`)
	plainKVRegex   = regexp.MustCompile(`password=[^ ]*`)
	brokenURLRegex = regexp.MustCompile(`:[^:@]+?@`)
)

func RedactPassword(err error) error {
	input := err.Error()

	// Check if this error message contains password information
	hasPassword := quotedKVRegex.MatchString(input) || plainKVRegex.MatchString(input) || brokenURLRegex.MatchString(input)

	if !hasPassword {
		return err
	}
	input = quotedKVRegex.ReplaceAllLiteralString(input, "password=xxxxx")
	input = plainKVRegex.ReplaceAllLiteralString(input, "password=xxxxx")
	input = brokenURLRegex.ReplaceAllLiteralString(input, ":xxxxxx@")

	return errors.New(input)
}
