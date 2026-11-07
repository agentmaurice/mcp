package logging

import (
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func observed(level zapcore.Level) (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(level)
	return zap.New(core, zap.AddCaller()), logs
}

func TestSizeFieldsCarryNoContent(t *testing.T) {
	sql := "SELECT * FROM v_customers WHERE name = 'Alma'"
	l, logs := observed(zapcore.InfoLevel)
	l.Info("x", Size("sql", sql), SizeOf("params", map[string]any{"n": "Alma"}))
	e := logs.All()[0]
	if e.ContextMap()["sql_bytes"] != int64(len(sql)) {
		t.Fatalf("sql_bytes = %v", e.ContextMap()["sql_bytes"])
	}
	if e.ContextMap()["params_bytes"] != int64(len(`{"n":"Alma"}`)) {
		t.Fatalf("params_bytes = %v", e.ContextMap()["params_bytes"])
	}
	for k, v := range e.ContextMap() {
		if s, ok := v.(string); ok && strings.Contains(s, "Alma") {
			t.Fatalf("field %s carries content: %q", k, s)
		}
	}
}

func TestDebugWritesMaskedPreviewOnlyAtDebug(t *testing.T) {
	sql := "SELECT 1 -- password=hunter2 contact bob@example.com"
	info, infoLogs := observed(zapcore.InfoLevel)
	Debug(info, "sql", "sql", sql)
	if infoLogs.Len() != 0 {
		t.Fatalf("an Info logger kept a Debug payload: %v", infoLogs.All())
	}
	dbg, dbgLogs := observed(zapcore.DebugLevel)
	Debug(dbg, "sql", "sql", sql, zap.String("tool", "memory.query"))
	if dbgLogs.Len() != 1 {
		t.Fatalf("want one Debug entry, got %d", dbgLogs.Len())
	}
	ctx := dbgLogs.All()[0].ContextMap()
	got := ctx["sql"].(string)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "bob@example.com") {
		t.Fatalf("preview not masked: %q", got)
	}
	if ctx["sql_bytes"] != int64(len(sql)) || ctx["tool"] != "memory.query" {
		t.Fatalf("fields = %v", ctx)
	}
	if !strings.HasSuffix(dbgLogs.All()[0].Caller.File, "payload_test.go") {
		t.Fatalf("entry attributed to %s, want the caller", dbgLogs.All()[0].Caller.File)
	}
}

func TestDebugValueMasksSensitiveKeysAndTruncates(t *testing.T) {
	l, logs := observed(zapcore.DebugLevel)
	DebugValue(l, "args", "arguments", map[string]any{"api_key": "sk_live_" + "abcdefgh12345", "note": strings.Repeat("é", 600)})
	got := logs.All()[0].ContextMap()["arguments"].(string)
	if strings.Contains(got, "sk_live") {
		t.Fatalf("secret kept: %q", got)
	}
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("preview = %q", got)
	}
}

func TestErrorClassKeepsNoValue(t *testing.T) {
	cases := map[string]string{
		"Conversion Error: Could not convert string 'MARKER_CONFIDENTIEL' to INT32": "Conversion Error",
		"Parser Error: syntax error near 'x'":                                       "Parser Error",
		"plain failure without class":                                               "error",
		"quoted 'value': not a class":                                               "error",
	}
	for text, want := range cases {
		f := ErrorClass(errors.New(text))
		if f.Key != "error_class" || f.String != want {
			t.Errorf("ErrorClass(%q) = %q, want %q", text, f.String, want)
		}
		if strings.Contains(f.String, "MARKER_CONFIDENTIEL") {
			t.Errorf("class carries the value: %q", f.String)
		}
	}
	if ErrorClass(nil).Type != zapcore.SkipType {
		t.Fatal("a nil error must add no field")
	}
}

func TestDebugErrorAndTextMaskAndCut(t *testing.T) {
	l, logs := observed(zapcore.DebugLevel)
	DebugError(l, "failed", errors.New("Conversion Error: bad value password=hunter2"))
	if got := logs.All()[0].ContextMap()["error"].(string); strings.Contains(got, "hunter2") {
		t.Fatalf("error text not masked: %q", got)
	}
	info, infoLogs := observed(zapcore.InfoLevel)
	DebugError(info, "failed", errors.New("x"))
	if infoLogs.Len() != 0 {
		t.Fatal("an Info logger kept a Debug error text")
	}
	got := Text("dedupe_key", "password=hunter2 bob@example.com "+strings.Repeat("z", 2000)).String
	if strings.Contains(got, "hunter2") || strings.Contains(got, "bob@") || len(got) > PreviewLimit+64 {
		t.Fatalf("Text = %d bytes %q", len(got), got[:80])
	}
}

func TestPreviewMasksCredentialKeyValues(t *testing.T) {
	got := Preview("SELECT 1 -- credential=MON_SECRET private_key=MA_CLE cookie=MA_SESSION session_id=abc access_key=K1")
	for _, secret := range []string{"MON_SECRET", "MA_CLE", "MA_SESSION", "abc", "K1"} {
		if strings.Contains(got, secret) {
			t.Fatalf("%s kept in %q", secret, got)
		}
	}
}

func TestPreviewMasksQuotedSecretsWithEscapedQuotes(t *testing.T) {
	for _, in := range []string{
		"SELECT 1 WHERE password='abc''SECRET_RESTANT' AND x = 1",
		`{"sql": "password=\"abc\\\"SECRET_RESTANT\" rest"}`,
	} {
		if got := Preview(in); strings.Contains(got, "SECRET_RESTANT") {
			t.Errorf("Preview(%q) = %q keeps the tail of the secret", in, got)
		}
	}
}
