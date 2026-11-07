package observe

import (
	"context"
	"strings"
	"testing"
)

func TestValidateCohortInputAcceptsBoundedTypedReports(t *testing.T) {
	reports := make([]DiagnosticReport, 20)
	for i := range reports {
		reports[i] = DiagnosticReport{ExecutionID: "execution", Events: []DiagnosticEvent{{Name: "step"}}}
	}
	if err := validateCohortInput(context.Background(), reports, nil); err != nil {
		t.Fatalf("bounded typed reports rejected: %v", err)
	}
}

func TestValidateCohortInputRejectsArrayAndEventLimitsBeforeWalk(t *testing.T) {
	tooManyRaw := make([]any, CohortMaxReports+1)
	if err := validateCohortInput(context.Background(), tooManyRaw, nil); err == nil {
		t.Fatal("expected raw report limit")
	}
	tooManyTyped := make([]DiagnosticReport, CohortMaxReports+1)
	if err := validateCohortInput(context.Background(), tooManyTyped, nil); err == nil {
		t.Fatal("expected typed report limit")
	}
	rawEvents := []any{map[string]any{"events": make([]any, CohortMaxEventsPerReport+1)}}
	if err := validateCohortInput(context.Background(), rawEvents, nil); err == nil {
		t.Fatal("expected raw event limit")
	}
	if err := validateCohortInput(context.Background(), []any{map[string]any{"events": nil}}, nil); err != nil {
		t.Fatalf("null events should be treated as absent: %v", err)
	}
	typedEvents := []DiagnosticReport{{Events: make([]DiagnosticEvent, CohortMaxEventsPerReport+1)}}
	if err := validateCohortInput(context.Background(), typedEvents, nil); err == nil {
		t.Fatal("expected typed event limit")
	}
	if err := validateCohortInput(context.Background(), make([]DiagnosticReport, CohortMaxReports/2+1), make([]DiagnosticReport, CohortMaxReports/2+1)); err == nil {
		t.Fatal("expected combined report limit")
	}
}

func TestValidateCohortInputRejectsDeepLargeCyclicAndCanceledValues(t *testing.T) {
	large := []any{map[string]any{"events": []any{map[string]any{"name": strings.Repeat("x", cohortInputMaxStringBytes+1)}}}}
	if err := validateCohortInput(context.Background(), large, nil); err == nil {
		t.Fatal("expected string byte budget")
	}
	deep := any(map[string]any{})
	root := deep.(map[string]any)
	for i := 0; i < cohortInputMaxDepth+2; i++ {
		next := map[string]any{}
		root["next"] = next
		root = next
	}
	if err := validateCohortInput(context.Background(), []any{deep}, nil); err == nil {
		t.Fatal("expected depth budget")
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if err := validateCohortInput(context.Background(), []any{cyclic}, nil); err == nil {
		t.Fatal("expected cycle rejection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateCohortInput(ctx, []any{map[string]any{"events": []any{map[string]any{"name": "x"}}}}, nil); err == nil {
		t.Fatal("expected canceled validation")
	}
}

func TestValidateCohortInputWalksUnknownNestedSerializedFieldsAndNodes(t *testing.T) {
	nested := []any{map[string]any{"metadata": map[string]any{"coverage": map[string]any{"data": strings.Repeat("x", cohortInputMaxStringBytes)}}}}
	if err := validateCohortInput(context.Background(), nested, nil); err == nil {
		t.Fatal("expected nested unknown string budget")
	}
	nodes := make([]any, cohortInputMaxNodes+1)
	if err := validateCohortInput(context.Background(), []any{map[string]any{"metadata": nodes}}, nil); err == nil || !strings.Contains(err.Error(), "100000 nodes") {
		t.Fatal("expected node budget")
	}
}
