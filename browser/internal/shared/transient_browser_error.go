package shared

import "strings"

// IsTransientBrowserError reports whether err is likely caused by a dead or
// resetting CDP session and should trigger reconnect/retry.
func IsTransientBrowserError(err error) bool {
	if err == nil {
		return false
	}
	if appErr := GetAppError(err); appErr != nil && appErr.Code == 503 {
		return true
	}

	message := strings.ToLower(err.Error())
	retryableSubstrings := []string{
		"context canceled",
		"context cancelled",
		"target closed",
		"websocket",
		"eof",
		"connection reset",
		"net::err_aborted",
		"page load error",
		"shutdown",
		"cannot find default execution context",
		"execution context",
		"browser is unavailable",
		"browser-manager is unavailable",
	}
	for _, substring := range retryableSubstrings {
		if strings.Contains(message, substring) {
			return true
		}
	}
	return false
}
