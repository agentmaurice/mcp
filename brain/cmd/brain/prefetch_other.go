//go:build !duckdb

package main

import "errors"

// runPrefetchExtensions is unavailable without the duckdb build tag: such a
// binary has no DuckDB backend and nothing to prefetch.
func runPrefetchExtensions() error {
	return errors.New("-prefetch-extensions requires a binary built with -tags duckdb")
}
