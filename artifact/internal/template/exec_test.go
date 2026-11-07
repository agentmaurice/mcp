package template

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Values pass by reference: no value of the injection cases of the contract
// (tests/test_contract.py) reaches the resolved Typst source.
func TestValuesPassByReference(t *testing.T) {
	cases := []struct {
		name, main string
		data       map[string]any
	}{
		{"code string", "#{ let x = \"{{ .data.x }}\"; x }\n", map[string]any{"x": "\"; panic(\"INJECT\"); let y = \""}},
		{"hash glued", "#{{ .data.x }}\n", map[string]any{"x": "pagebreak()"}},
		{"hash glued ident", "#text{{ .data.x }}\n", map[string]any{"x": "[#pagebreak() INJECT]"}},
		{"hash plus if", "#{{ if true }}{{ .data.x }}{{ end }}\n", map[string]any{"x": "pagebreak()"}},
		{"backslash", "\\{{ .data.x }}\n", map[string]any{"x": "#pagebreak()"}},
		{"range keys", "[{{ range $k, $v := .data }}{{ $k }}{{ end }}]\n", map[string]any{"#pagebreak() #text[INJECT]": 1}},
		{"printf", "[{{ printf \"%q\" .data.x }}]\n", map[string]any{"x": "#pagebreak() INJECT"}},
		{"markup", "[{{ .data.x }}]\n", map[string]any{"x": "*#pagebreak()* _INJECT_ $x$ `y` <l> @r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex, _ := newExecutor(context.Background(), EngineTypst, map[string]any{"data": tc.data}, 0)
			out, err := ex.render(tc.main)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "INJECT") || strings.Contains(out, "pagebreak") {
				t.Fatalf("a value reached the source: %s", out)
			}
			if !strings.HasPrefix(out, "#let __v = json(\"/values.json\")") {
				t.Fatalf("missing values table header: %s", out)
			}
		})
	}
}

func TestAbsentValueAndDefault(t *testing.T) {
	ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
	if _, err := ex.render("{{ .data.x }}"); err == nil {
		t.Fatal("an absent value must fail")
	}
	if out, err := ex.render(`{{ .data.x | default "-" }}`); err != nil || out != "-" {
		t.Fatalf("default: %q %v", out, err)
	}
}

func TestForbiddenAndUnknownFunctions(t *testing.T) {
	for src, want := range map[string]string{
		"{{ now }}": CodeForbiddenFunction, "{{ raw .x }}": CodeForbiddenFunction, "{{ escTypst .x }}": CodeForbiddenFunction,
		"{{ __present .x }}": CodeForbiddenFunction, "{{ call .x }}": CodeForbiddenFunction,
		"{{ if (call .f) }}x{{ end }}": CodeForbiddenFunction, "{{ key }}": CodeUnknownFunction, "{{ else }}": CodeTemplateSyntax,
	} {
		err := checkFunctions(src)
		e, ok := err.(*Error)
		if !ok || e.Code != want {
			t.Errorf("%q: want %s, got %v", src, want, err)
		}
	}
	if err := checkFunctions(`{{ .data.x | default "" }}{{ json .data }}`); err != nil {
		t.Fatal(err)
	}
}

// Finding 2 (pass 2): an action inside a Typst string or any code context is refused.
func TestTypstCodeContext(t *testing.T) {
	refused := []string{
		"#{ let x = \"{{ .data.x }}\"; x }\n",
		"#let a = \"{{ .data.x }}\"\n",
		"#set text(font: \"{{ .data.x }}\")\n",
		"#f(\"{{ .data.x }}\")\n",
		"#{ {{ .data.x }} }\n",
		"#import {{ .data.x }}\n",
	}
	for _, src := range refused {
		if err := typstCodeContext(src); err == nil || err.(*Error).Code != CodeTypstCodeContext {
			t.Errorf("%q must be refused, got %v", src, err)
		}
	}
	allowed := []string{
		"[{{ .data.x }}]\n",
		"#f([{{ .data.x }}], [{{ .data.y }}])\n",
		"#let a = [{{ .data.x }}]\n",
		"#grid(\n{{- range .data.l }}\n  [{{ .x }}],\n{{- end }}\n)\n",
		"{{ $v := .data.x }}text {{ $v }}\n",
		"= {{ .data.title }}\n- {{ .data.x }}\n",
		"#if true [{{ .data.x }}]\n",
	}
	for _, src := range allowed {
		if err := typstCodeContext(src); err != nil {
			t.Errorf("%q must be allowed, got %v", src, err)
		}
	}
}

// Finding 2 (pass 3): raw, math and comment contexts use the contract codes.
func TestTypstRawMathCommentContexts(t *testing.T) {
	for src, want := range map[string]string{
		"`{{ .data.x }}`\n":         CodeTypstRawContext,
		"```\n{{ .data.x }}\n```\n": CodeTypstRawContext,
		"$ {{ .data.x }} $\n":       CodeTypstMathContext,
		"// {{ .data.x }}\n":        CodeTypstCommentContext,
		"/* {{ .data.x }} */\n":     CodeTypstCommentContext,
		"[`{{ .data.x }}`]\n":       CodeTypstRawContext,
	} {
		err := typstCodeContext(src)
		if e, ok := err.(*Error); !ok || e.Code != want {
			t.Errorf("%q: want %s, got %v", src, want, err)
		}
	}
	// Control actions and plain markup stay allowed around them.
	for _, src := range []string{"`code` and [{{ .data.x }}]\n", "$x^2$ {{ if .data.x }}[{{ .data.x }}]{{ end }}\n", "// plain comment\n"} {
		if err := typstCodeContext(src); err != nil {
			t.Errorf("%q must be allowed: %v", src, err)
		}
	}
}

// Finding 3 (pass 3): the text "<no value>" is a legitimate value; absence is detected by __present.
func TestLiteralNoValueTextIsNotAbsence(t *testing.T) {
	for _, engine := range []string{EngineHTML, EngineText} {
		ex, _ := newExecutor(context.Background(), engine, map[string]any{"data": map[string]any{"x": "<no value>"}}, 0)
		out, err := ex.render("{{ .data.x }}")
		if err != nil || !strings.Contains(out, "no value") {
			t.Fatalf("%s: a literal value must render: %q %v", engine, out, err)
		}
		if _, err := ex.render("{{ .data.missing }}"); err == nil {
			t.Fatalf("%s: an absent key must still fail", engine)
		}
	}
}

