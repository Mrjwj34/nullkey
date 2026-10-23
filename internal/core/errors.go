// Package core implements the kernel services that sit between the HTTP
// transport and the adapters (store, runtime, secret store, gateways).
package core

import (
	"errors"
	"fmt"
	"net/http"
)

// Error is a typed service error that the HTTP layer maps to the stable error
// envelope. It never carries secret material.
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// E builds a service error.
func E(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Wrap builds a service error with an internal cause. The cause is logged by
// the caller, never returned to clients.
func Wrap(status int, code, message string, err error) *Error {
	return &Error{Status: status, Code: code, Message: message, Err: err}
}

// WithDetails attaches safe, non-secret details.
func (e *Error) WithDetails(details map[string]any) *Error {
	e.Details = details
	return e
}

// Invalid reports a malformed request.
func Invalid(message string) *Error {
	return E(http.StatusBadRequest, "invalid_request", message)
}

// NotFound reports a missing resource.
func NotFound(message string) *Error {
	return E(http.StatusNotFound, "not_found", message)
}

// Conflict reports a state conflict.
func Conflict(code, message string) *Error {
	return E(http.StatusConflict, code, message)
}

// AsError extracts a *Error.
func AsError(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
