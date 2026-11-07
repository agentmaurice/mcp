package logging

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Payload logging (AGENT-1093, same rule as AGENT-1081 in the host platform): above Debug, a
// request body, a SQL text, query parameters or a value is logged by its size, next to
// the tool, the tenant and other technical identifiers; its content is only written at
// Debug, truncated and with secrets and e-mail addresses masked.

// PreviewLimit is the number of bytes of a payload kept in a Debug log.
const PreviewLimit = 512

const redacted = "[REDACTED]"

// Size returns the Info-safe field for a payload: its length in bytes, under key + "_bytes".
func Size(key string, payload string) zap.Field {
	return zap.Int(key+"_bytes", len(payload))
}

// SizeOf is Size for a structured value: the length of its JSON encoding.
func SizeOf(key string, value any) zap.Field {
	return zap.Int(key+"_bytes", encodedLen(value))
}

func encodedLen(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return len(fmt.Sprintf("%v", value))
	}
	return len(encoded)
}

// Debug writes a truncated, masked preview of a string payload under key at Debug, with
// its size. It costs nothing when Debug is disabled.
func Debug(logger *zap.Logger, message, key, payload string, fields ...zap.Field) {
	if ce := check(logger, message); ce != nil {
		ce.Write(append(fields, zap.String(key, Preview(payload)), Size(key, payload))...)
	}
}

// DebugValue is Debug for a structured value, encoded as JSON first.
func DebugValue(logger *zap.Logger, message, key string, value any, fields ...zap.Field) {
	if ce := check(logger, message); ce != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			encoded = []byte(fmt.Sprintf("%v", value))
		}
		ce.Write(append(fields, zap.String(key, Preview(string(encoded))), zap.Int(key+"_bytes", len(encoded)))...)
	}
}

// check returns the Debug entry attributed to the code that called the helper, or nil
// when Debug is disabled. The logger is only cloned when Debug is on.
func check(logger *zap.Logger, message string) *zapcore.CheckedEntry {
	if logger == nil || !logger.Core().Enabled(zapcore.DebugLevel) {
		return nil
	}
	return logger.WithOptions(zap.AddCallerSkip(2)).Check(zapcore.DebugLevel, message)
}

// ErrorClass returns an Info-safe field for an error that can quote the statement or a value
// (a DuckDB conversion error quotes the offending value): only its class, the text before
// the first colon ("Conversion Error", "Parser Error", "Binder Error"), at most 64 bytes.
// The full text is available at Debug through DebugError.
func ErrorClass(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	class := "error"
	if i := strings.IndexByte(err.Error(), ':'); i > 0 && i <= 64 && !strings.ContainsAny(err.Error()[:i], "'\"\n") {
		class = err.Error()[:i]
	}
	return zap.String("error_class", class)
}

// DebugError writes the masked, cut text of an error at Debug.
func DebugError(logger *zap.Logger, message string, err error, fields ...zap.Field) {
	if err == nil {
		return
	}
	if ce := check(logger, message); ce != nil {
		ce.Write(append(fields, zap.String("error", Preview(err.Error())))...)
	}
}

// Text returns a field for a caller-supplied free-text identifier (a fact type, a view
// name, a dedupe key): masked and cut like any other content.
func Text(key, value string) zap.Field {
	return zap.String(key, Preview(value))
}

// Preview returns payload masked and truncated to PreviewLimit bytes. A JSON payload has
// the values of sensitive keys replaced; every string also has tokens, credentials in
// key=value form and e-mail addresses masked.
func Preview(payload string) string {
	var decoded any
	if json.Unmarshal([]byte(payload), &decoded) == nil {
		if encoded, err := json.Marshal(redactValue(decoded)); err == nil {
			return truncate(string(encoded), len(payload))
		}
	}
	return truncate(maskText(payload), len(payload))
}

var sensitiveKeyParts = []string{
	"secret", "password", "passwd", "apikey", "api_key", "api-key", "token",
	"credential", "private_key", "access_key", "authorization", "cookie", "auth",
}

// IsSensitiveKey reports whether a field name suggests a secret value.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func redactValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if IsSensitiveKey(key) {
				out[key] = redacted
				continue
			}
			out[key] = redactValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactValue(item)
		}
		return out
	case string:
		return maskText(v)
	default:
		return v
	}
}

var (
	tokenPattern      = regexp.MustCompile(`\b(?:sk|pk|rk|mcp_sk|sk_maurice|sk_maurice_app)_[A-Za-z0-9_\-]{8,}|\bgh[pousr]_[A-Za-z0-9]{16,}|\bglpat-[A-Za-z0-9_\-]{16,}|\bxox[abprs]-[A-Za-z0-9\-]{10,}|\bAKIA[0-9A-Z]{16}\b|\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`)
	bearerPattern     = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]+`)
	keyValuePattern   = regexp.MustCompile(`(?i)\b((?:[a-z0-9_\-]*)(?:password|passwd|secret|token|api[_\-]?key|authorization|credential|private[_\-]?key|access[_\-]?key|cookie|session[_\-]?id)[a-z0-9_\-]*)(\s*[=:]\s*)("(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|[^\s,;&"']+)`)
	emailPattern      = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	maskedEmail       = "[REDACTED-EMAIL]"
	bearerReplacement = "$1 " + redacted
)

func maskText(text string) string {
	text = tokenPattern.ReplaceAllString(text, redacted)
	text = bearerPattern.ReplaceAllString(text, bearerReplacement)
	text = keyValuePattern.ReplaceAllString(text, "${1}${2}"+redacted)
	return emailPattern.ReplaceAllString(text, maskedEmail)
}

func truncate(text string, originalBytes int) string {
	if len(text) <= PreviewLimit {
		return text
	}
	cut := PreviewLimit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s…[truncated, %d bytes]", text[:cut], originalBytes)
}
