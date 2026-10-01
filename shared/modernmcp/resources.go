package modernmcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// ErrResourceNotFound preserves AM's existing resource-not-found wire contract.
// mcp-go currently projects handler errors to INTERNAL_ERROR, so the stateless
// HTTP boundary maps only this explicitly classified domain failure.
var ErrResourceNotFound = errors.New("resource not found")

type resourceErrorKey struct{}
type resourceErrorState struct{ missing bool }
type resourceErrorWriter struct {
	http.ResponseWriter
	state *resourceErrorState
}

func (w *resourceErrorWriter) WriteHeader(status int) {
	if w.state.missing {
		status = http.StatusBadRequest
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *resourceErrorWriter) Write(data []byte) (int, error) {
	originalLen := len(data)
	if w.state.missing {
		var response map[string]json.RawMessage
		if json.Unmarshal(data, &response) == nil {
			var rpcError map[string]json.RawMessage
			if json.Unmarshal(response["error"], &rpcError) == nil && string(rpcError["code"]) == "-32603" {
				rpcError["code"] = json.RawMessage("-32602")
				encoded, err := json.Marshal(rpcError)
				if err != nil {
					return 0, err
				}
				response["error"] = encoded
				encoded, err = json.Marshal(response)
				if err != nil {
					return 0, err
				}
				data = append(encoded, '\n')
			}
		}
	}
	n, err := w.ResponseWriter.Write(data)
	if err == nil {
		return originalLen, nil
	}
	return n, err
}
func resourceErrorHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := &resourceErrorState{}
		next.ServeHTTP(&resourceErrorWriter{ResponseWriter: w, state: state}, r.WithContext(context.WithValue(r.Context(), resourceErrorKey{}, state)))
	})
}
