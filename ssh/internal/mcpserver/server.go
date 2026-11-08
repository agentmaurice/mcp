package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/agentmaurice/mcpchatui/mcp/ssh/internal/sshservice"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serviceVersion = "0.3.0"

// maxBase64Length bounds the encoded payload before decoding (20 MiB decoded
// plus padding); the service enforces the configured limit on decoded bytes.
const maxBase64Length = (sshservice.MaxUploadBytes+2)/3*4 + 4

type API interface {
	Health(context.Context) (sshservice.Health, error)
	Capabilities(context.Context) (sshservice.Capabilities, error)
	Targets(context.Context) (sshservice.Targets, error)
	Check(context.Context, string, time.Duration) (sshservice.CheckResult, error)
	Open(context.Context, string, time.Duration) (sshservice.SessionView, error)
	Exec(context.Context, string, string, string, time.Duration, int) (sshservice.ExecResult, error)
	Status(context.Context, string) (sshservice.SessionView, error)
	CloseSession(context.Context, string) (sshservice.SessionView, error)
	Upload(context.Context, string, string, []byte, bool, string) (sshservice.UploadResult, error)
	Remove(context.Context, string, string) (sshservice.RemoveResult, error)
	List(context.Context, string, string) (sshservice.ListResult, error)
}

func New(service API) *modernmcp.Server {
	s := modernmcp.New(
		"agentmaurice-ssh",
		serviceVersion,
		"Use SSH only for a published remote target when no narrower typed capability exists. Never accept a host, identity or credential from the conversation.",
	)
	s.AddTool(readTool("ssh_health_v1", "Check the SSH server, target projection and credential-provider boundary.", schema(nil, nil)), withAPI(service, health))
	s.AddTool(readTool("ssh_capabilities_v1", "Describe SSH functions, limits, mode and target revision.", schema(nil, nil)), withAPI(service, capabilities))
	s.AddTool(readTool("ssh_targets_v1", "List the SSH targets visible in the current Build or Run projection.", schema(nil, nil)), withAPI(service, targets))
	s.AddTool(actionTool("ssh_target_check_v1", "In Build mode, verify DNS, host key and SSH handshake for a known target.", false, schema(map[string]any{
		"target_id":               stringProperty(1, 63),
		"connect_timeout_seconds": integerProperty(1, 300, 15),
	}, []string{"target_id"})), withAPI(service, check))
	s.AddTool(actionTool("ssh_session_open_v1", "Open a bounded SSH connection to one published target_id.", false, schema(map[string]any{
		"target_id":               stringProperty(1, 63),
		"connect_timeout_seconds": integerProperty(1, 300, 15),
	}, []string{"target_id"})), withAPI(service, open))
	s.AddTool(actionTool("ssh_exec_v1", "Execute one synchronous command on an owned SSH session without PTY or persistent shell state.", true, schema(map[string]any{
		"session_id":       stringProperty(1, 128),
		"command":          stringProperty(1, 32768),
		"cwd":              map[string]any{"type": "string", "maxLength": 4096},
		"timeout_seconds":  integerProperty(1, 300, 30),
		"max_output_bytes": map[string]any{"type": "integer", "minimum": 1024, "maximum": 4194304, "default": 262144},
	}, []string{"session_id", "command"})), withAPI(service, execCommand))
	s.AddTool(readTool("ssh_session_status_v1", "Read the bounded status of one owned SSH session.", schema(map[string]any{
		"session_id": stringProperty(1, 128),
	}, []string{"session_id"})), withAPI(service, status))
	s.AddTool(actionTool("ssh_session_close_v1", "Close one owned SSH session and destroy its in-memory connection state.", false, schema(map[string]any{
		"session_id": stringProperty(1, 128),
	}, []string{"session_id"})), withAPI(service, closeSession))
	s.AddTool(actionTool("ssh_upload_v1", "Atomically write one file below the publication root of a published target (SFTP, path relative, no '..', no symbolic link, 20 MiB max, overwrite false by default).", false, schema(map[string]any{
		"target": stringProperty(1, 63),
		"path":   stringProperty(1, 1024),
		"file": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"content_base64": map[string]any{"type": "string"},
			"ref":            map[string]any{"type": "string", "description": "Buffer reference; requires a Buffer client, currently unavailable."},
		}},
		"overwrite": map[string]any{"type": "boolean", "default": false},
		"mode":      map[string]any{"type": "string", "pattern": "^0?[0-7]{3}$", "default": "0644"},
	}, []string{"target", "path", "file"})), withAPI(service, upload))
	s.AddTool(actionTool("ssh_remove_v1", "Remove one file below the publication root of a published target (SFTP).", true, schema(map[string]any{
		"target": stringProperty(1, 63),
		"path":   stringProperty(1, 1024),
	}, []string{"target", "path"})), withAPI(service, remove))
	s.AddTool(readTool("ssh_list_v1", "List one directory (depth 1, 500 entries max) below the publication root of a published target (SFTP).", schema(map[string]any{
		"target": stringProperty(1, 63),
		"path":   map[string]any{"type": "string", "maxLength": 1024},
	}, []string{"target"})), withAPI(service, list))
	return s
}

