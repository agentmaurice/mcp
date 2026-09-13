package data

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	MaxRows    = 10000
	MaxColumns = 200
)

type Dataset struct {
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
}

type ColumnProfile struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	NullCount   int         `json:"null_count"`
	Distinct    int         `json:"distinct_count"`
	DistinctCut bool        `json:"distinct_truncated,omitempty"`
	Min         interface{} `json:"min,omitempty"`
	Max         interface{} `json:"max,omitempty"`
}

type Filter struct {
	Column string      `json:"column"`
	Op     string      `json:"op"`
	Value  interface{} `json:"value"`
}

type Sort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

type Aggregation struct {
	Column string `json:"column,omitempty"`
	Op     string `json:"op"`
	As     string `json:"as,omitempty"`
}

type QueryPlan struct {
	Select       []string      `json:"select,omitempty"`
	Filters      []Filter      `json:"filters,omitempty"`
	Sort         *Sort         `json:"sort,omitempty"`
	Limit        int           `json:"limit,omitempty"`
	Aggregations []Aggregation `json:"aggregations,omitempty"`
}

func Parse(format, content string) (Dataset, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "csv":
		return parseCSV(content)
	case "json":
		return parseJSON(content)
	default:
		return Dataset{}, fmt.Errorf("format must be csv or json")
	}
}

