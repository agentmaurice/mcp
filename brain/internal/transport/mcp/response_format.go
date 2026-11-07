package mcp

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/search"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
)

const (
	responseFormatConcise  = "concise"
	responseFormatDetailed = "detailed"

	// conciseSnippetWidth bounds the text returned per result in concise mode.
	conciseSnippetWidth = 300

	responseFormatDescription = "'detailed' (default) returns full chunk content; 'concise' returns per result the document URI and title, chunk id, score, line range and a snippet of about 300 characters around the query terms."
)

// responseFormatProperty is the input schema entry shared by keyword, hybrid and search.
var responseFormatProperty = map[string]interface{}{
	"type":        "string",
	"enum":        []string{responseFormatDetailed, responseFormatConcise},
	"default":     responseFormatDetailed,
	"description": responseFormatDescription,
}

// parseResponseFormat validates the response_format argument. An empty value
// means detailed, which keeps existing consumers unchanged.
func parseResponseFormat(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", responseFormatDetailed:
		return responseFormatDetailed, nil
	case responseFormatConcise:
		return responseFormatConcise, nil
	default:
		return "", fmt.Errorf("response_format must be 'concise' or 'detailed', got %q", raw)
	}
}

// conciseSearchResponse is the compact envelope returned for response_format=concise.
type conciseSearchResponse struct {
	Mode           string                `json:"mode"`
	Query          string                `json:"query"`
	TotalResults   int                   `json:"total_results"`
	Results        []conciseSearchResult `json:"results"`
	SearchMetadata shared.SearchMetadata `json:"search_metadata"`
}

type conciseSearchResult struct {
	Rank       int     `json:"rank"`
	Score      float64 `json:"score"`
	MatchType  string  `json:"match_type,omitempty"`
	ChunkID    string  `json:"chunk_id"`
	DocumentID string  `json:"document_id"`
	URI        string  `json:"uri"`
	Title      string  `json:"title,omitempty"`
	StartLine  int     `json:"start_line,omitempty"`
	EndLine    int     `json:"end_line,omitempty"`
	Snippet    string  `json:"snippet"`
}

func buildConciseSearchResponse(result *shared.SearchResponse) *conciseSearchResponse {
	if result == nil {
		return &conciseSearchResponse{Results: []conciseSearchResult{}}
	}
	items := make([]conciseSearchResult, 0, len(result.Results))
	meta := result.SearchMetadata
	for _, entry := range result.Results {
		uri := entry.Document.FilePath
		if uri == "" {
			uri = entry.Document.Title
		}
		snippet, fallback := buildSnippet(entry.Chunk.Content, result.Query, conciseSnippetWidth)
		if fallback {
			meta.SnippetFallback = true
		}
		items = append(items, conciseSearchResult{
			Rank:       entry.Rank,
			Score:      entry.Score,
			MatchType:  entry.MatchType,
			ChunkID:    entry.Chunk.ID,
			DocumentID: entry.Document.ID,
			URI:        uri,
			Title:      entry.Document.Title,
			StartLine:  entry.Chunk.StartLine,
			EndLine:    entry.Chunk.EndLine,
			Snippet:    snippet,
		})
	}
	return &conciseSearchResponse{
		Mode:           result.Mode,
		Query:          result.Query,
		TotalResults:   result.TotalResults,
		Results:        items,
		SearchMetadata: meta,
	}
}

// buildSnippet returns at most width+2 runes of content (the two extra runes are
// the "…" marks), and whether no query term was found (fallback: the snippet then
// starts at the beginning of the content). The window is the one that covers the
// most distinct query terms.
//
// Terms are matched word by word, on folded text (case and Latin accents dropped, as
// the FTS index does with strip_accents=1). A word matches a query term when they
// share a common prefix of at least min(4, length of the shorter word): "running"
// matches "run", "cats" matches "cat", "securite" matches "sécurité". This stands in
// for the index's Porter stemmer, which is not available here.
func buildSnippet(content, query string, width int) (string, bool) {
	runes := []rune(content)
	if len(runes) == 0 {
		return "", false
	}
	if len(runes) <= width {
		return strings.Join(strings.Fields(content), " "), false
	}
	folded := make([]rune, len(runes))
	for i, r := range runes {
		folded[i] = foldRune(r)
	}
	terms := queryTerms(query)
	termRunes := make([][]rune, len(terms))
	for i, t := range terms {
		termRunes[i] = []rune(t)
	}

	// Hits come out in text order: words are visited left to right.
	// A whole-word match weighs 2 and a prefix match 1 (see the window scoring below).
	type hit struct{ pos, term, weight int }
	var hits []hit
	for _, w := range wordSpans(folded) {
		word := folded[w.start:w.end]
		for ti, term := range termRunes {
			if weight := matchWeight(term, word); weight > 0 {
				hits = append(hits, hit{pos: w.start, term: ti, weight: weight})
			}
		}
	}
	if len(hits) == 0 {
		head := strings.Join(strings.Fields(string(runes[:width])), " ")
		return withEllipsis(head, false, true), true
	}

	// Candidate windows start a little before each hit. Starts and ends never decrease
	// along the sorted hits, so one sliding window with two pointers counts the
	// distinct terms of every candidate in linear time. The earliest window wins ties.
	// A term weighs 2 in a window if a whole word of it is there, 1 if only prefix matches
	// are, and 0 if it is absent. The window score is the sum over the terms: forty
	// prefix matches of one term do not outweigh one whole word of another. Ties go to
	// the window that covers more distinct terms, then to the earliest one.
	const leadIn = 60
	whole := make([]int, len(termRunes))
	anyHits := make([]int, len(termRunes))
	score, covered := 0, 0
	termWeight := func(t int) int {
		switch {
		case whole[t] > 0:
			return 2
		case anyHits[t] > 0:
			return 1
		}
		return 0
	}
	bestStart, bestScore, bestCover := 0, -1, -1
	right, left := 0, 0
	for _, h := range hits {
		start, end := windowAt(h.pos-leadIn, width, len(runes))
		for right < len(hits) && hits[right].pos < end {
			t := hits[right].term
			before := termWeight(t)
			anyHits[t]++
			if hits[right].weight == 2 {
				whole[t]++
			}
			if anyHits[t] == 1 {
				covered++
			}
			score += termWeight(t) - before
			right++
		}
		for left < right && hits[left].pos < start {
			t := hits[left].term
			before := termWeight(t)
			anyHits[t]--
			if hits[left].weight == 2 {
				whole[t]--
			}
			if anyHits[t] == 0 {
				covered--
			}
			score += termWeight(t) - before
			left++
		}
		if score > bestScore || (score == bestScore && covered > bestCover) {
			bestStart, bestScore, bestCover = start, score, covered
		}
	}
	start, end := windowAt(bestStart, width, len(runes))
	snippet := strings.Join(strings.Fields(string(runes[start:end])), " ")
	return withEllipsis(snippet, start > 0, end < len(runes)), false
}

