package shared

import (
	"errors"
	"fmt"
	"net/http"
)

// AppError represents an application-level error with HTTP status code
type AppError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

// IsAppError checks if the error is an AppError
func IsAppError(err error) bool {
	var appErr *AppError
	return errors.As(err, &appErr)
}

// GetAppError extracts AppError from error chain
func GetAppError(err error) *AppError {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	return nil
}

// ErrValidation creates a validation error (400)
func ErrValidation(message string) *AppError {
	return &AppError{
		Code:    http.StatusBadRequest,
		Message: message,
	}
}

// ErrValidationf creates a formatted validation error (400)
func ErrValidationf(format string, args ...interface{}) *AppError {
	return &AppError{
		Code:    http.StatusBadRequest,
		Message: fmt.Sprintf(format, args...),
	}
}

// ErrNotFound creates a not found error (404)
func ErrNotFound(resource string) *AppError {
	return &AppError{
		Code:    http.StatusNotFound,
		Message: fmt.Sprintf("%s not found", resource),
	}
}

// ErrNotFoundf creates a formatted not found error (404)
func ErrNotFoundf(format string, args ...interface{}) *AppError {
	return &AppError{
		Code:    http.StatusNotFound,
		Message: fmt.Sprintf(format, args...),
	}
}

// ErrUnauthorized creates an unauthorized error (401)
func ErrUnauthorized(message string) *AppError {
	return &AppError{
		Code:    http.StatusUnauthorized,
		Message: message,
	}
}

// ErrForbidden creates a forbidden error (403)
func ErrForbidden(message string) *AppError {
	return &AppError{
		Code:    http.StatusForbidden,
		Message: message,
	}
}

// ErrConflict creates a conflict error (409)
func ErrConflict(message string) *AppError {
	return &AppError{
		Code:    http.StatusConflict,
		Message: message,
	}
}

// ErrInternal creates an internal error (500)
func ErrInternal(message string, err error) *AppError {
	return &AppError{
		Code:    http.StatusInternalServerError,
		Message: message,
		Err:     err,
	}
}

// ErrInternalf creates a formatted internal error (500)
func ErrInternalf(err error, format string, args ...interface{}) *AppError {
	return &AppError{
		Code:    http.StatusInternalServerError,
		Message: fmt.Sprintf(format, args...),
		Err:     err,
	}
}

// ErrServiceUnavailable creates a service unavailable error (503)
func ErrServiceUnavailable(service string) *AppError {
	return &AppError{
		Code:    http.StatusServiceUnavailable,
		Message: fmt.Sprintf("%s is unavailable", service),
	}
}

// ErrTimeout creates a timeout error (408)
func ErrTimeout(message string) *AppError {
	return &AppError{
		Code:    http.StatusRequestTimeout,
		Message: message,
	}
}

// ErrBrowserAction creates a browser action error
func ErrBrowserAction(action string, err error) *AppError {
	return &AppError{
		Code:    http.StatusInternalServerError,
		Message: fmt.Sprintf("browser action '%s' failed", action),
		Err:     err,
	}
}

// ErrElementNotFound creates an element not found error
func ErrElementNotFound(selector string) *AppError {
	return &AppError{
		Code:    http.StatusNotFound,
		Message: fmt.Sprintf("element not found: %s", selector),
	}
}
