package chunker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
)

// chunkGoAST parses Go source code and produces AST-aware chunks.
func chunkGoAST(content []byte, filePath string) []shared.ChunkInput {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return nil // signal to caller to use fallback
	}

	lines := strings.Split(string(content), "\n")
	var chunks []shared.ChunkInput

	// Extract top-level declarations
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			chunk := extractFuncChunk(fset, d, lines)
			if chunk != nil {
				chunks = append(chunks, *chunk)
			}

		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						chunk := extractTypeChunk(fset, d, ts, lines)
						if chunk != nil {
							chunks = append(chunks, *chunk)
						}
					}
				}
			case token.VAR, token.CONST:
				chunk := extractGenDeclChunk(fset, d, lines)
				if chunk != nil {
					chunks = append(chunks, *chunk)
				}
			}
		}
	}

	// If we got no chunks, treat the whole file as one chunk
	if len(chunks) == 0 && len(content) > 0 {
		chunks = append(chunks, shared.ChunkInput{
			Content:   string(content),
			Type:      "file",
			StartLine: 1,
			EndLine:   len(lines),
			Metadata: map[string]interface{}{
				"package": file.Name.Name,
			},
		})
	}

	return chunks
}

func extractFuncChunk(fset *token.FileSet, fn *ast.FuncDecl, lines []string) *shared.ChunkInput {
	startPos := fset.Position(fn.Pos())
	endPos := fset.Position(fn.End())

	startLine := startPos.Line
	endLine := endPos.Line

	// Include doc comments if present
	if fn.Doc != nil {
		docStart := fset.Position(fn.Doc.Pos())
		startLine = docStart.Line
	}

	content := extractLines(lines, startLine, endLine)

	chunkType := "function"
	symbolName := fn.Name.Name

	// Detect methods
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		chunkType = "method"
		recvType := exprToString(fn.Recv.List[0].Type)
		symbolName = recvType + "." + fn.Name.Name
	}

	meta := map[string]interface{}{
		"exported": fn.Name.IsExported(),
	}
	if fn.Type.Params != nil {
		meta["param_count"] = len(fn.Type.Params.List)
	}

	return &shared.ChunkInput{
		Content:    content,
		Type:       chunkType,
		SymbolName: symbolName,
		StartLine:  startLine,
		EndLine:    endLine,
		Metadata:   meta,
	}
}

func extractTypeChunk(fset *token.FileSet, genDecl *ast.GenDecl, ts *ast.TypeSpec, lines []string) *shared.ChunkInput {
	startPos := fset.Position(genDecl.Pos())
	endPos := fset.Position(genDecl.End())

	startLine := startPos.Line
	endLine := endPos.Line

	if genDecl.Doc != nil {
		docStart := fset.Position(genDecl.Doc.Pos())
		startLine = docStart.Line
	}

	content := extractLines(lines, startLine, endLine)

	chunkType := "class" // struct/interface as "class" for unified classification
	switch ts.Type.(type) {
	case *ast.InterfaceType:
		chunkType = "interface"
	case *ast.StructType:
		chunkType = "struct"
	}

	return &shared.ChunkInput{
		Content:    content,
		Type:       chunkType,
		SymbolName: ts.Name.Name,
		StartLine:  startLine,
		EndLine:    endLine,
		Metadata: map[string]interface{}{
			"exported":  ts.Name.IsExported(),
			"node_type": chunkType,
		},
	}
}

func extractGenDeclChunk(fset *token.FileSet, decl *ast.GenDecl, lines []string) *shared.ChunkInput {
	startPos := fset.Position(decl.Pos())
	endPos := fset.Position(decl.End())

	startLine := startPos.Line
	endLine := endPos.Line

	if decl.Doc != nil {
		docStart := fset.Position(decl.Doc.Pos())
		startLine = docStart.Line
	}

	content := extractLines(lines, startLine, endLine)
	if strings.TrimSpace(content) == "" {
		return nil
	}

	chunkType := "variable"
	if decl.Tok == token.CONST {
		chunkType = "constant"
	}

	// Try to get the first name
	var symbolName string
	if len(decl.Specs) > 0 {
		if vs, ok := decl.Specs[0].(*ast.ValueSpec); ok && len(vs.Names) > 0 {
			symbolName = vs.Names[0].Name
		}
	}

	return &shared.ChunkInput{
		Content:    content,
		Type:       chunkType,
		SymbolName: symbolName,
		StartLine:  startLine,
		EndLine:    endLine,
		Metadata:   map[string]interface{}{},
	}
}

func extractLines(lines []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], "\n")
}

func exprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return exprToString(t.X)
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	default:
		return "unknown"
	}
}
