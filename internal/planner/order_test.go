package planner

import (
	"reflect"
	"testing"
)

func TestExecutionOrderWritersBeforeReaders(t *testing.T) {
	plans := map[string]*QueryPlan{
		"a rule":   {Reads: []string{"unit.llm_result"}},
		"b review": {Reads: []string{"unit.access_change"}, Writes: []string{"unit.llm_result"}},
		"c decide": {Reads: []string{"unit.name"}, Writes: []string{"unit.access_change"}},
		"d other":  {Reads: []string{"pr.title"}},
	}
	order, cycle := ExecutionOrder(plans)
	if want := []string{"c decide", "b review", "a rule", "d other"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if cycle != nil {
		t.Fatalf("cycle = %v, want none", cycle)
	}
}

func TestExecutionOrderSelfReadIsNotADependency(t *testing.T) {
	plans := map[string]*QueryPlan{
		"refresh": {Reads: []string{"stock"}, Writes: []string{"stock"}},
		"alert":   {Reads: []string{"stock"}},
	}
	order, cycle := ExecutionOrder(plans)
	if want := []string{"refresh", "alert"}; !reflect.DeepEqual(order, want) || cycle != nil {
		t.Fatalf("order = %v cycle = %v, want %v and no cycle", order, cycle, want)
	}
}

func TestExecutionOrderBreaksCycleAtMinNameMember(t *testing.T) {
	plans := map[string]*QueryPlan{
		"x": {Reads: []string{"b"}, Writes: []string{"a"}},
		"y": {Reads: []string{"a"}, Writes: []string{"b"}},
		"z": {},
	}
	order, cycle := ExecutionOrder(plans)
	if want := []string{"z", "x", "y"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if want := []string{"x"}; !reflect.DeepEqual(cycle, want) {
		t.Fatalf("cycle = %v, want %v", cycle, want)
	}
}

func TestExecutionOrderReaderDownstreamOfCycleRunsAfterItsWriter(t *testing.T) {
	plans := map[string]*QueryPlan{
		"x":        {Reads: []string{"b"}, Writes: []string{"a"}},
		"y":        {Reads: []string{"a"}, Writes: []string{"b"}},
		"a reader": {Reads: []string{"a"}},
	}
	order, cycle := ExecutionOrder(plans)
	if want := []string{"x", "a reader", "y"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if want := []string{"x"}; !reflect.DeepEqual(cycle, want) {
		t.Fatalf("cycle = %v, want %v", cycle, want)
	}
}

func TestPlanReadsCoverEveryGate(t *testing.T) {
	plans := planAll(t, `
enrich "z enrich" {
  for records where type == "unit"
  stale_after 1 hour
  tool "llm" "review" { unit attr "name" }
  update attr "reviewed_at" from result.at
  update attr "llm_result" from result.verdict
}

rule "a temporal" {
  for records where type == "unit" and attr "reviewed_at" newer_than 1 hours
  do block "pr.merge"
}

detect "b mismatch" {
  for records where type == "unit" and attr "llm_result" == "mismatch"
  flag matching items
}
`)
	order, _ := ExecutionOrder(plans)
	if want := []string{"z enrich", "a temporal", "b mismatch"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}
