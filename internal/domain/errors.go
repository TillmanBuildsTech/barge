// Package domain holds barge's core types. It depends on nothing but the
// standard library so every other package can import it freely.
package domain

import "errors"

// Sentinel errors. Wrap them with fmt.Errorf("...: %w", err) and test with
// errors.Is; never compare error strings.
var (
	// ErrInvalid marks input that failed validation.
	ErrInvalid = errors.New("invalid")
	// ErrNotFound marks a container, image or other resource that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict marks a resource that already exists.
	ErrConflict = errors.New("conflict")
	// ErrUnavailable marks a runtime or target that could not be reached.
	ErrUnavailable = errors.New("unavailable")
)
