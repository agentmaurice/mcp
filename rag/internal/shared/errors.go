package shared

import "fmt"

// AppError represents a structured application error
type AppError struct {
	Code    int
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

// ErrValidation creates a validation error (400)
func ErrValidation(msg string) *AppError {
	return &AppError{Code: 400, Message: msg}
}

// ErrValidationf creates a formatted validation error (400)
func ErrValidationf(format string, args ...interface{}) *AppError {
	return &AppError{Code: 400, Message: fmt.Sprintf(format, args...)}
}

// ErrUnauthorized creates an unauthorized error (401)
func ErrUnauthorized(msg string) *AppError {
	return &AppError{Code: 401, Message: msg}
}

// ErrForbidden creates a forbidden error (403)
func ErrForbidden(msg string) *AppError {
	return &AppError{Code: 403, Message: msg}
}

// ErrNotFound creates a not found error (404)
func ErrNotFound(resource string) *AppError {
	return &AppError{Code: 404, Message: fmt.Sprintf("%s not found", resource)}
}

// ErrNotFoundf creates a formatted not found error (404)
func ErrNotFoundf(format string, args ...interface{}) *AppError {
	return &AppError{Code: 404, Message: fmt.Sprintf(format, args...)}
}

// ErrConflict creates a conflict error (409)
func ErrConflict(msg string) *AppError {
	return &AppError{Code: 409, Message: msg}
}

// ErrInternal creates an internal error (500)
func ErrInternal(msg string, err error) *AppError {
	return &AppError{Code: 500, Message: msg, Err: err}
}

// ErrInternalf creates a formatted internal error (500)
func ErrInternalf(err error, format string, args ...interface{}) *AppError {
	return &AppError{Code: 500, Message: fmt.Sprintf(format, args...), Err: err}
}

// ErrServiceUnavailable creates a service unavailable error (503)
func ErrServiceUnavailable(service string) *AppError {
	return &AppError{Code: 503, Message: fmt.Sprintf("%s unavailable", service)}
}
