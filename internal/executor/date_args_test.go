package executor

import (
	"context"
	"strings"
	"testing"
)

// The date + string toolkits must evaluate in tool-call args, not only in
// `when` guards — that's what lets a scheduled "due within N days" workflow
// build its query window (concat + today + arithmetic) and pass a computed
// number (days_until). Before the resolveExprValue fallback these resolved to
// nil.
func TestDateBuiltinsInMCPArg(t *testing.T) {
	var got map[string]any
	mock := &mockMCP{handler: func(_, tool string, args map[string]any) (any, error) {
		got = args
		return map[string]any{"tickets": []any{}}, nil
	}}
	e := &Executor{Tools: mock}
	plan := compilePlans(t, `
workflow "due" {
  step "find" {
    tool "timly-api" "list_tickets" {
      query concat("due_on:[", today, " TO ", today + 7 days, "]")
      lead days_until(today + 3 days)
    }
  }
}`)
	if _, err := e.Run(context.Background(), plan["due"]); err != nil {
		t.Fatal(err)
	}

	// concat + today built the window (exact dates are clock-dependent, so
	// assert the shape) — before the fix this whole arg was nil.
	q, ok := got["query"].(string)
	if !ok || !strings.HasPrefix(q, "due_on:[") || !strings.Contains(q, " TO ") || !strings.HasSuffix(q, "]") {
		t.Fatalf("query not built from concat+today: %#v", got["query"])
	}
	// days_until(today + 3 days) is 3 regardless of the wall clock — proves the
	// clock, arithmetic, and the days_until builtin all evaluate in an arg.
	if got["lead"] != float64(3) {
		t.Fatalf("lead = %#v, want 3", got["lead"])
	}
}
