package queryint

import (
	"context"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"go.uber.org/zap"
)

// Analyzer implements query analysis and intent detection
type Analyzer struct {
	logger *zap.Logger
}

// NewAnalyzer creates a new query analyzer
func NewAnalyzer(logger *zap.Logger) *Analyzer {
	return &Analyzer{
		logger: logger.Named("queryint"),
	}
}

// Analyze analyzes a query and extracts intent and keywords
func (a *Analyzer) Analyze(ctx context.Context, query string) (*shared.QueryAnalysis, error) {
	a.logger.Debug("analyzing query", zap.String("query", query))

	keywords := a.extractKeywords(query)
	language := a.detectLanguage(query)
	intent := a.detectIntent(query)

	return &shared.QueryAnalysis{
		Keywords: keywords,
		Language: language,
		Intent:   intent,
	}, nil
}

// extractKeywords extracts important keywords from the query
func (a *Analyzer) extractKeywords(query string) []string {
	// Simple keyword extraction based on word filtering
	// In production, this would use NLP/TF-IDF

	stopWords := map[string]bool{
		"le": true, "la": true, "les": true, "un": true, "une": true, "des": true,
		"de": true, "du": true, "et": true, "ou": true, "mais": true,
		"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
		"in": true, "on": true, "at": true, "to": true, "for": true,
		"est": true, "sont": true, "was": true, "were": true, "is": true, "are": true,
		"que": true, "qui": true, "what": true, "when": true, "where": true, "how": true,
		"comment": true, "quand": true, "où": true, "pourquoi": true, "why": true,
	}

	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_')
	})

	var keywords []string
	seen := make(map[string]bool)

	for _, word := range words {
		word = strings.TrimSpace(word)
		if len(word) < 3 {
			continue
		}
		if stopWords[word] {
			continue
		}
		if seen[word] {
			continue
		}
		seen[word] = true
		keywords = append(keywords, word)
	}

	return keywords
}

// detectLanguage detects the query language
func (a *Analyzer) detectLanguage(query string) string {
	// Simple language detection based on common words
	// In production, this would use a proper language detection library

	frenchIndicators := []string{"le ", "la ", "les ", "de ", "que ", "qui ", "dans ", "pour ", "avec "}
	englishIndicators := []string{"the ", "a ", "an ", "that ", "which ", "in ", "for ", "with "}

	queryLower := strings.ToLower(query)

	frenchScore := 0
	for _, indicator := range frenchIndicators {
		if strings.Contains(queryLower, indicator) {
			frenchScore++
		}
	}

	englishScore := 0
	for _, indicator := range englishIndicators {
		if strings.Contains(queryLower, indicator) {
			englishScore++
		}
	}

	if frenchScore > englishScore {
		return "fr"
	}
	return "en"
}

// detectIntent detects the query intent
func (a *Analyzer) detectIntent(query string) string {
	queryLower := strings.ToLower(query)

	// Question patterns
	questionWords := []string{"comment", "pourquoi", "quand", "où", "qui", "quoi",
		"how", "why", "when", "where", "who", "what", "?"}
	for _, word := range questionWords {
		if strings.Contains(queryLower, word) {
			return "question"
		}
	}

	// Search patterns
	searchWords := []string{"trouve", "cherche", "recherche", "list", "show", "find", "search"}
	for _, word := range searchWords {
		if strings.Contains(queryLower, word) {
			return "search"
		}
	}

	// Default to informational
	return "informational"
}
