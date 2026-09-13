package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const MaxResponseBytes = 1024 * 1024

type Operation struct {
	OperationID string      `json:"operation_id"`
	Method      string      `json:"method"`
	Path        string      `json:"path"`
	Summary     string      `json:"summary,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
	RequestBody interface{} `json:"request_body,omitempty"`
	raw         map[string]interface{}
}

type CallInput struct {
	Spec          string                 `json:"spec"`
	BaseURL       string                 `json:"base_url"`
	OperationID   string                 `json:"operation_id"`
	PathParams    map[string]interface{} `json:"path_params,omitempty"`
	Query         map[string]interface{} `json:"query,omitempty"`
	Headers       map[string]string      `json:"headers,omitempty"`
	Body          interface{}            `json:"body,omitempty"`
	CredentialRef string                 `json:"credential_ref,omitempty"`
	AllowMutation bool                   `json:"allow_mutation,omitempty"`
}

type CallResult struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       interface{}       `json:"body"`
	Bytes      int               `json:"bytes"`
}

func Parse(spec string) (map[string]interface{}, error) {
	var document map[string]interface{}
	if err := json.Unmarshal([]byte(spec), &document); err != nil {
		if yamlErr := yaml.Unmarshal([]byte(spec), &document); yamlErr != nil {
			return nil, fmt.Errorf("invalid OpenAPI document: %v", err)
		}
	}
	version, _ := document["openapi"].(string)
	if !strings.HasPrefix(version, "3.0.") && !strings.HasPrefix(version, "3.1.") {
		return nil, fmt.Errorf("only OpenAPI 3.0 and 3.1 are supported")
	}
	if _, ok := document["paths"].(map[string]interface{}); !ok {
		return nil, fmt.Errorf("OpenAPI paths object is required")
	}
	return document, nil
}

func Operations(document map[string]interface{}) []Operation {
	paths, _ := document["paths"].(map[string]interface{})
	operations := make([]Operation, 0)
	for path, rawPath := range paths {
		pathItem, _ := rawPath.(map[string]interface{})
		for method, rawOperation := range pathItem {
			if !isMethod(method) {
				continue
			}
			operation, _ := rawOperation.(map[string]interface{})
			operationID, _ := operation["operationId"].(string)
			if strings.TrimSpace(operationID) == "" {
				continue
			}
			operations = append(operations, Operation{OperationID: operationID, Method: strings.ToUpper(method), Path: path, Summary: stringField(operation, "summary"), Parameters: operation["parameters"], RequestBody: operation["requestBody"], raw: operation})
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].OperationID < operations[j].OperationID })
	return operations
}

func Find(document map[string]interface{}, operationID string) (Operation, error) {
	for _, operation := range Operations(document) {
		if operation.OperationID == operationID {
			return operation, nil
		}
	}
	return Operation{}, fmt.Errorf("operationId %q was not found", operationID)
}

func Call(ctx context.Context, input CallInput) (CallResult, error) {
	document, err := Parse(input.Spec)
	if err != nil {
		return CallResult{}, err
	}
	operation, err := Find(document, input.OperationID)
	if err != nil {
		return CallResult{}, err
	}
	if !isReadOnly(operation.Method) && !input.AllowMutation {
		return CallResult{}, fmt.Errorf("mutation requires allow_mutation=true")
	}
	target, err := buildURL(input.BaseURL, operation, input.PathParams, input.Query)
	if err != nil {
		return CallResult{}, err
	}
	if err := validateTarget(target); err != nil {
		return CallResult{}, err
	}
	var body io.Reader
	if input.Body != nil {
		payload, marshalErr := json.Marshal(input.Body)
		if marshalErr != nil {
			return CallResult{}, marshalErr
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, operation.Method, target.String(), body)
	if err != nil {
		return CallResult{}, err
	}
	for name, value := range input.Headers {
		if !allowedHeader(name) {
			return CallResult{}, fmt.Errorf("header %q is not allowed", name)
		}
		req.Header.Set(name, value)
	}
	if input.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if input.CredentialRef != "" {
		token, resolveErr := resolveCredential(input.CredentialRef)
		if resolveErr != nil {
			return CallResult{}, resolveErr
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return CallResult{}, fmt.Errorf("API request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		return CallResult{}, err
	}
	if len(payload) > MaxResponseBytes {
		return CallResult{}, fmt.Errorf("response exceeds %d bytes", MaxResponseBytes)
	}
	var decoded interface{} = string(payload)
	if json.Valid(payload) {
		_ = json.Unmarshal(payload, &decoded)
	}
	headers := map[string]string{}
	for _, name := range []string{"Content-Type", "ETag", "Last-Modified", "Request-Id"} {
		if value := response.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return CallResult{StatusCode: response.StatusCode, Headers: headers, Body: decoded, Bytes: len(payload)}, nil
}

func buildURL(baseURL string, operation Operation, pathParams, query map[string]interface{}) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}
	path := operation.Path
	for key, value := range pathParams {
		path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(fmt.Sprint(value)))
	}
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("all path parameters must be supplied")
	}
	relative, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	target := base.ResolveReference(relative)
	values := target.Query()
	for key, value := range query {
		switch typed := value.(type) {
		case []interface{}:
			for _, item := range typed {
				values.Add(key, fmt.Sprint(item))
			}
		default:
			values.Set(key, fmt.Sprint(value))
		}
	}
	target.RawQuery = values.Encode()
	return target, nil
}

func validateTarget(target *url.URL) error {
	if target.User != nil {
		return fmt.Errorf("credentials in URL are forbidden")
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && strings.EqualFold(os.Getenv("MCP_API_ALLOW_HTTP"), "true")) {
		return fmt.Errorf("HTTPS is required")
	}
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return fmt.Errorf("target host is required")
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("literal IP addresses are forbidden")
	}
	allowed := strings.Split(os.Getenv("MCP_API_ALLOWED_HOSTS"), ",")
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), host) {
			return nil
		}
	}
	return fmt.Errorf("target host is not allowlisted")
}

func resolveCredential(reference string) (string, error) {
	if strings.ContainsAny(reference, "\r\n") || !strings.HasPrefix(reference, "secret://") {
		return "", fmt.Errorf("credential_ref must use secret://")
	}
	name := strings.TrimPrefix(reference, "secret://")
	replacer := strings.NewReplacer("-", "_", "/", "_", ".", "_")
	env := "MCP_API_CREDENTIAL_" + strings.ToUpper(replacer.Replace(name))
	value := strings.TrimSpace(os.Getenv(env))
	if value == "" {
		return "", fmt.Errorf("credential reference is not provisioned")
	}
	return value, nil
}
func allowedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "accept", "content-type", "idempotency-key":
		return true
	default:
		return false
	}
}
func isReadOnly(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
func isMethod(method string) bool {
	switch strings.ToLower(method) {
	case "get", "post", "put", "patch", "delete", "head", "options":
		return true
	default:
		return false
	}
}
func stringField(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return value
}
