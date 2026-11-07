package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// The promotion manifest lists the tools of the server; it must match the
// tools this server registers, or a promotion publishes a wrong catalog.
// The manifest is not part of the public mirror: the test skips when absent.
func TestAdminManifestListsEveryRegisteredTool(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "mcp-"+"admin.json"))
	if os.IsNotExist(err) {
		t.Skip("promotion manifest absent (public mirror)")
	}
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Catalog struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	declared := make([]string, 0, len(manifest.Catalog.Tools))
	for _, tool := range manifest.Catalog.Tools {
		declared = append(declared, tool.Name)
	}
	registered := make([]string, 0)
	for name := range New().Legacy().ListTools() {
		registered = append(registered, name)
	}
	sort.Strings(declared)
	sort.Strings(registered)
	if !reflect.DeepEqual(declared, registered) {
		t.Fatalf("manifest tools %v differ from registered tools %v", declared, registered)
	}
}
