package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func TestParseResponseFormat(t *testing.T) {
	cases := map[string]string{"": "detailed", "detailed": "detailed", "CONCISE": "concise", " concise ": "concise"}
	for raw, want := range cases {
		got, err := parseResponseFormat(raw)
		if err != nil || got != want {
			t.Fatalf("parseResponseFormat(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := parseResponseFormat("verbose"); err == nil {
		t.Fatal("unknown response_format must be rejected")
	}
}

func TestBuildSnippetCentresOnQueryTermAndIsBounded(t *testing.T) {
	content := strings.Repeat("contexte sans rapport. ", 200) + "Le ticket AGENT-530 fixe la grille tarifaire." + strings.Repeat(" suite du texte.", 200)
	snippet, _ := buildSnippet(content, "AGENT-530", conciseSnippetWidth)
	if !strings.Contains(snippet, "AGENT-530") {
		t.Fatalf("snippet does not contain the matched term: %q", snippet)
	}
	if n := utf8.RuneCountInString(snippet); n > conciseSnippetWidth+2 {
		t.Fatalf("snippet has %d runes, want at most %d", n, conciseSnippetWidth+2)
	}
	if !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") {
		t.Fatalf("cut snippet must be marked at both ends: %q", snippet)
	}
}

func TestBuildSnippetMatchesStemmedTermsAndShortContent(t *testing.T) {
	snippet, _ := buildSnippet("La tarification annuelle est revue.", "tarifs", conciseSnippetWidth)
	if !strings.Contains(snippet, "tarification") {
		t.Fatalf("stemmed term should anchor the snippet: %q", snippet)
	}
	short := "Court texte."
	if got, _ := buildSnippet(short, "absent", conciseSnippetWidth); got != short {
		t.Fatalf("short content without match should be returned whole, got %q", got)
	}
}

func TestConciseResponseIsBoundedAndCarriesNoFullContent(t *testing.T) {
	huge := strings.Repeat("mot de remplissage ", 400) + "AGENT-530"
	result := &shared.SearchResponse{Mode: "bm25", Query: "AGENT-530", TotalResults: 20}
	for i := 0; i < 20; i++ {
		result.Results = append(result.Results, shared.SearchResult{
			Rank:      i + 1,
			Score:     1.5,
			MatchType: "bm25",
			ChunkID:   "chunk-" + string(rune('a'+i)),
			Document:  shared.Document{ID: "doc", FilePath: "docs/large.md", Title: "Large"},
			Chunk:     shared.Chunk{ID: "chunk-x", Content: huge, StartLine: 1, EndLine: 40},
		})
	}
	payload, err := json.Marshal(buildConciseSearchResponse(result))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), huge) {
		t.Fatal("concise response must not contain the full chunk content")
	}
	var decoded struct {
		Results []struct {
			URI     string  `json:"uri"`
			Title   string  `json:"title"`
			ChunkID string  `json:"chunk_id"`
			Score   float64 `json:"score"`
			Snippet string  `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Results) != 20 {
		t.Fatalf("expected 20 results, got %d", len(decoded.Results))
	}
	for _, r := range decoded.Results {
		if r.URI != "docs/large.md" || r.Title != "Large" || r.ChunkID == "" || r.Score != 1.5 {
			t.Fatalf("missing URI/title/chunk id/score: %+v", r)
		}
		if n := utf8.RuneCountInString(r.Snippet); n > conciseSnippetWidth+2 {
			t.Fatalf("snippet has %d runes, want at most %d", n, conciseSnippetWidth+2)
		}
	}
	if len(payload) > 20*(conciseSnippetWidth+400) {
		t.Fatalf("concise payload is %d bytes, not bounded", len(payload))
	}
}

func TestKeywordToolRejectsUnknownResponseFormatBeforeSearching(t *testing.T) {
	// No orchestrator: the format is validated before any search runs.
	tool := NewKeywordTool(nil, nil, NewResponseWrapper(zap.NewNop()), zap.NewNop())
	res, err := tool.Handler()(context.Background(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name:      tool.Name(),
		Arguments: map[string]any{"query": "AGENT-530", "response_format": "xml"},
	}})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatal("unknown response_format must return an error result")
	}
}

func TestResponseFormatIsDocumentedInToolSchemas(t *testing.T) {
	tools := []Tool{
		NewKeywordTool(nil, nil, nil, zap.NewNop()),
		NewHybridTool(nil, nil, nil, zap.NewNop()),
		NewSearchTool(nil, nil, nil, zap.NewNop()),
	}
	for _, tool := range tools {
		def := tool.Definition()
		prop, ok := def.InputSchema.Properties["response_format"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s: response_format missing from input schema", tool.Name())
		}
		if prop["default"] != responseFormatDetailed {
			t.Fatalf("%s: default = %v, want detailed", tool.Name(), prop["default"])
		}
		if !strings.Contains(def.Description, "concise") && !strings.Contains(prop["description"].(string), "concise") {
			t.Fatalf("%s: concise format not described", tool.Name())
		}
	}
}

// Review case: "securite" must bring the accented "sécurité" into the snippet, even
// when the term comes after 1 000 characters of other text.
func TestBuildSnippetFindsAccentedTermByFoldedQuery(t *testing.T) {
	content := strings.Repeat("x", 1000) + " La sécurité des données est revue chaque trimestre."
	snippet, _ := buildSnippet(content, "securite", conciseSnippetWidth)
	if !strings.Contains(snippet, "sécurité") {
		t.Fatalf("snippet must contain the matched term sécurité: %q", snippet)
	}
	if n := utf8.RuneCountInString(snippet); n > conciseSnippetWidth+2 {
		t.Fatalf("snippet has %d runes, want at most %d", n, conciseSnippetWidth+2)
	}
}

// The window is the one that covers the most distinct query terms, not the one
// around the first term found.
func TestBuildSnippetCoversMostDistinctTerms(t *testing.T) {
	filler := func(n int) string { return strings.Repeat("remplissage ", n/12) }
	content := filler(600) + "alpha " + filler(1000) + "beta gamma " + filler(600)
	snippet, _ := buildSnippet(content, "alpha beta gamma", conciseSnippetWidth)
	if !strings.Contains(snippet, "beta") || !strings.Contains(snippet, "gamma") {
		t.Fatalf("snippet should cover beta and gamma (2 terms) rather than alpha alone: %q", snippet)
	}
}

// A term changed by the stemmer (cats -> cat) still anchors the snippet.
func TestBuildSnippetFindsStemmedShorterTerm(t *testing.T) {
	content := strings.Repeat("abcdef ", 150) + " la tarification annuelle. " + strings.Repeat("ghijkl ", 150)
	snippet, _ := buildSnippet(content, "tarifs", conciseSnippetWidth)
	if !strings.Contains(snippet, "tarification annuelle") {
		t.Fatalf("snippet should centre on the stem tarif of tarifs: %q", snippet)
	}
}

// One-letter query terms anchor the snippet when nothing longer is in the query.
func TestBuildSnippetOneLetterQueryAnchors(t *testing.T) {
	content := strings.Repeat("abcdef ", 150) + " x " + strings.Repeat("ghijkl ", 150)
	snippet, _ := buildSnippet(content, "x", conciseSnippetWidth)
	if !strings.Contains(snippet, " x ") {
		t.Fatalf("snippet should contain the one-letter term: %q", snippet)
	}
}

// Repeated terms cost linear time: 100 000 occurrences must not take seconds.
func TestBuildSnippetLinearOnRepeatedTerms(t *testing.T) {
	content := strings.Repeat("a ", 100000)
	start := time.Now()
	snippet, _ := buildSnippet(content, "a", conciseSnippetWidth)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("snippet took %v on 100 000 occurrences", elapsed)
	}
	if n := utf8.RuneCountInString(snippet); n > conciseSnippetWidth+2 {
		t.Fatalf("snippet has %d runes", n)
	}
}

// The query says "running", the text has "runner" after 1 000 characters of filler: the
// common prefix "runn" (four runes, both words long enough) anchors the snippet.
func TestBuildSnippetMatchesStemOfQueryTerm(t *testing.T) {
	content := strings.Repeat("remplissage ", 84) + "le runner ici " + strings.Repeat("remplissage ", 84)
	snippet, fallback := buildSnippet(content, "running", conciseSnippetWidth)
	if fallback {
		t.Fatal("a stem match must not be reported as a fallback")
	}
	if !strings.Contains(snippet, "runner") {
		t.Fatalf("snippet must show the stem runn of running: %q", snippet)
	}
}

// When no term is found, the snippet starts at the beginning and the fallback is reported.
func TestBuildSnippetReportsFallbackWhenNothingMatches(t *testing.T) {
	content := strings.Repeat("remplissage ", 120)
	snippet, fallback := buildSnippet(content, "absent", conciseSnippetWidth)
	if !fallback {
		t.Fatal("expected the fallback to be reported")
	}
	if !strings.HasPrefix(snippet, "remplissage") {
		t.Fatalf("fallback snippet must start at the beginning: %q", snippet)
	}
}

// The concise envelope carries snippet_fallback when one result had no matching term.
func TestConciseResponseSignalsSnippetFallback(t *testing.T) {
	result := &shared.SearchResponse{Mode: "bm25", Query: "running", Results: []shared.SearchResult{
		{Rank: 1, Chunk: shared.Chunk{ID: "a", Content: strings.Repeat("remplissage ", 120) + "runner"}},
		{Rank: 2, Chunk: shared.Chunk{ID: "b", Content: strings.Repeat("remplissage ", 120)}},
	}}
	envelope := buildConciseSearchResponse(result)
	if !envelope.SearchMetadata.SnippetFallback {
		t.Fatal("search_metadata.snippet_fallback must be true when one snippet fell back")
	}
	result.Results = result.Results[:1]
	if buildConciseSearchResponse(result).SearchMetadata.SnippetFallback {
		t.Fatal("snippet_fallback must be false when every snippet matched a term")
	}
}

// Review case: "AGENT-531" must not stand for the query "AGENT-530" in the snippet.
// The identifier is matched as a whole word, so the snippet shows the real AGENT-530.
func TestBuildSnippetIdentifierIsNotMatchedByPrefix(t *testing.T) {
	content := "AGENT-531 " + strings.Repeat("remplissage ", 84) + " AGENT-530 " + strings.Repeat("remplissage ", 84)
	snippet, _ := buildSnippet(content, "AGENT-530", conciseSnippetWidth)
	if !strings.Contains(snippet, "AGENT-530") {
		t.Fatalf("snippet must show AGENT-530, not AGENT-531: %q", snippet)
	}
}

// Review case: the word "s" must not stand for "sécurité". Below four runes, only a whole
// word matches, so the snippet shows the real term and no fallback is reported.
func TestBuildSnippetShortWordDoesNotMatchLongTermByPrefix(t *testing.T) {
	content := "s " + strings.Repeat("remplissage ", 84) + " La sécurité des données. " + strings.Repeat("remplissage ", 84)
	snippet, fallback := buildSnippet(content, "sécurité", conciseSnippetWidth)
	if fallback {
		t.Fatal("the real term is in the text: no fallback expected")
	}
	if !strings.Contains(snippet, "sécurité") {
		t.Fatalf("snippet must show sécurité, not the letter s: %q", snippet)
	}
}

// Review case: "contrainte" is a false prefix match of "contrats". The window holding the
// real word must win over the windows holding the forty prefix matches.
func TestBuildSnippetWholeWordBeatsPrefixMatches(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("contrainte ")
	}
	b.WriteString(strings.Repeat("remplissage ", 100))
	b.WriteString(" les contrats signés. ")
	b.WriteString(strings.Repeat("remplissage ", 100))
	snippet, _ := buildSnippet(b.String(), "contrats", conciseSnippetWidth)
	if !strings.Contains(snippet, "contrats") {
		t.Fatalf("snippet must show the whole word contrats, not the prefix matches of contrainte: %q", snippet)
	}
}
