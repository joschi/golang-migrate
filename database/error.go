package database

import (
	"errors"
	"fmt"
	"regexp"
)

// Error should be used for errors involving queries ran against the database
type Error struct {
	// Optional: the line number
	Line uint

	// Query is a query excerpt
	Query []byte

	// Err is a useful/helping error message for humans
	Err string

	// OrigErr is the underlying error
	OrigErr error
}

func (e Error) Error() string {
	if len(e.Err) == 0 {
		return fmt.Sprintf("%v in line %v: %s", e.OrigErr, e.Line, e.Query)
	}
	return fmt.Sprintf("%v in line %v: %s (details: %v)", e.Err, e.Line, e.Query, e.OrigErr)
}

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
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	var dbErr Error
	if !errors.As(err, &dbErr) {
		return err
	}
	return RedactedError{
		Line:    dbErr.Line,
		Err:     dbErr.Err,
		OrigErr: dbErr.OrigErr,
	}
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