type handler func(context.Context, API, mcp.CallToolRequest) (*mcp.CallToolResult, error)

func withAPI(service API, next handler) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return next(ctx, service, request)
	}
}

func health(ctx context.Context, service API, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	value, err := service.Health(ctx)
	return response(value, err)
}

func capabilities(ctx context.Context, service API, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	value, err := service.Capabilities(ctx)
	return response(value, err)
}

func targets(ctx context.Context, service API, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	value, err := service.Targets(ctx)
	return response(value, err)
}

type targetRequest struct {
	TargetID             string `json:"target_id"`
	ConnectTimeoutSecond int    `json:"connect_timeout_seconds"`
}

func check(ctx context.Context, service API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if hasTargetOverride(request) {
		return failure("ssh_target_override_forbidden", "destination, identity and credential overrides are forbidden"), nil
	}
	var input targetRequest
	if err := decodeStrict(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := service.Check(ctx, input.TargetID, seconds(input.ConnectTimeoutSecond))
	return response(value, err)
}

func open(ctx context.Context, service API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if hasTargetOverride(request) {
		return failure("ssh_target_override_forbidden", "destination, identity and credential overrides are forbidden"), nil
	}
	var input targetRequest
	if err := decodeStrict(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := service.Open(ctx, input.TargetID, seconds(input.ConnectTimeoutSecond))
	return response(value, err)
}

type execRequest struct {
	SessionID     string `json:"session_id"`
	Command       string `json:"command"`
	CWD           string `json:"cwd"`
	TimeoutSecond int    `json:"timeout_seconds"`
	MaxOutput     int    `json:"max_output_bytes"`
}

func execCommand(ctx context.Context, service API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input execRequest
	if err := decodeStrict(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := service.Exec(ctx, input.SessionID, input.Command, input.CWD, seconds(input.TimeoutSecond), input.MaxOutput)
	return response(value, err)
}

type fileInput struct {
	ContentBase64 *string `json:"content_base64"`
	Ref           string  `json:"ref"`
}

type uploadRequest struct {
	Target    string    `json:"target"`
	Path      string    `json:"path"`
	File      fileInput `json:"file"`
	Overwrite bool      `json:"overwrite"`
	Mode      string    `json:"mode"`
}

func upload(ctx context.Context, service API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if hasTargetOverride(request) {
		return failure("ssh_target_override_forbidden", "destination, identity and credential overrides are forbidden"), nil
	}
	var input uploadRequest
	if err := decodeStrict(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	if input.File.ContentBase64 != nil && input.File.Ref != "" {
		return failure("invalid_arguments", "file takes content_base64 or ref, not both"), nil
	}
	if input.File.ContentBase64 == nil {
		if input.File.Ref != "" {
			return failure("buffer_unavailable", "no Buffer client is configured for this server; use file.content_base64"), nil
		}
		return failure("invalid_arguments", "file.content_base64 is required"), nil
	}
	encoded := *input.File.ContentBase64
	if len(encoded) > maxBase64Length {
		return failure("file_too_large", "file exceeds the upload limit"), nil
	}
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return failure("invalid_arguments", "file.content_base64 is not valid base64"), nil
	}
	value, err := service.Upload(ctx, input.Target, input.Path, content, input.Overwrite, input.Mode)
	return response(value, err)
}

type pathRequest struct {
	Target string `json:"target"`
	Path   string `json:"path"`
}

func remove(ctx context.Context, service API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if hasTargetOverride(request) {
		return failure("ssh_target_override_forbidden", "destination, identity and credential overrides are forbidden"), nil
	}
	var input pathRequest
	if err := decodeStrict(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := service.Remove(ctx, input.Target, input.Path)
	return response(value, err)
}

func list(ctx context.Context, service API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if hasTargetOverride(request) {
		return failure("ssh_target_override_forbidden", "destination, identity and credential overrides are forbidden"), nil
	}
	var input pathRequest
	if err := decodeStrict(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := service.List(ctx, input.Target, input.Path)
	return response(value, err)
}

type sessionRequest struct {
	SessionID string `json:"session_id"`
}

func status(ctx context.Context, service API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input sessionRequest
	if err := decodeStrict(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := service.Status(ctx, input.SessionID)
	return response(value, err)
}

func closeSession(ctx context.Context, service API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input sessionRequest
	if err := decodeStrict(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := service.CloseSession(ctx, input.SessionID)
	return response(value, err)
}

func response(value any, err error) (*mcp.CallToolResult, error) {
	if err == nil {
		return &mcp.CallToolResult{StructuredContent: value}, nil
	}
	var serviceError *sshservice.Error
	if errors.As(err, &serviceError) {
		return failure(serviceError.Code, serviceError.Message), nil
	}
	return failure("ssh_internal_error", "SSH operation failed"), nil
}

// injectedContextKeys are added by the One to every call; they carry no tool
// input and are dropped before the strict decoding. Other unknown fields stay
// refused.
var injectedContextKeys = []string{"deployment_id", "organization_id", "user_id", "space_id", "session_id"}

func declaredFields(target any) map[string]struct{} {
	fields := map[string]struct{}{}
	t := reflect.TypeOf(target)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return fields
	}
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" {
			fields[name] = struct{}{}
		}
	}
	return fields
}

func decodeStrict(request mcp.CallToolRequest, target any) error {
	payload, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return fmt.Errorf("encode arguments: %w", err)
	}
	var arguments map[string]json.RawMessage
	if json.Unmarshal(payload, &arguments) == nil {
		declared := declaredFields(target)
		for _, key := range injectedContextKeys {
			// session_id is also a real input of the session tools: keep it
			// whenever the tool declares it.
			if _, ok := declared[key]; !ok {
				delete(arguments, key)
			}
		}
		if payload, err = json.Marshal(arguments); err != nil {
			return fmt.Errorf("encode arguments: %w", err)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	return nil
}

func hasTargetOverride(request mcp.CallToolRequest) bool {
	arguments, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"host", "port", "username", "fingerprint", "host_key_fingerprint", "credential_ref", "private_key", "password", "passphrase"} {
		if _, exists := arguments[key]; exists {
			return true
		}
	}
	return false
}

func seconds(value int) time.Duration {
	return time.Duration(value) * time.Second
}

func readTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := true, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func actionTool(name, description string, destructive bool, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, idempotent := false, false
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func schema(properties map[string]any, required []string) mcp.ToolInputSchema {
	if properties == nil {
		properties = map[string]any{}
	}
	return mcp.ToolInputSchema{Type: "object", Properties: properties, Required: required}
}

func stringProperty(minimum, maximum int) map[string]any {
	return map[string]any{"type": "string", "minLength": minimum, "maxLength": maximum}
}

func integerProperty(minimum, maximum, defaultValue int) map[string]any {
	return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum, "default": defaultValue}
}

func failure(code, message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"error": code, "message": message}}
}
