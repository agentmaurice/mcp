package observe

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

const (
	cohortInputMaxNodes       = 100000
	cohortInputMaxStringBytes = 8 * 1024 * 1024
	cohortInputMaxDepth       = 32
)

// InputLimits exposes structural limits for capability advertisements. It is
// not a claim about total serialized JSON bytes.
func InputLimits() map[string]int {
	return map[string]int{"max_input_nodes": cohortInputMaxNodes, "max_string_bytes": cohortInputMaxStringBytes, "max_depth": cohortInputMaxDepth}
}

var diagnosticReportSliceType = reflect.TypeOf([]DiagnosticReport{})

// ValidateCohortInput exposes the bounded validator to the MCP adapter while
// keeping the implementation and its limits owned by Observe.
func ValidateCohortInput(ctx context.Context, reports, baseline any) error {
	return validateCohortInput(ctx, reports, baseline)
}

// validateCohortInput bounds decoded MCP values before any JSON marshal or
// deduplication. It accepts the two report containers used by the server and
// walks every serializable value, including unknown fields.
func validateCohortInput(ctx context.Context, reports, baseline any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("input validation canceled: %w", err)
	}
	arrays := []struct {
		label string
		value any
	}{
		{label: "reports", value: reports},
		{label: "baseline_reports", value: baseline},
	}
	total := 0
	for _, item := range arrays {
		if item.value == nil {
			continue
		}
		value := reflect.ValueOf(item.value)
		if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
			return fmt.Errorf("%s must be an array", item.label)
		}
		if value.Len() > CohortMaxReports {
			return fmt.Errorf("%s exceed %d", item.label, CohortMaxReports)
		}
		total += value.Len()
		if total > CohortMaxReports {
			return fmt.Errorf("combined reports exceed %d", CohortMaxReports)
		}
		if value.Type() == diagnosticReportSliceType {
			for index := 0; index < value.Len(); index++ {
				if err := validateTypedReportEvents(value.Index(index), item.label); err != nil {
					return err
				}
			}
			continue
		}
		for index := 0; index < value.Len(); index++ {
			if err := validateRawReport(value.Index(index), item.label); err != nil {
				return err
			}
		}
	}

	budget := cohortInputBudget{ctx: ctx}
	for _, item := range arrays {
		if item.value == nil {
			continue
		}
		if err := budget.walk(reflect.ValueOf(item.value), 0); err != nil {
			return fmt.Errorf("%s: %w", item.label, err)
		}
	}
	return nil
}

func validateTypedReportEvents(value reflect.Value, label string) error {
	events := value.FieldByName("Events")
	if events.IsValid() && (events.Kind() != reflect.Slice && events.Kind() != reflect.Array) {
		return fmt.Errorf("%s events must be an array", label)
	}
	if events.IsValid() && events.Len() > CohortMaxEventsPerReport {
		return fmt.Errorf("events exceed %d", CohortMaxEventsPerReport)
	}
	return nil
}

func validateRawReport(value reflect.Value, label string) error {
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return fmt.Errorf("%s entries must be objects", label)
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Map || value.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("%s entries must be objects", label)
	}
	events := value.MapIndex(reflect.ValueOf("events").Convert(value.Type().Key()))
	if !events.IsValid() {
		return nil
	}
	if events.Kind() == reflect.Interface {
		if events.IsNil() {
			// JSON null is equivalent to an absent optional events field.
			return nil
		}
		events = events.Elem()
	}
	if events.Kind() != reflect.Slice && events.Kind() != reflect.Array {
		return fmt.Errorf("%s events must be an array", label)
	}
	if events.Len() > CohortMaxEventsPerReport {
		return fmt.Errorf("events exceed %d", CohortMaxEventsPerReport)
	}
	return nil
}

type cohortInputBudget struct {
	ctx         context.Context
	nodes       int
	stringBytes int
	active      []reflect.Value
}

func (b *cohortInputBudget) walk(value reflect.Value, depth int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > cohortInputMaxDepth {
		return fmt.Errorf("input nesting exceeds %d", cohortInputMaxDepth)
	}
	b.nodes++
	if b.nodes%256 == 0 {
		select {
		case <-b.ctx.Done():
			return fmt.Errorf("input validation canceled: %w", b.ctx.Err())
		default:
		}
	}
	if b.nodes > cohortInputMaxNodes {
		return fmt.Errorf("input exceeds %d nodes", cohortInputMaxNodes)
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.String:
		b.stringBytes += value.Len()
		if b.stringBytes > cohortInputMaxStringBytes {
			return fmt.Errorf("input strings exceed %d bytes", cohortInputMaxStringBytes)
		}
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		if b.seenActive(value) {
			return fmt.Errorf("cyclic input reference")
		}
		b.active = append(b.active, value)
		err := b.walk(value.Elem(), depth+1)
		b.active = b.active[:len(b.active)-1]
		return err
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if b.seenActive(value) {
			return fmt.Errorf("cyclic input reference")
		}
		b.active = append(b.active, value)
		for _, key := range value.MapKeys() {
			if err := b.walk(key, depth+1); err != nil {
				b.active = b.active[:len(b.active)-1]
				return err
			}
			if err := b.walk(value.MapIndex(key), depth+1); err != nil {
				b.active = b.active[:len(b.active)-1]
				return err
			}
		}
		b.active = b.active[:len(b.active)-1]
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		if value.Kind() == reflect.Slice && b.seenActive(value) {
			return fmt.Errorf("cyclic input reference")
		}
		if value.Kind() == reflect.Slice {
			b.active = append(b.active, value)
		}
		for index := 0; index < value.Len(); index++ {
			if err := b.walk(value.Index(index), depth+1); err != nil {
				if value.Kind() == reflect.Slice {
					b.active = b.active[:len(b.active)-1]
				}
				return err
			}
		}
		if value.Kind() == reflect.Slice {
			b.active = b.active[:len(b.active)-1]
		}
	case reflect.Struct:
		typeOfValue := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := typeOfValue.Field(index)
			if field.PkgPath != "" || jsonFieldIgnored(field.Tag.Get("json")) {
				continue
			}
			if err := b.walk(value.Field(index), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *cohortInputBudget) seenActive(value reflect.Value) bool {
	for _, active := range b.active {
		if sameInputReference(active, value) {
			return true
		}
	}
	return false
}

func sameInputReference(left, right reflect.Value) bool {
	if left.Kind() != right.Kind() {
		return false
	}
	switch left.Kind() {
	case reflect.Map, reflect.Pointer, reflect.Slice:
		return left.UnsafePointer() == right.UnsafePointer()
	default:
		return left == right
	}
}

func jsonFieldIgnored(tag string) bool {
	return strings.Split(tag, ",")[0] == "-"
}
