package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/ocr/internal/bridge"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/agentmaurice/mcpchatui/mcp/shared/sidecar"
	"github.com/agentmaurice/mcpchatui/mcp/shared/version"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serviceVersion = "0.1.0"

func main() {
	if version.HandleVersionFlag() {
		return
	}
	if _, err := sidecar.RegisterIfConfigured(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "sidecar registration failed: %v\n", err)
	}
	client := bridge.NewFromEnv()
	mcpServer := modernmcp.New(
		"agentmaurice-ocr",
		serviceVersion,
		"Use ocr_extract_v1 to extract structured Markdown from PDFs and images. Large results and images are returned as opaque Buffer references.",
	)
	mcpServer.AddTool(healthTool(), healthHandler(client))
	mcpServer.AddTool(capabilitiesTool(), capabilitiesHandler())
	mcpServer.AddTool(extractTool(), extractHandler(client))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpServer.Serve(ctx, modernmcp.ConfigFromEnv("/mcp/ocr")); err != nil {
		fmt.Fprintf(os.Stderr, "OCR MCP server exited: %v\n", err)
		os.Exit(1)
	}
}

func healthTool() mcplib.Tool {
	return mcplib.Tool{Name: "ocr_health_v1", Description: "Check the OCR MCP and hosted bridge without consuming credits.", InputSchema: mcplib.ToolInputSchema{Type: "object", Properties: map[string]interface{}{}}}
}

func capabilitiesTool() mcplib.Tool {
	return mcplib.Tool{Name: "ocr_capabilities_v1", Description: "Describe supported OCR formats, limits, page selection and output structure.", InputSchema: mcplib.ToolInputSchema{Type: "object", Properties: map[string]interface{}{}}}
}

func extractTool() mcplib.Tool {
	readOnly := true
	destructive := false
	idempotent := true
	return mcplib.Tool{
		Name:        "ocr_extract_v1",
		Description: "Extract Markdown, pages, tables and optional images from exactly one source_ref or source_url using hosted OCR. Page indexes are zero-based.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"source_ref":     map[string]interface{}{"type": "string", "description": "storage://, buffer:// or bounded base64 data: reference"},
				"source_url":     map[string]interface{}{"type": "string", "description": "Public HTTP(S) URL without embedded credentials"},
				"pages":          map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "integer", "minimum": 0}, "description": "Optional zero-based page indexes"},
				"include_images": map[string]interface{}{"type": "boolean", "default": false},
				"request_id":     map[string]interface{}{"type": "string", "description": "Optional idempotency key"},
			},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent},
	}
}

func healthHandler(client *bridge.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := client.Health(ctx); err != nil {
			return errorResult("hosted_bridge_unavailable", err.Error()), nil
		}
		return structuredResult(map[string]interface{}{"status": "ok", "version": serviceVersion, "hosted_bridge": "reachable"}), nil
	}
}

func capabilitiesHandler() server.ToolHandlerFunc {
	return func(context.Context, mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		return structuredResult(map[string]interface{}{
			"version": "v1", "mode": "hosted", "synchronous": true,
			"formats":          []string{"application/pdf", "image/jpeg", "image/png", "image/webp"},
			"max_source_bytes": 25 * 1024 * 1024, "page_indexes": "zero_based", "partial_page_selection": true,
			"include_images_default": false,
			"output":                 []string{"markdown", "pages", "tables", "images", "scores", "request_id", "model", "pages_processed", "credits_consumed", "buffer_ref"},
		}), nil
	}
}

func extractHandler(client *bridge.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var input bridge.ExtractRequest
		raw, err := json.Marshal(request.Params.Arguments)
		if err != nil || json.Unmarshal(raw, &input) != nil {
			return errorResult("invalid_arguments", "Arguments do not match the OCR schema."), nil
		}
		input.SourceRef = strings.TrimSpace(input.SourceRef)
		input.SourceURL = strings.TrimSpace(input.SourceURL)
		if (input.SourceRef == "") == (input.SourceURL == "") {
			return errorResult("invalid_arguments", "Provide exactly one of source_ref or source_url."), nil
		}
		result, err := client.Extract(ctx, input)
		if err != nil {
			return errorResult("ocr_failed", err.Error()), nil
		}
		return structuredResult(result), nil
	}
}

func structuredResult(content interface{}) *mcplib.CallToolResult {
	return &mcplib.CallToolResult{StructuredContent: content}
}

func errorResult(code, message string) *mcplib.CallToolResult {
	return &mcplib.CallToolResult{IsError: true, StructuredContent: map[string]interface{}{"error": code, "message": message}}
}
