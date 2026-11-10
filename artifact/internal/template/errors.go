package template

import "fmt"

// Stable error codes. The first group mirrors the codes of the template/v1
// contract (`maurice template check`); the second is specific to rendering.
const (
	CodeManifestSchema      = "manifest_schema"
	CodeEntryMissing        = "entry_missing"
	CodeInputSchemaMissing  = "input_schema_missing"
	CodeInputSchemaInvalid  = "input_schema_invalid"
	CodeReservedNamespace   = "reserved_namespace"
	CodeForbiddenFunction   = "forbidden_function"
	CodeUnknownFunction     = "unknown_function"
	CodeTemplateSyntax      = "template_syntax"
	CodeTemplateExec        = "template_exec"
	CodeFragmentUndeclared  = "fragment_undeclared"
	CodeFragmentMissing     = "fragment_missing"
	CodeLimitsExceeded      = "limits_exceeded"
	CodeTypstCodeContext    = "typst_code_context"
	CodeTypstEscapedAction  = "typst_escaped_action"
	CodeTypstRawContext     = "typst_raw_context"
	CodeTypstMathContext    = "typst_math_context"
	CodeTypstCommentContext = "typst_comment_context"
	CodeContextInvalid      = "context_invalid"
	CodeDataInvalid         = "data_invalid"
	CodeFilePathInvalid     = "file_path_invalid"
	CodeEngineUnsupported   = "engine_unsupported"
	CodeOutputUnsupported   = "output_unsupported"
	CodePackageImport       = "package_import_forbidden"
	CodeCompileFailed       = "compile_failed"
	CodeRenderTimeout       = "render_timeout"
	CodeEngineUnavailable   = "engine_unavailable"
	CodeEngineVersion       = "engine_version_mismatch"
	CodeAssetInvalid        = "asset_invalid"
	CodeReservedFileName    = "reserved_file_name"
	CodeFileExtension       = "file_extension_forbidden"
	CodeInvalidModeOrOutput = "invalid_arguments"

	// Message profile (mjml engine; html, txt and eml outputs).
	CodeMJMLFailed             = "mjml_failed"
	CodeMJMLIncludeOutside     = "mjml_include_outside_fragments"
	CodeMJMLIncludeDenied      = "mjml_include_denied"
	CodeMessageOutputInvalid   = "message_output_invalid"
	CodeMessageTemplateResidue = "message_template_residue"
	CodeMessageTooHeavy        = "message_html_too_heavy"
	CodeMessageURLNotHTTPS     = "message_url_not_https"
	CodeMessageUnsubscribeMiss = "message_unsubscribe_missing"
	CodeMessageHiddenRule      = "message_hidden_rule_unsupported"
	CodeMessageReservedChar    = "message_reserved_character"
)

// Error is a render or validation failure with a stable code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
