package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRedactPassword(t *testing.T) {
	testcases := []struct {
		name     string
		input    error
		expected string
	}{
		{
			name:     "quoted password in key-value format",
			input:    errors.New("connection failed: password='secret123' invalid"),
			expected: "connection failed: password=xxxxx invalid",
		},
		{
			name:     "plain password in key-value format",
			input:    errors.New("connection failed: password=secret123 invalid"),
			expected: "connection failed: password=xxxxx invalid",
		},
		{
			name:     "password in URL format",
			input:    errors.New("connection failed: postgres://user:secret123@localhost/db"),
			expected: "connection failed: postgres://user:xxxxxx@localhost/db",
		},
		{
			name:     "multiple password formats",
			input:    errors.New("connection failed: password='secret' and url postgres://user:pass@host"),
			expected: "connection failed: password=xxxxx and url postgres://user:xxxxxx@host",
		},
		{
			name:     "no password in error",
			input:    errors.New("connection failed: invalid host"),
			expected: "connection failed: invalid host",
		},
		{
			name:     "empty error",
			input:    errors.New(""),
			expected: "",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			result := RedactPassword(tc.input)
			if result.Error() != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result.Error())
			}
		})
	}
}

type SpecialError struct {
	msg string
}

func (e SpecialError) Error() string {
	return e.msg
}

func TestRedactPasswordPreservesOriginalWhenNoPassword(t *testing.T) {
	originalErr := SpecialError{msg: "no password here"}
	result := RedactPassword(originalErr)

	if !errors.Is(result, originalErr) {
		t.Error("Expected original error to be returned when no password found")
	}
}

// TestErrorUnwrap covers the cause being visible through Error, in both the
// value and pointer forms drivers actually return.
func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("underlying")

	value := Error{OrigErr: cause}
	if got := errors.Unwrap(value); got != cause {
		t.Errorf("Unwrap(value) = %v, want %v", got, cause)
	}

	pointer := &Error{OrigErr: cause}
	if !errors.Is(pointer, cause) {
		t.Error("errors.Is must see through a *Error to its cause")
	}

	wrapped := fmt.Errorf("context: %w", value)
	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is must see through wrapping plus Error")
	}

	// A nil cause must not panic or match everything.
	empty := Error{Err: "no cause"}
	if errors.Is(empty, cause) {
		t.Error("an Error with no cause must not match an unrelated error")
	}
	if got := errors.Unwrap(empty); got != nil {
		t.Errorf("Unwrap(no cause) = %v, want nil", got)
	}
}

// TestErrorTypeSeesThroughError is the reason Unwrap matters for telemetry: a
// lock failure reported as Error{OrigErr: ErrLocked} must classify as "locked",
// not fall through to _OTHER.
func TestErrorTypeSeesThroughError(t *testing.T) {
	testcases := []struct {
		name string
		err  error
		want string
	}{
		{"value Error", Error{OrigErr: ErrLocked}, "locked"},
		{"pointer Error", &Error{OrigErr: ErrLocked}, "locked"},
		{"wrapped Error", fmt.Errorf("outer: %w", Error{OrigErr: ErrLocked}), "locked"},
		{"deadline through Error", Error{OrigErr: context.DeadlineExceeded}, "timeout"},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorType(tc.err); got != tc.want {
				t.Errorf("ErrorType() = %q, want %q", got, tc.want)
			}
		})
	}
}