// An absent value passed to a function (printf, index, ...) is an error, not a
// string such as "%!s(<nil>)". Guards (default, if, with, range) stay free.
func TestAbsentArgumentsAreErrors(t *testing.T) {
	run := func(engine, src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), engine, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	printfSrc := `{{ printf "%s%s" (.data.x | default "") .data.y }}`
	for _, engine := range []string{EngineText, EngineHTML, EngineTypst} {
		if out, err := run(engine, printfSrc, map[string]any{"x": "a"}); err == nil {
			t.Errorf("%s: y absent must fail, got %q", engine, out)
		}
	}
	if out, err := run(EngineText, printfSrc, map[string]any{"y": "b"}); err != nil || out != "b" {
		t.Errorf("x absent, y present: want b, got %q %v", out, err)
	}
	if out, err := run(EngineText, printfSrc, map[string]any{"x": "a", "y": "b"}); err != nil || out != "ab" {
		t.Errorf("both present: %q %v", out, err)
	}
	for _, src := range []string{
		`{{ index .data.m "k" }}`,
		`{{ printf "%s" (index .data.m "k") }}`,
		`{{ .data.l | len }}`,
		`{{ printf "%s" (.data.x | default .data.z) }}`,
	} {
		if out, err := run(EngineText, src, map[string]any{"m": map[string]any{}}); err == nil {
			t.Errorf("%s must fail, got %q", src, out)
		}
	}
	for _, src := range []string{
		`{{ if .data.flag }}x{{ end }}`,
		`{{ with .data.w }}x{{ end }}`,
		`{{ range .data.r }}x{{ end }}`,
		`{{ .data.x | default "-" }}`,
		`{{ default "-" .data.x }}`,
	} {
		if _, err := run(EngineText, src, map[string]any{}); err != nil {
			t.Errorf("%s must not fail: %v", src, err)
		}
	}
}

// Pass 4, finding 1: a variable holding an absent value is checked like a field.
func TestAbsentVariablesAreErrors(t *testing.T) {
	run := func(src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	for _, src := range []string{
		`{{ $x := .data.absent }}{{ printf "%s" $x }}`,
		`{{ $x := .data.absent }}{{ $x }}`,
		`{{ $x := .data.absent }}{{ $x | printf "%s" }}`,
	} {
		if out, err := run(src, map[string]any{}); err == nil {
			t.Errorf("%s must fail, got %q", src, out)
		}
	}
	for _, src := range []string{
		`{{ $x := .data.absent | default "" }}{{ printf "%s" $x }}`,
		`{{ $x := .data.absent }}{{ if $x }}y{{ end }}`,
		`{{ printf "%s" $.data.s }}`,
		`{{ $x := .data.s }}{{ printf "%s" $x }}`,
	} {
		if _, err := run(src, map[string]any{"s": "v"}); err != nil {
			t.Errorf("%s must not fail: %v", src, err)
		}
	}
}

// Pass 5: declarations get the same argument checks; their final result stays free.
func TestDeclarationsCannotHideAbsence(t *testing.T) {
	run := func(src string) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
		return ex.render(src)
	}
	for _, src := range []string{
		`{{ $x := printf "%s" .data.absent }}{{ $x }}`,
		`{{ range $i, $x := printf "%s" .data.absent }}y{{ end }}`,
	} {
		if out, err := run(src); err == nil {
			t.Errorf("%s must fail, got %q", src, out)
		}
	}
	for _, src := range []string{
		`{{ $x := printf "%s" (.data.absent | default "") }}[{{ $x }}]`,
		`{{ $x := .data.absent }}{{ if $x }}y{{ end }}`,
		`{{ range $i, $x := .data.absent }}y{{ end }}`,
	} {
		if _, err := run(src); err != nil {
			t.Errorf("%s must not fail: %v", src, err)
		}
	}
}

