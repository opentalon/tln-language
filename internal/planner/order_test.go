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
	// An enrich that refreshes the attribute it filters on reads its own write.
	plans := map[string]*QueryPlan{
		"refresh": {Reads: []string{"stock"}, Writes: []string{"stock"}},
		"alert":   {Reads: []string{"stock"}},
	}
	order, cycle := ExecutionOrder(plans)
	if want := []string{"refresh", "alert"}; !reflect.DeepEqual(order, want) || cycle != nil {
		t.Fatalf("order = %v cycle = %v, want %v and no cycle", order, cycle, want)
	}
}

func TestExecutionOrderCycleRunsInNameOrderLast(t *testing.T) {
	plans := map[string]*QueryPlan{
		"x": {Reads: []string{"b"}, Writes: []string{"a"}},
		"y": {Reads: []string{"a"}, Writes: []string{"b"}},
		"z": {},
	}
	order, cycle := ExecutionOrder(plans)
	if want := []string{"z", "x", "y"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if want := []string{"x", "y"}; !reflect.DeepEqual(cycle, want) {
		t.Fatalf("cycle = %v, want %v", cycle, want)
	}
}
