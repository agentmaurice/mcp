package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/system/internal/protocol"
)

type API interface {
	Health(context.Context) (protocol.Health, error)
	Capabilities(context.Context) (protocol.Capabilities, error)
	Inspect(context.Context) (protocol.Inspection, error)
	Services(context.Context) (protocol.Services, error)
	Logs(context.Context, protocol.LogsRequest) (protocol.Logs, error)
	Plan(context.Context, protocol.PlanRequest) (protocol.Plan, error)
	Apply(context.Context, string, string) (protocol.ApplyResult, error)
}

type Client struct {
	http     *http.Client
	identity Identity
}

type Identity struct {
	Actor          string
	OrganizationID string
	DeploymentID   string
}

func New(socketPath string, timeout time.Duration, identity Identity) *Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: timeout}, identity: identity}
}

func (c *Client) Health(ctx context.Context) (protocol.Health, error) {
	return get[protocol.Health](ctx, c.http, "/v1/health")
}

func (c *Client) Capabilities(ctx context.Context) (protocol.Capabilities, error) {
	return get[protocol.Capabilities](ctx, c.http, "/v1/capabilities")
}

func (c *Client) Inspect(ctx context.Context) (protocol.Inspection, error) {
	return get[protocol.Inspection](ctx, c.http, "/v1/inspect")
}

func (c *Client) Services(ctx context.Context) (protocol.Services, error) {
	return get[protocol.Services](ctx, c.http, "/v1/services")
}

func (c *Client) Logs(ctx context.Context, request protocol.LogsRequest) (protocol.Logs, error) {
	return post[protocol.Logs](ctx, c.http, "/v1/logs", request)
}

func (c *Client) Plan(ctx context.Context, request protocol.PlanRequest) (protocol.Plan, error) {
	return post[protocol.Plan](ctx, c.http, "/v1/actions/plan", request)
}

func (c *Client) Apply(ctx context.Context, planID, planHash string) (protocol.ApplyResult, error) {
	return post[protocol.ApplyResult](ctx, c.http, "/v1/actions/apply", protocol.ApplyRequest{
		PlanID: planID, PlanHash: planHash, Actor: c.identity.Actor,
		OrganizationID: c.identity.OrganizationID, DeploymentID: c.identity.DeploymentID,
	})
}

func get[T any](ctx context.Context, client *http.Client, path string) (T, error) {
	return request[T](ctx, client, http.MethodGet, path, nil)
}

func post[T any](ctx context.Context, client *http.Client, path string, body any) (T, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		var zero T
		return zero, err
	}
	return request[T](ctx, client, http.MethodPost, path, bytes.NewReader(payload))
}

func request[T any](ctx context.Context, client *http.Client, method, path string, body io.Reader) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return zero, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("host agent unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError protocol.Error
		if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&apiError); err != nil {
			return zero, fmt.Errorf("host agent returned HTTP %d", response.StatusCode)
		}
		return zero, &Error{Status: response.StatusCode, Code: apiError.Error, Message: apiError.Message}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 512*1024)).Decode(&zero); err != nil {
		return zero, fmt.Errorf("decode host agent response: %w", err)
	}
	return zero, nil
}

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return strings.TrimSpace(e.Code + ": " + e.Message)
}
