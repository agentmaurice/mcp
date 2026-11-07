// Package templatetest loads the contract fixtures copied under testdata/contract
// (see testdata/contract/README.md) as inline templates, for tests.
package templatetest

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
)

// Root is the fixtures directory.
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "contract", "golden")
}

// Load reads a template folder (valid/<name> or invalid/<name>) as an inline template.
func Load(t testing.TB, rel string) template.Template {
	t.Helper()
	dir := filepath.Join(Root(), rel)
	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		r, _ := filepath.Rel(dir, path)
		if r == "manifest.json" || r == "expected_error.txt" {
			return nil
		}
		b, err := os.ReadFile(path)
		files[filepath.ToSlash(r)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return template.Template{Manifest: manifest, Files: files}
}

// Data returns the example data of a template folder.
func Data(t testing.TB, tpl template.Template, example string) any {
	t.Helper()
	var m struct {
		Examples []struct{ Name, Data string } `json:"examples"`
	}
	if err := json.Unmarshal(tpl.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Examples {
		if example == "" || e.Name == example {
			var v any
			if err := json.Unmarshal([]byte(tpl.Files[e.Data]), &v); err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("example %q not found", example)
	return nil
}

// Context returns the fictitious platform context and the logo asset.
func Context(t testing.TB) (template.Context, map[string][]byte) {
	t.Helper()
	dir := filepath.Join(Root(), "context")
	read := func(name string) map[string]any {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	logo, err := os.ReadFile(filepath.Join(dir, "logo.svg"))
	if err != nil {
		t.Fatal(err)
	}
	return template.Context{
		Company: read("company.example.json"), Brand: read("brand.example.json"),
		Doc: read("doc.example.json"), I18n: read("i18n.fr.json"),
	}, map[string][]byte{"logo.svg": logo}
}
