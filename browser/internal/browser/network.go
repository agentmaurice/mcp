package browser

import (
	"context"
	"strings"
)

// NetworkEntry represents a captured network request/response
type NetworkEntry struct {
	RequestID           string            `json:"request_id"`
	URL                 string            `json:"url"`
	Method              string            `json:"method"`
	StatusCode          int               `json:"status_code"`
	ResponseHeaders     map[string]string `json:"response_headers"`
	ResponseBodyTruncated bool            `json:"response_body_truncated"`
	ResponseBody        string            `json:"response_body"`
	Timestamp           int64             `json:"timestamp"` // Unix milliseconds
}

// MockRule represents a network mock rule
type MockRule struct {
	ID               string            `json:"id"`
	URLPattern       string            `json:"url_pattern"`        // glob pattern
	Method           string            `json:"method"`             // e.g. GET, POST
	ResponseStatus   int               `json:"response_status"`    // HTTP status code
	ResponseHeaders  map[string]string `json:"response_headers"`   // response headers
	ResponseBody     string            `json:"response_body"`      // response body
	Once             bool              `json:"once"`               // if true, rule is removed after first match
}

// NetworkState holds network capture and mock state for a session
type NetworkState struct {
	// Capture state
	Capturing        bool
	CaptureID        string
	Entries          []*NetworkEntry // ring buffer
	MaxEntries       int
	ResponseBodySize int // max bytes for response body

	// Mock state
	Mocks map[string]*MockRule // ID -> MockRule

	// Listener state
	ListenerCtx context.Context
	ListenerCancel context.CancelFunc
	ListenerDone chan struct{} // closed when listener goroutine exits
}

// GlobMatch returns true if a glob pattern matches a URL.
// Case-sensitive. * matches any chars except /, ** matches any chars including /.
func GlobMatch(pattern, url string) bool {
	// Simple glob implementation: * and **
	return globMatchHelper(pattern, 0, url, 0)
}

func globMatchHelper(pattern string, patIdx int, url string, urlIdx int) bool {
	patLen := len(pattern)
	urlLen := len(url)

	for patIdx < patLen {
		if pattern[patIdx] == '*' {
			// Check for **
			if patIdx+1 < patLen && pattern[patIdx+1] == '*' {
				// ** matches everything
				// Try matching the rest of pattern from current url position
				for i := urlIdx; i <= urlLen; i++ {
					if globMatchHelper(pattern, patIdx+2, url, i) {
						return true
					}
				}
				return false
			}

			// Single * matches anything except /
			// Find the next non-* character in pattern
			nextPatIdx := patIdx + 1
			for nextPatIdx < patLen && pattern[nextPatIdx] == '*' {
				nextPatIdx++
			}

			// Try matching from different positions
			for i := urlIdx; i <= urlLen; i++ {
				if i < urlLen && url[i] == '/' {
					// * doesn't match /
					break
				}
				if globMatchHelper(pattern, nextPatIdx, url, i) {
					return true
				}
			}
			return false
		}

		if pattern[patIdx] != url[urlIdx] {
			return false
		}
		patIdx++
		urlIdx++
	}

	return urlIdx == urlLen
}

// StripQueryFragment removes query string and fragment from URL
func StripQueryFragment(url string) string {
	// Remove fragment first (#)
	if idx := strings.IndexByte(url, '#'); idx >= 0 {
		url = url[:idx]
	}
	// Remove query string (?)
	if idx := strings.IndexByte(url, '?'); idx >= 0 {
		url = url[:idx]
	}
	return url
}

// addEntry adds a network entry to the ring buffer
func (ns *NetworkState) addEntry(entry *NetworkEntry) {
	if ns.Entries == nil {
		ns.Entries = make([]*NetworkEntry, 0, ns.MaxEntries)
	}

	// If at capacity, remove oldest (FIFO)
	if len(ns.Entries) >= ns.MaxEntries {
		ns.Entries = ns.Entries[1:]
	}

	ns.Entries = append(ns.Entries, entry)
}

// GetEntries returns a copy of all captured entries
func (ns *NetworkState) GetEntries() []*NetworkEntry {
	if ns.Entries == nil {
		return nil
	}
	result := make([]*NetworkEntry, len(ns.Entries))
	copy(result, ns.Entries)
	return result
}

// ClearEntries clears all captured entries
func (ns *NetworkState) ClearEntries() {
	ns.Entries = nil
}

// AddMock adds a mock rule
func (ns *NetworkState) AddMock(rule *MockRule) {
	if ns.Mocks == nil {
		ns.Mocks = make(map[string]*MockRule)
	}
	ns.Mocks[rule.ID] = rule
}

// RemoveMock removes a mock rule by ID
func (ns *NetworkState) RemoveMock(id string) {
	if ns.Mocks != nil {
		delete(ns.Mocks, id)
	}
}

// ClearMocks clears all mock rules
func (ns *NetworkState) ClearMocks() {
	ns.Mocks = make(map[string]*MockRule)
}