// matchWeight is 2 for a whole-word match, 1 for a prefix match, 0 for no match.
func matchWeight(term, word []rune) int {
	if equalRunes(term, word) {
		return 2
	}
	if stemMatch(term, word) {
		return 1
	}
	return 0
}

// stemMatch reports whether a query term and a text word share a common prefix of at
// least min(4, len(term), len(word)) runes.
//
// Matching rules. A term with a digit, or an identifier (AGENT-530, calcul_taux_marge),
// matches a whole word only: "AGENT-531" is not "AGENT-530". A prefix match needs both
// the word and the term to have at least four runes, and a common prefix of at least
// four runes: "sécurité" matches "securite" by prefix, but "s" never matches it. Below
// that, equality of whole words.
func stemMatch(term, word []rune) bool {
	const minLen = 4
	if hasDigit(term) || search.IsIdentifier(string(term)) || len(term) < minLen || len(word) < minLen {
		return equalRunes(term, word)
	}
	n := 0
	for n < len(term) && n < len(word) && term[n] == word[n] {
		n++
	}
	return n >= minLen
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasDigit(term []rune) bool {
	for _, r := range term {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

type span struct{ start, end int }

// wordSpans returns the words of the folded text: runs of letters, digits, underscores,
// dots and hyphens, without trailing dots or hyphens, so "AGENT-530." is "AGENT-530".
func wordSpans(folded []rune) []span {
	isWord := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.'
	}
	var out []span
	for i := 0; i < len(folded); {
		if !isWord(folded[i]) {
			i++
			continue
		}
		j := i
		for j < len(folded) && isWord(folded[j]) {
			j++
		}
		end := j
		for end > i && (folded[end-1] == '.' || folded[end-1] == '-') {
			end--
		}
		if end > i {
			out = append(out, span{start: i, end: end})
		}
		i = j
	}
	return out
}

// windowAt returns [start, start+width) clipped to [0, length).
func windowAt(start, width, length int) (int, int) {
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > length {
		end = length
		start = end - width
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

func withEllipsis(text string, cutBefore, cutAfter bool) string {
	if cutBefore {
		text = "…" + text
	}
	if cutAfter {
		text += "…"
	}
	return text
}

// findAll returns the start positions of needle in haystack (both already folded).
// foldRune lowercases r and drops its Latin accent. It maps one rune to one rune,
// so positions in the original text stay valid. Ligatures (œ, æ) become their
// first letter: an approximation, applied to both the text and the query.
func foldRune(r rune) rune {
	r = unicode.ToLower(r)
	if f, ok := accentFold[r]; ok {
		return f
	}
	return r
}

var accentFold = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'æ': 'a',
	'ç': 'c',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o', 'œ': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u',
	'ý': 'y', 'ÿ': 'y',
}

// queryTerms splits the query into folded terms, longest first, so that the most
// specific term anchors the snippet. One-rune terms are kept only when nothing longer
// is in the query.
func queryTerms(query string) []string {
	fields := strings.FieldsFunc(strings.Map(foldRune, query), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.')
	})
	seen := make(map[string]struct{}, len(fields))
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, "-.")
		if f == "" {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		terms = append(terms, f)
	}
	// One-rune terms count only when the query has nothing longer: in "l'agent" the
	// letter "l" must not anchor the snippet, but in "x" alone it is the term.
	hasLonger := false
	for _, t := range terms {
		if len([]rune(t)) >= 2 {
			hasLonger = true
			break
		}
	}
	if hasLonger {
		kept := terms[:0]
		for _, t := range terms {
			if len([]rune(t)) >= 2 {
				kept = append(kept, t)
			}
		}
		terms = kept
	}
	sort.SliceStable(terms, func(i, j int) bool {
		return len([]rune(terms[i])) > len([]rune(terms[j]))
	})
	return terms
}