func Profile(dataset Dataset) []ColumnProfile {
	profiles := make([]ColumnProfile, 0, len(dataset.Columns))
	for _, column := range dataset.Columns {
		profile := ColumnProfile{Name: column, Type: "null"}
		distinct := map[string]bool{}
		var minNumber, maxNumber float64
		hasNumber := false
		for _, row := range dataset.Rows {
			value := row[column]
			if value == nil || value == "" {
				profile.NullCount++
				continue
			}
			profile.Type = mergeType(profile.Type, valueType(value))
			if len(distinct) < 1000 {
				encoded, _ := json.Marshal(value)
				distinct[string(encoded)] = true
			} else {
				profile.DistinctCut = true
			}
			if number, ok := asNumber(value); ok {
				if !hasNumber || number < minNumber {
					minNumber = number
				}
				if !hasNumber || number > maxNumber {
					maxNumber = number
				}
				hasNumber = true
			}
		}
		profile.Distinct = len(distinct)
		if hasNumber && profile.Type == "number" {
			profile.Min, profile.Max = minNumber, maxNumber
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

func Query(dataset Dataset, plan QueryPlan) (Dataset, error) {
	if plan.Limit < 0 || plan.Limit > MaxRows {
		return Dataset{}, fmt.Errorf("limit must be between 0 and %d", MaxRows)
	}
	columns := dataset.Columns
	if len(plan.Select) > 0 {
		for _, column := range plan.Select {
			if !hasColumn(dataset.Columns, column) {
				return Dataset{}, fmt.Errorf("unknown selected column %q", column)
			}
		}
		columns = append([]string(nil), plan.Select...)
	}
	rows := make([]map[string]interface{}, 0, len(dataset.Rows))
	for _, row := range dataset.Rows {
		matches := true
		for _, filter := range plan.Filters {
			if !hasColumn(dataset.Columns, filter.Column) {
				return Dataset{}, fmt.Errorf("unknown filter column %q", filter.Column)
			}
			ok, err := compare(row[filter.Column], filter.Value, filter.Op)
			if err != nil {
				return Dataset{}, err
			}
			if !ok {
				matches = false
				break
			}
		}
		if matches {
			rows = append(rows, row)
		}
	}
	if len(plan.Aggregations) > 0 {
		aggregated, err := aggregate(rows, plan.Aggregations, dataset.Columns)
		if err != nil {
			return Dataset{}, err
		}
		return aggregated, nil
	}
	if plan.Sort != nil {
		if !hasColumn(dataset.Columns, plan.Sort.Column) {
			return Dataset{}, fmt.Errorf("unknown sort column %q", plan.Sort.Column)
		}
		sort.SliceStable(rows, func(i, j int) bool {
			less := compareLess(rows[i][plan.Sort.Column], rows[j][plan.Sort.Column])
			if plan.Sort.Desc {
				return !less
			}
			return less
		})
	}
	if plan.Limit > 0 && len(rows) > plan.Limit {
		rows = rows[:plan.Limit]
	}
	projected := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		selected := make(map[string]interface{}, len(columns))
		for _, column := range columns {
			selected[column] = row[column]
		}
		projected = append(projected, selected)
	}
	return Dataset{Columns: columns, Rows: projected}, nil
}

func Export(dataset Dataset, format string) ([]byte, string, error) {
	switch strings.ToLower(format) {
	case "json":
		payload, err := json.Marshal(dataset.Rows)
		return payload, "application/json", err
	case "csv":
		var buffer bytes.Buffer
		writer := csv.NewWriter(&buffer)
		if err := writer.Write(dataset.Columns); err != nil {
			return nil, "", err
		}
		for _, row := range dataset.Rows {
			record := make([]string, len(dataset.Columns))
			for index, column := range dataset.Columns {
				record[index] = fmt.Sprint(row[column])
			}
			if err := writer.Write(record); err != nil {
				return nil, "", err
			}
		}
		writer.Flush()
		return buffer.Bytes(), "text/csv", writer.Error()
	default:
		return nil, "", fmt.Errorf("export format must be csv or json")
	}
}

func parseCSV(content string) (Dataset, error) {
	reader := csv.NewReader(strings.NewReader(content))
	reader.ReuseRecord = false
	records, err := reader.ReadAll()
	if err != nil {
		return Dataset{}, fmt.Errorf("invalid CSV: %w", err)
	}
	if len(records) == 0 || len(records[0]) == 0 {
		return Dataset{}, fmt.Errorf("CSV header is required")
	}
	if len(records)-1 > MaxRows || len(records[0]) > MaxColumns {
		return Dataset{}, fmt.Errorf("dataset exceeds %d rows or %d columns", MaxRows, MaxColumns)
	}
	columns := uniqueColumns(records[0])
	rows := make([]map[string]interface{}, 0, len(records)-1)
	for _, record := range records[1:] {
		row := make(map[string]interface{}, len(columns))
		for index, column := range columns {
			if index < len(record) {
				row[column] = parseScalar(record[index])
			} else {
				row[column] = nil
			}
		}
		rows = append(rows, row)
	}
	return Dataset{Columns: columns, Rows: rows}, nil
}

func parseJSON(content string) (Dataset, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var rows []map[string]interface{}
	if err := decoder.Decode(&rows); err != nil {
		return Dataset{}, fmt.Errorf("JSON must be an array of objects: %w", err)
	}
	if len(rows) > MaxRows {
		return Dataset{}, fmt.Errorf("dataset exceeds %d rows", MaxRows)
	}
	seen := map[string]bool{}
	columns := make([]string, 0)
	for _, row := range rows {
		for column := range row {
			if !seen[column] {
				seen[column] = true
				columns = append(columns, column)
			}
		}
	}
	sort.Strings(columns)
	if len(columns) > MaxColumns {
		return Dataset{}, fmt.Errorf("dataset exceeds %d columns", MaxColumns)
	}
	return Dataset{Columns: columns, Rows: rows}, nil
}

func aggregate(rows []map[string]interface{}, aggregations []Aggregation, columns []string) (Dataset, error) {
	row := map[string]interface{}{}
	resultColumns := make([]string, 0, len(aggregations))
	for index, aggregation := range aggregations {
		op := strings.ToLower(aggregation.Op)
		name := aggregation.As
		if name == "" {
			name = fmt.Sprintf("%s_%s", op, aggregation.Column)
		}
		if op != "count" && !hasColumn(columns, aggregation.Column) {
			return Dataset{}, fmt.Errorf("unknown aggregation column %q", aggregation.Column)
		}
		values := make([]float64, 0, len(rows))
		for _, source := range rows {
			if number, ok := asNumber(source[aggregation.Column]); ok {
				values = append(values, number)
			}
		}
		switch op {
		case "count":
			row[name] = len(rows)
		case "sum", "avg", "min", "max":
			if len(values) == 0 {
				row[name] = nil
			} else {
				value := values[0]
				if op == "sum" || op == "avg" {
					value = 0
					for _, number := range values {
						value += number
					}
					if op == "avg" {
						value /= float64(len(values))
					}
				} else {
					for _, number := range values[1:] {
						if op == "min" {
							value = math.Min(value, number)
						} else {
							value = math.Max(value, number)
						}
					}
				}
				row[name] = value
			}
		default:
			return Dataset{}, fmt.Errorf("unsupported aggregation %q at index %d", op, index)
		}
		resultColumns = append(resultColumns, name)
	}
	return Dataset{Columns: resultColumns, Rows: []map[string]interface{}{row}}, nil
}

func compare(left, right interface{}, op string) (bool, error) {
	if leftNumber, ok := asNumber(left); ok {
		if rightNumber, rightOK := asNumber(right); rightOK {
			switch op {
			case "eq":
				return leftNumber == rightNumber, nil
			case "ne":
				return leftNumber != rightNumber, nil
			case "gt":
				return leftNumber > rightNumber, nil
			case "gte":
				return leftNumber >= rightNumber, nil
			case "lt":
				return leftNumber < rightNumber, nil
			case "lte":
				return leftNumber <= rightNumber, nil
			}
		}
	}
	leftText, rightText := fmt.Sprint(left), fmt.Sprint(right)
	switch op {
	case "eq":
		return leftText == rightText, nil
	case "ne":
		return leftText != rightText, nil
	case "contains":
		return strings.Contains(leftText, rightText), nil
	default:
		return false, fmt.Errorf("unsupported filter operation %q", op)
	}
}

func compareLess(left, right interface{}) bool {
	if a, ok := asNumber(left); ok {
		if b, otherOK := asNumber(right); otherOK {
			return a < b
		}
	}
	return fmt.Sprint(left) < fmt.Sprint(right)
}

func asNumber(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case string:
		result, err := strconv.ParseFloat(typed, 64)
		return result, err == nil
	default:
		return 0, false
	}
}

func parseScalar(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if boolean, err := strconv.ParseBool(value); err == nil {
		return boolean
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil {
		return number
	}
	return value
}

func valueType(value interface{}) string {
	switch value.(type) {
	case bool:
		return "boolean"
	case json.Number, float64, float32, int, int64:
		return "number"
	case string:
		return "string"
	default:
		return "object"
	}
}

func mergeType(current, next string) string {
	if current == "null" {
		return next
	}
	if current == next {
		return current
	}
	return "mixed"
}

func uniqueColumns(columns []string) []string {
	seen := map[string]bool{}
	result := make([]string, len(columns))
	for index, raw := range columns {
		column := strings.TrimSpace(raw)
		if column == "" {
			column = fmt.Sprintf("column_%d", index+1)
		}
		if seen[column] {
			column = fmt.Sprintf("%s_%d", column, index+1)
		}
		seen[column] = true
		result[index] = column
	}
	return result
}

func hasColumn(columns []string, expected string) bool {
	for _, column := range columns {
		if column == expected {
			return true
		}
	}
	return false
}
