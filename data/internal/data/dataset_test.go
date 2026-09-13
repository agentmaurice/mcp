package data

import "testing"

func TestProfileQueryAndExport(t *testing.T) {
	dataset, err := Parse("csv", "team,score\nA,10\nB,20\nA,30\n")
	if err != nil {
		t.Fatal(err)
	}
	profiles := Profile(dataset)
	if len(profiles) != 2 || profiles[1].Type != "number" || profiles[1].Min != float64(10) || profiles[1].Max != float64(30) {
		t.Fatalf("unexpected profiles: %#v", profiles)
	}
	result, err := Query(dataset, QueryPlan{Filters: []Filter{{Column: "team", Op: "eq", Value: "A"}}, Aggregations: []Aggregation{{Column: "score", Op: "avg", As: "average"}}})
	if err != nil || result.Rows[0]["average"] != float64(20) {
		t.Fatalf("unexpected query: %#v, %v", result, err)
	}
	payload, mime, err := Export(result, "json")
	if err != nil || mime != "application/json" || string(payload) != `[{"average":20}]` {
		t.Fatalf("unexpected export: %s %s %v", payload, mime, err)
	}
}

func TestRejectsUnknownColumnAndSQLLikeOperation(t *testing.T) {
	dataset, _ := Parse("json", `[{"a":1}]`)
	if _, err := Query(dataset, QueryPlan{Filters: []Filter{{Column: "missing", Op: "eq", Value: 1}}}); err == nil {
		t.Fatal("expected unknown column error")
	}
	if _, err := Query(dataset, QueryPlan{Filters: []Filter{{Column: "a", Op: "select *", Value: 1}}}); err == nil {
		t.Fatal("expected closed operation error")
	}
}