// Pass 6, finding 1: the result of every command that feeds the next one is checked.
func TestAbsentValueBetweenPipelineCommands(t *testing.T) {
	run := func(src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	for _, src := range []string{
		`{{ index .data "absent" | printf "%s" }}`,
		`{{ $x := index .data "absent" | printf "%s" }}{{ $x }}`,
		`{{ printf "%s" (index .data "absent" | printf "%s") }}`,
	} {
		if out, err := run(src, map[string]any{}); err == nil {
			t.Errorf("%s must fail, got %q", src, out)
		}
	}
	for src, want := range map[string]string{
		`{{ index .data "absent" | default "x" | printf "%s" }}`: "x",
		`{{ .data.s | printf "%s" | printf "[%s]" }}`:            "[v]",
		`{{ .data.absent | default "d" }}`:                       "d",
		`{{ .data.absent | default "d" | printf "%s" }}`:         "d",
	} {
		if out, err := run(src, map[string]any{"s": "v"}); err != nil || out != want {
			t.Errorf("%s: want %q, got %q %v", src, want, out, err)
		}
	}
}

// Pass 7: sub-template calls get the same checks, and define bodies are processed.
func TestSubTemplateCalls(t *testing.T) {
	for _, engine := range []string{EngineText, EngineHTML, EngineTypst} {
		run := func(src string) (string, error) {
			ex, _ := newExecutor(context.Background(), engine, map[string]any{"data": map[string]any{}}, 0)
			return ex.render(src)
		}
		if out, err := run(`{{ define "echo" }}{{ . }}{{ end }}{{ template "echo" (printf "%s" .data.absent) }}`); err == nil {
			t.Errorf("%s: absent argument through a template call must fail, got %q", engine, out)
		}
		if out, err := run(`{{ define "echo" }}{{ . }}{{ end }}{{ template "echo" (.data.absent | default "d") }}`); err != nil || (engine == EngineText && out != "d") {
			t.Errorf("%s: default guard must pass: %q %v", engine, out, err)
		}
		// the body checks at use: an absent value printed inside a define fails
		if _, err := run(`{{ define "x" }}{{ .absent }}{{ end }}{{ template "x" .data }}`); err == nil {
			t.Errorf("%s: define body output must be checked", engine)
		}
		// but passing an absent value is allowed (checked at use)
		if _, err := run(`{{ define "x" }}ok{{ end }}{{ template "x" .data.absent }}`); err != nil {
			t.Errorf("%s: passing an absent value must be allowed: %v", engine, err)
		}
	}
}

func TestSilentLoopInsideDefineStopsAtDeadline(t *testing.T) {
	items := make([]any, 10000)
	for i := range items {
		items[i] = float64(i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ex, _ := newExecutor(ctx, EngineText, map[string]any{"data": map[string]any{"a": items}}, 0)
	src := `{{ define "loop" }}{{range .a}}{{range $.a}}{{range $.a}}{{range $.a}}{{ $x := . }}{{end}}{{end}}{{end}}{{end}}{{ end }}{{ template "loop" .data }}`
	before := runtime.NumGoroutine()
	start := time.Now()
	_, err := ex.render(src)
	if e, ok := err.(*Error); !ok || e.Code != CodeRenderTimeout {
		t.Fatalf("want render_timeout, got %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutine leaked: %d -> %d", before, after)
	}
}

// Pass 8, finding 1: the dot of a sub-template or range body is checked like a field.
func TestDotCannotHideAbsence(t *testing.T) {
	run := func(src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	if out, err := run(`{{ define "echo" }}{{ printf "%s" . }}{{ end }}{{ template "echo" .data.absent }}`, map[string]any{}); err == nil {
		t.Errorf("printf of an absent dot must fail, got %q", out)
	}
	if out, err := run(`{{ define "echo" }}{{ . }}{{ end }}{{ template "echo" .data.absent }}`, map[string]any{}); err == nil {
		t.Errorf("a bare absent dot must fail through the final check, got %q", out)
	}
	if out, err := run(`{{ define "e" }}{{ . | default "d" }}{{ end }}{{ template "e" .data.absent }}`, map[string]any{}); err != nil || out != "d" {
		t.Errorf("default on the dot: want d, got %q %v", out, err)
	}
	if out, err := run(`{{ range .data.items }}{{ printf "%s" . }}{{ end }}`, map[string]any{"items": []any{"a", "b"}}); err != nil || out != "ab" {
		t.Errorf("range over present items: %q %v", out, err)
	}
}

// Pass 9: the values table counts in a budget, checked before a value is stored.
func TestValuesTableIsBounded(t *testing.T) {
	items := make([]any, 10000)
	for i := range items {
		items[i] = float64(i)
	}
	big := strings.Repeat("a", 2_500_000)
	ex, _ := newExecutor(context.Background(), EngineTypst, map[string]any{"data": map[string]any{"items": items, "x": big}}, 0)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err := ex.render(`{{ range .data.items }}{{ $.data.x }}{{ end }}`)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("took %s", elapsed)
	}
	if ex.valuesBytes > DefaultMaxValuesBytes {
		t.Fatalf("table grew to %d bytes", ex.valuesBytes)
	}
	if after.HeapAlloc > 200<<20 {
		t.Fatalf("heap %d MiB", after.HeapAlloc>>20)
	}
}

// Pass 10, finding 1: conditions check function arguments; only their final result is free.
func TestConditionsCheckFunctionArguments(t *testing.T) {
	run := func(src string) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
		return ex.render(src)
	}
	for _, src := range []string{
		`{{ with printf "%s" .data.absent }}{{ . }}{{ end }}`,
		`{{ if eq .data.flag true }}x{{ end }}`,
		`{{ if not .data.flag }}x{{ end }}`, // not is a function too: nil is refused
		`{{ range printf "%s" .data.absent }}x{{ end }}`,
		`{{ if index .data "absent" | printf "%s" }}x{{ end }}`,
	} {
		if out, err := run(src); err == nil {
			t.Errorf("%s must fail, got %q", src, out)
		}
	}
	for _, src := range []string{
		`{{ with .data.absent }}x{{ end }}`,
		`{{ if .data.flag }}x{{ end }}`,
		`{{ range .data.items }}x{{ end }}`,
	} {
		if out, err := run(src); err != nil {
			t.Errorf("%s must not fail: %v (%q)", src, err, out)
		}
	}
}

// Pass 10, finding 2: the budget counts the JSON-encoded size, as written in values.json.
func TestValuesBudgetCountsJSONEncoding(t *testing.T) {
	lt := strings.Repeat("<", 100_000) // 6 bytes each once encoded
	run := func(n int) (*executor, error) {
		items := make([]any, n)
		for i := range items {
			items[i] = float64(i)
		}
		ex, _ := newExecutor(context.Background(), EngineTypst, map[string]any{"data": map[string]any{"items": items, "x": lt}}, 0)
		_, err := ex.render(`{{ range .data.items }}{{ $.data.x }}{{ end }}`)
		return ex, err
	}
	ex, err := run(20) // 20 x 600 001 bytes encoded > 8 MiB
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if len(ex.valuesJSON()) > DefaultMaxValuesBytes {
		t.Fatalf("file larger than the budget: %d", len(ex.valuesJSON()))
	}
	ex, err = run(10) // 6 MB: fits
	if err != nil {
		t.Fatal(err)
	}
	if got := len(ex.valuesJSON()); got > ex.valuesBytes || got > DefaultMaxValuesBytes {
		t.Fatalf("written file %d bytes exceeds the counted budget %d", got, ex.valuesBytes)
	}
}

// Pass 11, finding 1: intermediate values are bounded in size and time.
func TestDoublingStringStopsAtTheBudget(t *testing.T) {
	src := `{{ $x := "a" }}` + strings.Repeat(`{{ $x = printf "%s%s" $x $x }}`, 35) + `{{ $x }}`
	ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
	runtime.GC()
	start := time.Now()
	_, err := ex.render(src)
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapAlloc > 200<<20 {
		t.Fatalf("heap %d MiB", m.HeapAlloc>>20)
	}
	for name, s := range map[string]string{
		"huge width": `{{ printf "%2000000000s" "a" }}`,
		"print":      `{{ $x := "a" }}` + strings.Repeat(`{{ $x = print $x $x }}`, 35),
		"html":       `{{ $x := "<" }}` + strings.Repeat(`{{ $x = printf "%s%s" $x $x }}`, 30),
	} {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
		if _, err := ex.render(s); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
	if out, err := func() (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{"s": "v"}}, 0)
		return ex.render(`{{ printf "%s-%03d" .data.s 7 }}{{ .data.s | print | println }}`)
	}(); err != nil || out != "v-007v\n" {
		t.Fatalf("normal printf/print must still work: %q %v", out, err)
	}
}

// Pass 12: printf is bounded by a walk of the format, before fmt allocates.
func TestPrintfBoundRefusesBeforeAllocating(t *testing.T) {
	format := strings.Repeat("%[1]1000000s", 10000) // under 1 MiB of source, ~10 GB of output
	run := func(src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	runtime.GC()
	start := time.Now()
	_, err := run(`{{ printf "`+format+`" "a" }}`, map[string]any{})
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapAlloc > 100<<20 {
		t.Fatalf("heap %d MiB", m.HeapAlloc>>20)
	}
	if _, err := run(`{{ printf "%[1]*s" .data.w "a" }}`, map[string]any{"w": float64(2000000000)}); err == nil {
		t.Fatal("a width from the data must be refused")
	}
	if _, err := run(`{{ printf "%.*f" .data.w 1.5 }}`, map[string]any{"w": float64(2000000000)}); err == nil {
		t.Fatal("a precision from the data must be refused")
	}
	if _, err := run(`{{ printf "%v" .data.big }}`, map[string]any{"big": strings.Repeat("a", 30<<20)}); err == nil {
		t.Fatal("a huge argument must be refused")
	}
	for src, want := range map[string]string{
		`{{ printf "%s-%03d|%5.1f|%q|%x|%%|%[2]d" "a" 7 }}`: "a-007|%!f(MISSING)",
		`{{ printf "%-5s|%5s|" "ab" "cd" }}`:                "ab   |   cd|",
		`{{ printf "%v" .data.m }}`:                         "map[k:v]",
		`{{ printf "%[2]s %[1]s" "a" "b" }}`:                "b a",
		`{{ printf "%*d" 5 42 }}`:                           "   42",
	} {
		out, err := run(src, map[string]any{"m": map[string]any{"k": "v"}})
		if err != nil {
			t.Errorf("%s must work: %v", src, err)
		} else if want != "" && !strings.HasPrefix(out, strings.SplitN(want, "|%!", 2)[0]) {
			t.Errorf("%s: got %q, want prefix of %q", src, out, want)
		}
	}
}

// Pass 13: a width applies to every element of a collection argument.
func TestPrintfBoundCountsCollectionElements(t *testing.T) {
	items := make([]any, 30000)
	for i := range items {
		items[i] = "x"
	}
	run := func(src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	runtime.GC()
	start := time.Now()
	_, err := run(`{{ printf "%1000000s" .data.items }}`, map[string]any{"items": items})
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapAlloc > 100<<20 {
		t.Fatalf("heap %d MiB", m.HeapAlloc>>20)
	}
	nested := map[string]any{"a": map[string]any{"b": []any{"x", "y"}, "c": map[string]any{"d": "z"}}}
	if _, err := run(`{{ printf "%10000000v" .data.n }}`, map[string]any{"n": nested}); err == nil {
		t.Fatal("a width over a nested map must be refused")
	}
	for src, want := range map[string]string{
		`{{ printf "%v" .data.l }}`:  "[a b]",
		`{{ printf "%5v" .data.l }}`: "[    a     b]",
	} {
		if out, err := run(src, map[string]any{"l": []any{"a", "b"}}); err != nil || out != want {
			t.Errorf("%s: got %q %v, want %q", src, out, err, want)
		}
	}
}

// Pass 15: sub-templates that write nothing still reach the deadline check.
func TestDefineChainStopsAtDeadline(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{{ define "d0" }}{{ end }}`)
	for i := 1; i < 60; i++ {
		fmt.Fprintf(&b, `{{ define "d%d" }}{{ template "d%d" }}{{ template "d%d" }}{{ end }}`, i, i-1, i-1)
	}
	b.WriteString(`{{ template "d59" }}`)
	for _, engine := range []string{EngineText, EngineHTML, EngineTypst} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ex, _ := newExecutor(ctx, engine, map[string]any{"data": map[string]any{}}, 0)
		before := runtime.NumGoroutine()
		start := time.Now()
		_, err := ex.render(b.String())
		cancel()
		if e, ok := err.(*Error); !ok || e.Code != CodeRenderTimeout {
			t.Fatalf("%s: want render_timeout, got %v", engine, err)
		}
		if time.Since(start) > 4*time.Second {
			t.Fatalf("%s: took %s", engine, time.Since(start))
		}
		deadline := time.Now().Add(3 * time.Second)
		for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if after := runtime.NumGoroutine(); after > before+1 {
			t.Fatalf("%s: goroutine leaked: %d -> %d", engine, before, after)
		}
	}
}

func TestDefineCountLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, `{{ define "d%d" }}x{{ end }}`, i)
	}
	if err := checkFunctions(b.String()); err == nil || err.(*Error).Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	out, err := func() (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
		return ex.render(`{{ define "a" }}[{{ . }}]{{ end }}{{ template "a" "v" }}`)
	}()
	if err != nil || out != "[v]" {
		t.Fatalf("a normal define must render: %q %v", out, err)
	}
}

// Pass 16: the input_schema cost is bounded before compilation.
func TestSchemaCostBound(t *testing.T) {
	defs := map[string]any{"d0": map[string]any{"type": "string"}}
	for i := 1; i < 45; i++ {
		prev := map[string]any{"$ref": fmt.Sprintf("#/$defs/d%d", i-1)}
		defs[fmt.Sprintf("d%d", i)] = map[string]any{"allOf": []any{prev, prev}}
	}
	exp := map[string]any{"type": "object", "$defs": defs, "properties": map[string]any{"x": map[string]any{"$ref": "#/$defs/d44"}}}
	text, _ := json.Marshal(exp)
	var out any
	start := time.Now()
	err := validateInputSchema(string(text), &out)
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	cyclic := `{"type":"object","$defs":{"a":{"allOf":[{"$ref":"#/$defs/b"}]},"b":{"allOf":[{"$ref":"#/$defs/a"}]}},"properties":{"x":{"$ref":"#/$defs/a"}}}`
	if err := validateInputSchema(cyclic, &out); err == nil || err.(*Error).Code != CodeInputSchemaInvalid {
		t.Fatalf("a cyclic $ref must be refused: %v", err)
	}
	ok := `{"type":"object","$defs":{"s":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/s"},"b":{"type":"array","items":{"$ref":"#/$defs/s"}}}}`
	if err := validateInputSchema(ok, &out); err != nil {
		t.Fatalf("a normal schema must pass: %v", err)
	}
}

// Pass 17: a parenthesized operand feeding default keeps its result free.
func TestParenthesizedOperandBeforeDefault(t *testing.T) {
	run := func(src string) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
		return ex.render(src)
	}
	for src, want := range map[string]string{
		`{{ (.data.x) | default "-" }}`:              "-",
		`{{ default "-" (.data.x) }}`:                "-",
		`{{ ((.data.x)) | default "-" }}`:            "-",
		`{{ default "-" ((.data.x)) }}`:              "-",
		`{{ (.data.x | default "") | default "-" }}`: "-",
	} {
		if out, err := run(src); err != nil || out != want {
			t.Errorf("%s: want %q, got %q %v", src, want, out, err)
		}
	}
	for _, src := range []string{
		`{{ (printf "%s" .data.x) | default "-" }}`,
		`{{ default "-" (printf "%s" .data.x) }}`,
		`{{ ((printf "%s" .data.x)) | default "-" }}`,
		`{{ (.data.x) }}`,
		`{{ (.data.x) | printf "%s" }}`,
	} {
		if out, err := run(src); err == nil {
			t.Errorf("%s must fail, got %q", src, out)
		}
	}
}

// Pass 18: a pipeline inside a chain, (pipeline).field, is guarded too.
func TestChainOverPipelineIsGuarded(t *testing.T) {
	run := func(src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	for _, src := range []string{
		`{{ (index .data (printf "%s" .data.absent)).x }}`,
		`{{ printf "%s" (index .data (printf "%s" .data.absent)).x }}`,
		`{{ if (index .data (printf "%s" .data.absent)).x }}y{{ end }}`,
		`{{ $v := (index .data (printf "%s" .data.absent)).x }}`,
		`{{ default "-" (index .data (printf "%s" .data.absent)).x }}`,
	} {
		if out, err := run(src, map[string]any{}); err == nil {
			t.Errorf("%s must fail, got %q", src, out)
		}
	}
	data := map[string]any{"k": map[string]any{"x": "v"}}
	for src, want := range map[string]string{
		`{{ (index .data "k").x }}`:               "v",
		`{{ printf "%s" (index .data "k").x }}`:   "v",
		`{{ (index .data "k").x | default "-" }}`: "v",
	} {
		if out, err := run(src, data); err != nil || out != want {
			t.Errorf("%s: want %q, got %q %v", src, want, out, err)
		}
	}
}

// Pass 19, finding 1: print, println and the escapers are bounded before they run.
func TestPrintFamilyIsBoundedBeforeRunning(t *testing.T) {
	m := map[string]any{"x": strings.Repeat("a", 1_000_000)}
	args := strings.Repeat(" .data.m", 10000)
	for _, fn := range []string{"print", "println", "html", "js", "urlquery"} {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{"m": m}}, 0)
		runtime.GC()
		start := time.Now()
		_, err := ex.render(`{{ ` + fn + args + ` }}`)
		if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
			t.Fatalf("%s: want limits_exceeded, got %v", fn, err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("%s: took %s", fn, time.Since(start))
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		if ms.HeapAlloc > 100<<20 {
			t.Fatalf("%s: heap %d MiB", fn, ms.HeapAlloc>>20)
		}
	}
	ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{"s": "v", "n": 3}}, 0)
	if out, err := ex.render(`{{ print .data.s .data.n }}|{{ html "<b>" }}|{{ urlquery "a b" }}`); err != nil || out != "v3|&lt;b&gt;|a+b" {
		t.Fatalf("normal calls must work: %q %v", out, err)
	}
}

// Pass 19, finding 2: schema cost x data nodes is bounded before validating the data.
func TestValidationWorkBound(t *testing.T) {
	prop := map[string]any{"properties": map[string]any{"a": map[string]any{"type": "array", "items": map[string]any{"type": "number"}}}}
	copies := make([]any, 2000)
	for i := range copies {
		copies[i] = prop
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "allOf": copies})
	zeros := make([]any, 1_000_000)
	for i := range zeros {
		zeros[i] = float64(0)
	}
	tpl := Template{Manifest: json.RawMessage(`{"schema":"agentmaurice.template/v1","key":"work-bound","version":"1.0.0","kind":"page","profile":"web",
	"source":{"engine":"text","entry":"main.txt"},"outputs":["txt"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`),
		Files: map[string]string{"main.txt": "x", "input.schema.json": string(schema), "examples/default.json": "{}"}}
	start := time.Now()
	_, err := Validate(tpl, map[string]any{"a": zeros}, "txt", 0)
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

// Pass 20: the dialect is 2020-12 and the cost does not depend on keywords.
func TestSchemaDialectAndKeywordIndependentCost(t *testing.T) {
	chain := func(wrapper string) map[string]any {
		defs := map[string]any{"d0": map[string]any{"type": "string"}}
		for i := 1; i < 45; i++ {
			prev := map[string]any{"$ref": fmt.Sprintf("#/%s/d%d", wrapper, i-1)}
			defs[fmt.Sprintf("d%d", i)] = map[string]any{"allOf": []any{prev, prev}}
		}
		return map[string]any{"type": "object", wrapper: defs, "properties": map[string]any{"x": map[string]any{"$ref": "#/" + wrapper + "/d44"}}}
	}
	var out any
	// (b) draft-07 with definitions is refused by the dialect check
	d7 := chain("definitions")
	d7["$schema"] = "http://json-schema.org/draft-07/schema#"
	text, _ := json.Marshal(d7)
	if err := validateInputSchema(string(text), &out); err == nil || err.(*Error).Code != CodeInputSchemaInvalid {
		t.Fatalf("draft-07 must be refused: %v", err)
	}
	// (a) 2020-12, unknown keywords holding subschemas and prefixItems: refused by the cost
	hidden := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object"}
	defs := map[string]any{"d0": map[string]any{"type": "string"}}
	for i := 1; i < 45; i++ {
		prev := map[string]any{"$ref": fmt.Sprintf("#/$defs/d%d", i-1)}
		defs[fmt.Sprintf("d%d", i)] = map[string]any{"x-sub": []any{prev, prev}, "prefixItems": []any{prev, prev}}
	}
	hidden["$defs"] = defs
	hidden["properties"] = map[string]any{"x": map[string]any{"$ref": "#/$defs/d44"}}
	text, _ = json.Marshal(hidden)
	start := time.Now()
	if err := validateInputSchema(string(text), &out); err == nil || err.(*Error).Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	// non-local and cyclic references
	for _, ref := range []string{"https://example.com/x.json", "other.json#/a"} {
		if _, err := schemaCost(map[string]any{"properties": map[string]any{"a": map[string]any{"$ref": ref}}}); err == nil || err.(*Error).Code != CodeInputSchemaInvalid {
			t.Fatalf("%s must be refused: %v", ref, err)
		}
	}
	cyc := map[string]any{"x-a": map[string]any{"$ref": "#/x-b"}, "x-b": map[string]any{"$ref": "#/x-a"}}
	if _, err := schemaCost(cyc); err == nil || err.(*Error).Code != CodeInputSchemaInvalid {
		t.Fatalf("a cyclic reference must be refused: %v", err)
	}
	// a normal 2020-12 schema passes
	ok := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","$defs":{"s":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/s"}}}`
	if err := validateInputSchema(ok, &out); err != nil {
		t.Fatalf("2020-12 must pass: %v", err)
	}
}

// Pass 22: numbers format much longer than their JSON ("1e308" -> 317 characters with %f).
func TestPrintfBoundCountsNumberFormatting(t *testing.T) {
	nums := make([]any, 3000)
	for i := range nums {
		nums[i] = 1e308
	}
	run := func(src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	format := strings.Repeat("%[1]f", 800)
	runtime.GC()
	start := time.Now()
	_, err := run(`{{ printf "`+format+`" .data.n }}`, map[string]any{"n": nums})
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapAlloc > 100<<20 {
		t.Fatalf("heap %d MiB", ms.HeapAlloc>>20)
	}
	out, err := run(`{{ printf "%v" .data.l }}|{{ printf "%.2f" .data.f }}`, map[string]any{"l": []any{1.5, 2.0, 3.0}, "f": 3.14159})
	if err != nil || out != "[1.5 2 3]|3.14" {
		t.Fatalf("small number lists must pass: %q %v", out, err)
	}
	// the real worst formatting stays under the margin
	if got := len(fmt.Sprintf("%f", -1e308)); got > numericMargin {
		t.Fatalf("%%f of 1e308 is %d characters, above the margin %d", got, numericMargin)
	}
}

// Pass 23: html/template needs a predefined escaper last in the pipeline.
func TestPredefinedEscapersStayLast(t *testing.T) {
	run := func(engine, src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), engine, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	data := map[string]any{"x": "<b>"}
	for src, want := range map[string]string{
		`{{ .data.x | html }}`:             "&lt;b&gt;",
		`{{ printf "%s" .data.x | html }}`: "&lt;b&gt;",
		`{{ html .data.x }}`:               "&lt;b&gt;",
		`{{ .data.x | urlquery }}`:         "%3Cb%3E",
	} {
		if out, err := run(EngineHTML, src, data); err != nil || out != want {
			t.Errorf("html %s: want %q, got %q %v", src, want, out, err)
		}
	}
	for _, engine := range []string{EngineHTML, EngineText, EngineTypst} {
		for _, src := range []string{`{{ .data.absent | html }}`, `{{ .data.absent | urlquery }}`, `{{ .data.absent | js }}`, `{{ html .data.absent }}`} {
			if out, err := run(engine, src, data); err == nil {
				t.Errorf("%s %s must fail, got %q", engine, src, out)
			}
		}
		if _, err := run(engine, `{{ .data.x | html }}`, data); err != nil {
			t.Errorf("%s: html escaper must work: %v", engine, err)
		}
	}
}

// Pass 24: the same input always gives the same error code and message.
func TestErrorsAreDeterministic(t *testing.T) {
	padding := make([]any, 10001)
	for i := range padding {
		padding[i] = map[string]any{"type": "string"}
	}
	schema, _ := json.Marshal(map[string]any{
		"type": "object", "x-padding": padding,
		"$defs":      map[string]any{"loop": map[string]any{"allOf": []any{map[string]any{"$ref": "#/$defs/loop"}}}},
		"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/loop"}},
	})
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		var out any
		err := validateInputSchema(string(schema), &out)
		e, ok := err.(*Error)
		if !ok || e.Code != CodeInputSchemaInvalid {
			t.Fatalf("call %d: want input_schema_invalid, got %v", i, err)
		}
		seen[e.Message] = true
	}
	if len(seen) != 1 {
		t.Fatalf("%d distinct messages: %v", len(seen), seen)
	}
	// reference errors win over the cost limit, whatever the key order
	ctx := map[string]any{"data": 1, "company": map[string]any{}, "brand": map[string]any{"colors": map[string]any{"primary": "x"}}, "doc": map[string]any{}, "i18n": map[string]any{}}
	msgs := map[string]bool{}
	for i := 0; i < 50; i++ {
		msgs[ValidateContext(ctx).Error()] = true
	}
	if len(msgs) != 1 {
		t.Fatalf("context validation gave %d distinct messages", len(msgs))
	}
	for i := 0; i < 50; i++ {
		if err := checkRefs(map[string]any{"b": map[string]any{"$ref": "https://x/y"}, "a": map[string]any{"$ref": "#/zz"}}); err == nil || !strings.Contains(err.Error(), "does not resolve") {
			t.Fatalf("sorted order must report #/zz first: %v", err)
		}
	}
}

// Pass 26: in a define, $ is the argument of the call and may be nil.
func TestDollarInDefineIsGuarded(t *testing.T) {
	run := func(engine, src string) (string, error) {
		ex, _ := newExecutor(context.Background(), engine, map[string]any{"data": map[string]any{"x": "v"}}, 0)
		return ex.render(src)
	}
	for _, engine := range []string{EngineText, EngineHTML, EngineTypst} {
		if out, err := run(engine, `{{ define "echo" }}{{ printf "%s" $ }}{{ end }}{{ template "echo" .data.absent }}`); err == nil {
			t.Errorf("%s: an absent $ in a define must fail, got %q", engine, out)
		}
	}
	if out, err := run(EngineText, `{{ define "e" }}{{ $ | default "d" }}{{ end }}{{ template "e" .data.absent }}`); err != nil || out != "d" {
		t.Errorf("default on $: want d, got %q %v", out, err)
	}
	if out, err := run(EngineText, `{{ printf "%s" $.data.x }}{{ range .data }}{{ printf "%s" $.data.x }}{{ end }}`); err != nil || out != "vv" {
		t.Errorf("$.data.x in the entry must work: %q %v", out, err)
	}
}

// A define or block cannot take the name of the entry tree or of an internal function.
func TestReservedTemplateNames(t *testing.T) {
	for _, src := range []string{
		`{{ define "t" }}x{{ end }}`,
		`{{- block "t" . }}x{{ end }}`,
		`{{ define "__tick" }}x{{ end }}`,
		`{{ define "__present" }}x{{ end }}`,
		`{{ define "escTypst" }}x{{ end }}`,
		"{{ define `t` }}x{{ end }}",
		"{{- define \"t\" -}}x{{- end -}}",
		"{{define\n\"t\"}}x{{end}}",
		"{{ define \"__entry_abc\" }}x{{ end }}",
		"{{ define \"now\" }}x{{ end }}",
	} {
		if err := checkFunctions(src); err == nil || err.(*Error).Code != CodeForbiddenFunction {
			t.Errorf("%q must be refused: %v", src, err)
		}
	}
	for _, src := range []string{`{{ define "echo" }}x{{ end }}{{ template "echo" . }}`, `{{ define "t2" }}x{{ end }}`,
		`{{/* define "t" */}}x`, `text mentioning define "t" and {{ "{{ define \"t\" }}" }}`} {
		if err := checkFunctions(src); err != nil {
			t.Errorf("%q must pass: %v", src, err)
		}
	}
}

// A reserved define in a secondary .typ file is refused too.
func TestReservedDefineInSecondaryFile(t *testing.T) {
	tpl := Template{Manifest: json.RawMessage(`{"schema":"agentmaurice.template/v1","key":"secondary","version":"1.0.0","kind":"document","profile":"document",
	"source":{"engine":"typst","entry":"main.typ"},"outputs":["pdf"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`),
		Files: map[string]string{"main.typ": "x", "parts/b.typ": "{{- define \"t\" -}}y{{- end -}}", "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
	if _, err := Validate(tpl, map[string]any{}, "pdf", 0); err == nil || err.(*Error).Code != CodeForbiddenFunction {
		t.Fatalf("want forbidden_function, got %v", err)
	}
}

func deepObject(n int) map[string]any {
	root := map[string]any{}
	cur := root
	for i := 0; i < n; i++ {
		next := map[string]any{}
		cur["a"] = next
		cur = next
	}
	return root
}

// json is bounded before it allocates, and deep data is refused at validation.
func TestJSONFunctionIsBounded(t *testing.T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	// 9000 nested objects: refused by the depth limit
	tpl := Template{Manifest: json.RawMessage(`{"schema":"agentmaurice.template/v1","key":"deep-json","version":"1.0.0","kind":"page","profile":"web",
	"source":{"engine":"text","entry":"main.txt"},"outputs":["txt"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`),
		Files: map[string]string{"main.txt": "{{ json .data }}", "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
	start := time.Now()
	_, err := Validate(tpl, deepObject(9000), "txt", 0)
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > 100<<20 {
		t.Fatalf("heap %d MiB", after.HeapAlloc>>20)
	}
	// a depth within the limit still renders
	ok := deepObject(200)
	ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": ok}, 0)
	if _, err := ex.render(`{{ json .data }}`); err != nil {
		t.Fatalf("depth 200 must render: %v", err)
	}
	// json over a big value: refused before marshaling, also in a range (measured once)
	items := make([]any, 40000)
	for i := range items {
		items[i] = map[string]any{"k": strings.Repeat("a", 100), "n": []any{1.0, 2.0, 3.0}}
	}
	ex, _ = newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{"big": items, "r": make([]any, 10000)}}, 1<<20)
	start = time.Now()
	_, err = ex.render(`{{ range .data.r }}{{ $x := json $.data.big }}{{ end }}`)
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > 100<<20 {
		t.Fatalf("heap %d MiB", after.HeapAlloc>>20)
	}
	// a small json still works
	ex, _ = newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{"a": "b"}}, 0)
	if out, err := ex.render(`{{ json .data }}`); err != nil || out != "{\n  \"a\": \"b\"\n}" {
		t.Fatalf("small json: %q %v", out, err)
	}
}

// printf never prints an address, and map formatting is deterministic.
func TestPrintfRefusesAddressesAndIsDeterministic(t *testing.T) {
	m := map[string]any{}
	for i := 0; i < 50; i++ {
		m[fmt.Sprintf("k%02d", 49-i)] = float64(i)
	}
	run := func(src string) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{"m": m, "l": []any{"a", 1.5}, "s": "x"}}, 0)
		return ex.render(src)
	}
	for _, src := range []string{`{{ printf "%p" .data }}`, `{{ printf "%p" .data.m }}`, `{{ printf "%10p|%v" .data.m 1 }}`} {
		if out, err := run(src); err == nil {
			t.Errorf("%s must be refused, got %q", src, out)
		} else if e, ok := err.(*Error); !ok || e.Code != CodeForbiddenFunction {
			t.Errorf("%s: want forbidden_function, got %v", src, err)
		}
	}
	x := 1
	if _, err := printfBound("%v", []any{&x}, 1<<20); err == nil {
		t.Error("a pointer argument must be refused")
	}
	ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
	print := ex.funcs()["print"].(func(...any) (string, error))
	if _, err := print(&x); err == nil {
		t.Error("print of a pointer must be refused")
	}
	if _, err := print(func() {}); err == nil {
		t.Error("print of a function must be refused")
	}
	first, err := run(`{{ printf "%v|%T|%T|%T" .data.m .data.l .data.s .data.m }}{{ print .data.m }}{{ println .data.l }}`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if out, _ := run(`{{ printf "%v|%T|%T|%T" .data.m .data.l .data.s .data.m }}{{ print .data.m }}{{ println .data.l }}`); out != first {
			t.Fatalf("output changed between calls")
		}
	}
	if !strings.Contains(first, "|[]interface {}|string|map[string]interface {}") || !strings.HasPrefix(first, "map[k00:49 k01:48 k02:47") {
		t.Fatalf("unexpected %%v or %%T output: %.120s", first)
	}
}

// A literal nil cannot bypass __present.
func TestNilLiteralIsForbiddenExceptForDefault(t *testing.T) {
	for _, src := range []string{`{{ printf "%s" nil }}`, `{{ print nil }}`, `{{ printf "%s" (print nil) }}`, `{{ eq .x nil }}`, `{{ if eq .x nil }}y{{ end }}`} {
		if err := checkFunctions(src); err == nil || err.(*Error).Code != CodeForbiddenFunction {
			t.Errorf("%s must be refused: %v", src, err)
		}
	}
	if err := checkFunctions(`{{ default "x" nil }}`); err != nil {
		t.Errorf("default \"x\" nil must pass: %v", err)
	}
	ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{}}, 0)
	if out, err := ex.render(`{{ default "x" nil }}`); err != nil || out != "x" {
		t.Errorf("default \"x\" nil: want x, got %q %v", out, err)
	}
}

// $ cannot be reassigned or redeclared; other variables can.
func TestDollarCannotBeReassigned(t *testing.T) {
	for _, src := range []string{
		`{{ $ = .data.absent }}{{ printf "%s" $ }}`,
		`{{ $ := .data.absent }}{{ printf "%s" $ }}`,
		`{{ range $ := .data.items }}x{{ end }}`,
		`{{ if $ = .data.absent }}x{{ end }}`,
	} {
		if err := checkFunctions(src); err == nil || err.(*Error).Code != CodeForbiddenFunction {
			t.Errorf("%s must be refused: %v", src, err)
		}
	}
	for _, src := range []string{`{{ $x := 1 }}{{ $x = 2 }}{{ $x }}`, `{{ range $i, $v := .data.items }}{{ $i }}{{ end }}`, `{{ printf "%s" $.data.x }}`} {
		if err := checkFunctions(src); err != nil {
			t.Errorf("%s must pass: %v", src, err)
		}
	}
}

// Pass printf-3: expansion factor, hard width cap, map keys as leaves.
func TestPrintfBoundHardCapExpansionAndKeys(t *testing.T) {
	run := func(src string, data map[string]any) (string, error) {
		ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": data}, 0)
		return ex.render(src)
	}
	refused := func(name, src string, data map[string]any) {
		runtime.GC()
		start := time.Now()
		_, err := run(src, data)
		if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
			t.Errorf("%s: want limits_exceeded, got %v", name, err)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s: took %s", name, time.Since(start))
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		if ms.HeapAlloc > 100<<20 {
			t.Errorf("%s: heap %d MiB", name, ms.HeapAlloc>>20)
		}
	}
	runes := make([]any, 100000)
	for i := range runes {
		runes[i] = "\u0080"
	}
	refused("%q of U+0080 list", `{{ printf "`+strings.Repeat("%[1]q", 100)+`" .data.l }}`, map[string]any{"l": runes})
	keys := map[string]any{}
	for i := 0; i < 15; i++ {
		keys[fmt.Sprintf("key%02d", i)] = "v"
	}
	refused("width over a map", `{{ printf "%1000000v" .data.m }}`, map[string]any{"m": keys})
	many := map[string]any{}
	for i := 0; i < 20000; i++ {
		many[fmt.Sprintf("k%d", i)] = "v"
	}
	refused("keys counted as leaves", `{{ printf "%1000v" .data.m }}`, map[string]any{"m": many})
	refused("width 1001", `{{ printf "%1001s" "a" }}`, map[string]any{})
	refused("star width 1001", `{{ printf "%*s" 1001 "a" }}`, map[string]any{})
	refused("precision 1001", `{{ printf "%.1001f" 1.5 }}`, map[string]any{})
	if out, err := run(`{{ printf "%1000s" "a" }}`, map[string]any{}); err != nil || len(out) != 1000 {
		t.Errorf("%%1000s must pass: %d %v", len(out), err)
	}
	if out, err := run(`{{ printf "%-5s|%q|%x|%5.1f" "ab" "c" "d" 2.25 }}`, map[string]any{}); err != nil || out != `ab   |"c"|64|  2.2` {
		t.Errorf("normal printf: %q %v", out, err)
	}
}

// The json bound cache is keyed by pointer, length and capacity: a sub-slice
// does not share the bound of its parent.
func TestJSONBoundCacheDistinguishesSubSlices(t *testing.T) {
	items := make([]any, 2000)
	for i := range items {
		items[i] = strings.Repeat("a", 50)
	}
	sub := items[:1]
	cache := map[identityKey]int{}
	small := jsonBound(sub, 1<<30, cache)
	big := jsonBound(items, 1<<30, cache)
	if small >= big || len(cache) != 2 {
		t.Fatalf("a slice and its sub-slice must have different bounds: %d vs %d (cache %d)", small, big, len(cache))
	}
	// through the template: the first call fits the budget, the second does not
	ex, _ := newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{"a": items}}, 20000)
	out, err := ex.render(`{{ $a := .data.a }}{{ $x := json (slice $a 0 1) }}{{ $y := json $a }}`)
	if e, ok := err.(*Error); !ok || e.Code != CodeLimitsExceeded {
		t.Fatalf("the second call must be refused, got %q %v", out, err)
	}
	ex, _ = newExecutor(context.Background(), EngineText, map[string]any{"data": map[string]any{"a": items}}, 20000)
	if _, err := ex.render(`{{ $a := .data.a }}{{ $x := json (slice $a 0 1) }}`); err != nil {
		t.Fatalf("the first call must be accepted: %v", err)
	}
}

// The Typst context rules run on the text after the trim of {{- and -}} and
// ignore the actions that print nothing (ported from template_check.py, and
// stricter than it on a backslash separated from the action by empty actions).
func TestTypstContextAfterTrim(t *testing.T) {
	refused := map[string]string{
		"\\ {{- if true }}{{ end }}{{ .data.x }}\n":                        CodeTypstEscapedAction,
		"\\{{ if true -}} {{ end }}{{ .data.x }}\n":                        CodeTypstEscapedAction,
		"\\ {{- if true }}{{ end }}{{ if false }}{{ end }}{{ .data.x }}\n": CodeTypstEscapedAction,
		"\\{{ if true }}{{ end -}}\n {{ .data.x }}\n":                      CodeTypstEscapedAction,
		"\\{{ .data.x }}\n":                        CodeTypstEscapedAction,
		"{{ .data.x }}(a)\n":                       CodeTypstCodeContext,
		"{{ .data.x }}.foo\n":                      CodeTypstCodeContext,
		"{{ .data.x }}{{ if true }}[x]{{ end }}\n": CodeTypstCodeContext,
		"#{{ if true }}{{ .data.x }}{{ end }}\n":   CodeTypstCodeContext,
		"#text{{ .data.x }}\n":                     CodeTypstCodeContext,
	}
	for src, want := range refused {
		if err := typstCodeContext(src); err == nil || err.(*Error).Code != want {
			t.Errorf("%q: want %s, got %v", src, want, err)
		}
	}
	for _, src := range []string{
		"\\ {{ .data.x }}\n",
		"\\ {{ if true -}} {{ end }}{{ .data.x }}\n", // the space before the if stays
		"{{ .data.x }} (a)\n",
		"a\\\\{{ .data.x }}\n", // an even number of backslashes
		"[{{ .data.x }}]\n",
		"{{ if true }}[{{ .data.x }}]{{ end }}\n",
	} {
		if err := typstCodeContext(src); err != nil {
			t.Errorf("%q must be accepted: %v", src, err)
		}
	}
}

// A sub-template call prints: it follows the same Typst context rules as {{ .x }}.
func TestTemplateCallIsAnOutputAction(t *testing.T) {
	def := `{{ define "echo" }}{{ . }}{{ end }}` + "\n"
	for src, want := range map[string]string{
		def + "`{{ template \"echo\" .data.x }}`\n":            CodeTypstRawContext,
		def + "$ {{ template \"echo\" .data.x }} $\n":          CodeTypstMathContext,
		def + "// {{ template \"echo\" .data.x }}\n":           CodeTypstCommentContext,
		def + "#{{ template \"echo\" .data.x }}\n":             CodeTypstCodeContext,
		def + "#let a = \"{{ template \"echo\" .data.x }}\"\n": CodeTypstCodeContext,
		def + "\\{{ template \"echo\" .data.x }}\n":            CodeTypstEscapedAction,
		def + "{{ template \"echo\" .data.x }}(a)\n":           CodeTypstCodeContext,
		"`{{ block \"b\" .data.x }}y{{ end }}`\n":              CodeTypstRawContext,
	} {
		if err := typstCodeContext(src); err == nil || err.(*Error).Code != want {
			t.Errorf("%q: want %s, got %v", src, want, err)
		}
	}
	for _, src := range []string{
		def + "[{{ template \"echo\" .data.x }}]\n",
		def + "{{ template \"echo\" .data.x }}\n",
		def + "\\ {{ template \"echo\" .data.x }}\n",
	} {
		if err := typstCodeContext(src); err != nil {
			t.Errorf("%q must be accepted: %v", src, err)
		}
	}
}

// Line-start code rules and strings apply to markup at any depth, [..] blocks included.
func TestTypstCodeLinesInsideContentBlocks(t *testing.T) {
	for _, src := range []string{
		"#block[\n#let x = \"{{ .data.x }}\"\n#x\n]\n",
		"#block[\n#block[\n#let x = \"{{ .data.x }}\"\n]\n]\n",
		"#block[\n#set text(font: \"{{ .data.x }}\")\n]\n",
		"#f(\n[\n#let a = {{ .data.x }}\n]\n)\n",
	} {
		if err := typstCodeContext(src); err == nil || err.(*Error).Code != CodeTypstCodeContext {
			t.Errorf("%q must be refused: %v", src, err)
		}
	}
	for _, src := range []string{
		"#block[{{ .data.x }}]\n",
		"#block[\n= Titre {{ .data.x }}\n]\n",
		"#block[\n#let x = [{{ .data.x }}]\n#x\n]\n",
		"#block[\n#let x = 1\ntext {{ .data.x }}\n]\n", // the code line ended at the newline
	} {
		if err := typstCodeContext(src); err != nil {
			t.Errorf("%q must be accepted: %v", src, err)
		}
	}
}
