package mcpserver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"

	"github.com/agentmaurice/mcpchatui/mcp/office/internal/engine"
)

// Typed shapes of the engine answers. A field that is required for a tool is a
// pointer (nil = absent); a mistyped or inconsistent answer is invalid_output.

type createAnswer struct {
	Sheets *int `json:"sheets"`
	Blocks *int `json:"blocks"`
}

type batchAnswer struct {
	Total   *int `json:"total"`
	Applied *int `json:"applied"`
	Failed  *int `json:"failed"`
	Skipped *int `json:"skipped"`
}

type applyAnswer struct {
	Batch *batchAnswer `json:"batch"`
	Cells *int         `json:"cells"`
}

type compareAnswer struct {
	Insertions        *int `json:"insertions"`
	Deletions         *int `json:"deletions"`
	ChangedParagraphs *int `json:"changed_paragraphs"`
}

type docInfoAnswer struct {
	Blocks     *int           `json:"blocks"`
	BlockTypes map[string]int `json:"block_types"`
	Headings   []struct {
		Level int    `json:"level"`
		Text  string `json:"text"`
	} `json:"headings"`
	Comments  *int  `json:"comments"`
	Footnotes *int  `json:"footnotes"`
	Endnotes  *int  `json:"endnotes"`
	Protected *bool `json:"protected"`
}

type checkAnswer struct {
	Issues *[]map[string]any `json:"issues"`
}

type workbookInfoAnswer struct {
	Sheets *[]struct {
		Name    *string `json:"name"`
		Rows    *int    `json:"rows"`
		Columns *int    `json:"columns"`
	} `json:"sheets"`
	DefinedNames *int `json:"defined_names"`
}

type sheetReadAnswer struct {
	Rows      *[][]any          `json:"rows"`
	Formulas  map[string]string `json:"formulas"`
	Truncated *bool             `json:"truncated"`
	Range     *string           `json:"range"`
}

func invalidOutput(why string) error {
	return &engine.Error{Code: engine.CodeInvalidOutput, Message: "the engine answer is invalid: " + why}
}

// parseDetail decodes the detail of an answer into v; a wrong type is invalid_output.
func parseDetail(res *engine.Result, v any) error {
	if len(bytes.TrimSpace(res.Detail)) == 0 || string(bytes.TrimSpace(res.Detail)) == "null" {
		return nil
	}
	if err := json.Unmarshal(res.Detail, v); err != nil {
		return invalidOutput("a field has the wrong type")
	}
	return nil
}

func negative(vals ...*int) bool {
	for _, v := range vals {
		if v != nil && *v < 0 {
			return true
		}
	}
	return false
}

func parseCreate(res *engine.Result) (createAnswer, error) {
	var a createAnswer
	if err := parseDetail(res, &a); err != nil {
		return a, err
	}
	if negative(a.Sheets, a.Blocks) {
		return a, invalidOutput("negative count")
	}
	return a, nil
}

func parseApply(res *engine.Result) (applyAnswer, error) {
	var a applyAnswer
	if err := parseDetail(res, &a); err != nil {
		return a, err
	}
	if a.Batch != nil {
		b := a.Batch
		if negative(b.Total, b.Applied, b.Failed, b.Skipped) {
			return a, invalidOutput("negative count")
		}
	}
	if negative(a.Cells) {
		return a, invalidOutput("negative count")
	}
	return a, nil
}

func parseCompare(res *engine.Result) (compareAnswer, error) {
	var a compareAnswer
	if err := parseDetail(res, &a); err != nil {
		return a, err
	}
	if a.Insertions == nil || a.Deletions == nil {
		return a, invalidOutput("the change counts are missing")
	}
	if negative(a.Insertions, a.Deletions, a.ChangedParagraphs) {
		return a, invalidOutput("negative count")
	}
	return a, nil
}

func parseDocInfo(res *engine.Result) (docInfoAnswer, error) {
	var a docInfoAnswer
	if err := parseDetail(res, &a); err != nil {
		return a, err
	}
	if a.Blocks == nil {
		return a, invalidOutput("missing blocks")
	}
	if negative(a.Blocks, a.Comments, a.Footnotes, a.Endnotes) {
		return a, invalidOutput("negative count")
	}
	return a, nil
}

func parseCheck(res *engine.Result) ([]map[string]any, error) {
	var a checkAnswer
	if err := parseDetail(res, &a); err != nil {
		return nil, err
	}
	if a.Issues == nil {
		return nil, invalidOutput("missing issues")
	}
	return *a.Issues, nil
}

func parseWorkbookInfo(res *engine.Result) (workbookInfoAnswer, error) {
	var a workbookInfoAnswer
	if err := parseDetail(res, &a); err != nil {
		return a, err
	}
	if a.Sheets == nil {
		return a, invalidOutput("missing sheets")
	}
	for _, s := range *a.Sheets {
		if s.Name == nil || negative(s.Rows, s.Columns) {
			return a, invalidOutput("a sheet is incomplete")
		}
	}
	return a, nil
}

func parseSheetRead(res *engine.Result) (sheetReadAnswer, error) {
	var a sheetReadAnswer
	if err := parseDetail(res, &a); err != nil {
		return a, err
	}
	if a.Rows == nil {
		return a, invalidOutput("missing rows")
	}
	return a, nil
}

const wordNS = nsWordML

// revisionCounts are the tracked changes found in word/document.xml, counted by
// element name in the WordprocessingML namespace (never by prefix).
type revisionCounts struct {
	Ins, Del int // w:ins, w:del
	Other    int // format changes and moves
}

func (r revisionCounts) Total() int { return r.Ins + r.Del + r.Other }

var otherRevisions = map[string]bool{
	"rPrChange": true, "pPrChange": true, "sectPrChange": true, "tblPrChange": true, "trPrChange": true,
	"tcPrChange": true, "numberingChange": true, "moveFrom": true, "moveTo": true,
}

// trackedCounts parses word/document.xml with encoding/xml (bounded read).
func trackedCounts(docx []byte) (revisionCounts, error) {
	var rc revisionCounts
	zr, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx)))
	if err != nil {
		return rc, err
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return rc, err
		}
		defer r.Close()
		lr := &io.LimitedReader{R: r, N: maxXMLPartBytes + 1}
		dec := xml.NewDecoder(lr)
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return revisionCounts{}, err
			}
			if lr.N <= 0 {
				return revisionCounts{}, errors.New("part too large")
			}
			se, ok := tok.(xml.StartElement)
			if !ok || se.Name.Space != wordNS {
				continue
			}
			switch {
			case se.Name.Local == "ins":
				rc.Ins++
			case se.Name.Local == "del":
				rc.Del++
			case otherRevisions[se.Name.Local]:
				rc.Other++
			}
		}
		return rc, nil
	}
	return rc, io.EOF
}
