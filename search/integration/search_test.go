//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestMeilisearchThroughStdio(t *testing.T) {
	base, embedder := os.Getenv("MEILI_TEST_URL"), os.Getenv("MEILI_TEST_EMBEDDER_URL")
	if base == "" || embedder == "" {
		t.Fatal("MEILI_TEST_URL and MEILI_TEST_EMBEDDER_URL are required; integration never silently skips")
	}
	adminKey := os.Getenv("MEILI_TEST_MASTER_KEY")
	if adminKey == "" {
		t.Fatal("MEILI_TEST_MASTER_KEY is required")
	}
	httpClient := &http.Client{Timeout: 15 * time.Second}
	request := func(method, path, key string, body any) map[string]any {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(b)
		}
		r, err := http.NewRequest(method, base+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		res, err := httpClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			t.Fatalf("%s %s returned %d", method, path, res.StatusCode)
		}
		var result map[string]any
		if res.StatusCode != 204 {
			if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	wait := func(task map[string]any) {
		t.Helper()
		uid, ok := task["taskUid"].(float64)
		if !ok {
			t.Fatal("missing async task id")
		}
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			d := request("GET", fmt.Sprintf("/tasks/%.0f", uid), adminKey, nil)
			switch d["status"] {
			case "succeeded":
				return
			case "failed", "canceled":
				t.Fatalf("task failed: %v", d["error"])
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("indexing task timed out")
	}
	index := fmt.Sprintf("search_test_%d", time.Now().UnixNano())
	wait(request("POST", "/indexes", adminKey, map[string]any{"uid": index, "primaryKey": "id"}))
	t.Cleanup(func() { wait(request("DELETE", "/indexes/"+index, adminKey, nil)) })
	settings := map[string]any{
		"searchableAttributes": []string{"title", "content"},
		"filterableAttributes": []string{"id", "organization_id", "deployment_id", "corpus", "index_revision"},
		"embedders":            map[string]any{"fixture": map[string]any{"source": "rest", "url": embedder, "dimensions": 3, "request": map[string]any{"input": "{{text}}"}, "response": map[string]any{"embedding": "{{embedding}}"}, "documentTemplate": "{{doc.title}} {{doc.content}}"}},
	}
	wait(request("PATCH", "/indexes/"+index+"/settings", adminKey, settings))
	document := func(id, title, content, org string) map[string]any {
		return map[string]any{"id": id, "document_id": "doc_" + id, "title": title, "content": content, "source_ref": "storage:doc_" + id, "position": "section-1", "source_revision": "r1", "organization_id": org, "deployment_id": "dep-a", "corpus": "manual", "index_revision": "v1"}
	}
	docs := []map[string]any{
		document("contract", "Contrat ACME", "Résilier le contrat fournisseur par notification écrite.", "org-a"),
		document("holiday", "Vacation policy", "Annual leave requests need manager approval.", "org-a"),
		document("secret", "Contrat ACME confidentiel", "Secret supplier contract.", "org-b"),
	}
	wait(request("POST", "/indexes/"+index+"/documents", adminKey, docs))
	keyData := request("POST", "/keys", adminKey, map[string]any{"description": "ephemeral MCP Search integration", "actions": []string{"search"}, "indexes": []string{index}, "expiresAt": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
	searchKey, ok := keyData["key"].(string)
	if !ok || searchKey == "" {
		t.Fatal("missing search credential")
	}
	keyUID := keyData["uid"].(string)
	t.Cleanup(func() { request("DELETE", "/keys/"+keyUID, adminKey, nil) })
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "search")
	build := exec.Command("go", "build", "-o", binary, "../cmd/search")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, b)
	}
	keyFile := filepath.Join(tmp, "key")
	if err := os.WriteFile(keyFile, []byte(searchKey), 0600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(tmp, "config.json")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := func(revision string) *client.Client {
		t.Helper()
		config := map[string]any{"meili_url": base, "organization_id": "org-a", "deployment_id": "dep-a", "principal_id": "alice", "corpus": "manual", "index_uid": index, "index_revision": revision, "embedder": "fixture"}
		b, _ := json.Marshal(config)
		if err := os.WriteFile(configFile, b, 0600); err != nil {
			t.Fatal(err)
		}
		c, err := client.NewStdioMCPClient(binary, []string{"SEARCH_CONFIG_FILE=" + configFile, "SEARCH_API_KEY_FILE=" + keyFile, "MCP_TRANSPORT=stdio"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		var init mcp.InitializeRequest
		init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		init.Params.ClientInfo = mcp.Implementation{Name: "search-integration", Version: "1.0"}
		if _, err := c.Initialize(ctx, init); err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := start("v1")
	listed, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(listed.Tools) != 4 {
		t.Fatalf("tools: %v %v", listed, err)
	}
	call := func(c *client.Client, name string, args map[string]any, wantError bool) map[string]any {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = name
		req.Params.Arguments = args
		r, err := c.CallTool(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if r.IsError != wantError {
			t.Fatalf("%s isError=%v want=%v", name, r.IsError, wantError)
		}
		b, _ := json.Marshal(r.StructuredContent)
		var data map[string]any
		if err := json.Unmarshal(b, &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	call(c, "search_health_v1", map[string]any{}, false)
	caps := call(c, "search_capabilities_v1", map[string]any{}, false)
	if caps["managed_multiuser"] != false {
		t.Fatal("unexpected multiuser claim")
	}
	for _, test := range []struct{ mode, query, want string }{{"keyword", "ACME", "contract"}, {"semantic", "cancel supplier subscription", "contract"}, {"hybrid", "cancel supplier subscription", "contract"}, {"semantic", "demander des vacances", "holiday"}} {
		t.Run(test.mode+"_"+test.want, func(t *testing.T) {
			r := call(c, "search_query_v1", map[string]any{"corpus": "manual", "query": test.query, "mode": test.mode, "limit": 1}, false)
			hits := r["results"].([]any)
			if len(hits) != 1 || hits[0].(map[string]any)["id"] != test.want {
				t.Fatalf("unexpected result: %+v", r)
			}
		})
	}
	r := call(c, "search_get_v1", map[string]any{"corpus": "manual", "passage_id": "contract"}, false)
	if r["results"].([]any)[0].(map[string]any)["source_ref"] != "storage:doc_contract" {
		t.Fatal("missing citation")
	}
	for _, args := range []map[string]any{{"corpus": "manual", "passage_id": "secret"}, {"corpus": "manual", "passage_id": "missing"}, {"corpus": "private", "passage_id": "contract"}} {
		if call(c, "search_get_v1", args, true)["error"] != "not_found" {
			t.Fatal("existence leak")
		}
	}
	call(c, "search_query_v1", map[string]any{"corpus": "manual", "query": "ACME", "organization_id": "org-b"}, true)
	empty := call(c, "search_query_v1", map[string]any{"corpus": "manual", "query": "zzzzqqqqnonexistent", "mode": "keyword"}, false)
	if len(empty["results"].([]any)) != 0 {
		t.Fatal("expected no keyword results")
	}
	// Publish a new revision only after the indexing task succeeds.
	c.Close()
	for _, d := range docs {
		d["index_revision"] = "v2"
	}
	docs[0]["content"] = "Contrat actualisé : préavis de trente jours."
	docs[0]["source_revision"] = "r2"
	wait(request("POST", "/indexes/"+index+"/documents", adminKey, docs))
	c = start("v2")
	updated := call(c, "search_get_v1", map[string]any{"corpus": "manual", "passage_id": "contract"}, false)
	if updated["index_revision"] != "v2" || updated["results"].([]any)[0].(map[string]any)["source_revision"] != "r2" {
		t.Fatal("stale revision")
	}
	c.Close()
	wait(request("DELETE", "/indexes/"+index+"/documents/contract", adminKey, nil))
	c = start("v2")
	call(c, "search_get_v1", map[string]any{"corpus": "manual", "passage_id": "contract"}, true)
	// A same-ID source moved out of scope must also become inaccessible.
	c.Close()
	docs[1]["organization_id"] = "org-b"
	wait(request("POST", "/indexes/"+index+"/documents", adminKey, []map[string]any{docs[1]}))
	c = start("v2")
	call(c, "search_get_v1", map[string]any{"corpus": "manual", "passage_id": "holiday"}, true)
	// The binary refuses network transport and missing binding, even with a key.
	bad := exec.Command(binary)
	bad.Env = append(os.Environ(), "MCP_TRANSPORT=http", "SEARCH_CONFIG_FILE="+configFile, "SEARCH_API_KEY_FILE="+keyFile)
	if err := bad.Run(); err == nil {
		t.Fatal("HTTP transport was exposed")
	}
	bad = exec.Command(binary)
	bad.Env = append(os.Environ(), "MCP_TRANSPORT=stdio", "SEARCH_CONFIG_FILE=", "SEARCH_API_KEY_FILE="+keyFile)
	if err := bad.Run(); err == nil {
		t.Fatal("missing identity binding was accepted")
	}
}
