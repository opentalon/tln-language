package constraints

import (
	"testing"
	"time"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/lexer"
	"github.com/opentalon/tln-language/internal/parser"
)

// evalExprAt parses a single expression (as the left side of a throwaway
// detect selector's compare) and evaluates it at a fixed clock, so the date
// builtins are deterministic. Mirrors evalStr but with an explicit `now`.
func evalExprAt(t *testing.T, expr string, row map[string]any, now time.Time) any {
	t.Helper()
	src := `detect "T" { for records where ` + expr + ` == "SENTINEL" }`
	tokens, ld := lexer.Lex("t.tln", src)
	if ld.HasErrors() {
		t.Fatalf("lex %q: %v", expr, ld)
	}
	prog, pd := parser.Parse("t.tln", tokens)
	if pd.HasErrors() {
		t.Fatalf("parse %q: %v", expr, pd)
	}
	det := prog.Blocks[0].(*ast.DetectBlock)
	cmp := det.Selector.Conditions[0].(*ast.CompareCondition)
	v, err := EvalExpr(cmp.Left, row, now)
	if err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
	return v
}

func TestDateBuiltins(t *testing.T) {
	// A fixed clock with a time-of-day, so `now()` keeps its time while
	// `today`/`date(...)` truncate to the day.
	now := time.Date(2026, 9, 16, 14, 30, 0, 0, time.UTC)
	row := map[string]any{
		"due_on": "2026-09-23",           // 7 days ahead
		"past":   "2026-09-01",           // 15 days ago
		"rfc":    "2026-09-19T08:00:00Z", // 3 days ahead, RFC3339
	}

	cases := []struct {
		expr string
		want any
	}{
		{`days_until(attr "due_on")`, float64(7)},
		{`days_until(attr "past")`, float64(-15)},
		{`days_until(attr "rfc")`, float64(3)}, // time-of-day truncated before diff
		{`days_between(attr "past", attr "due_on")`, float64(22)},
		{`date(attr "due_on")`, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		{`now()`, now},
	}
	for _, c := range cases {
		if got := evalExprAt(t, c.expr, row, now); got != c.want {
			t.Errorf("%s = %v (%T), want %v (%T)", c.expr, got, got, c.want, c.want)
		}
	}
}

// The date builtins are meant to drive numeric `when` guards — the whole point
// of a scheduled "warn N days before due" rule.
func TestDaysUntilInGuard(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	src := `detect "Due" { for records where days_until(attr "due_on") <= 7 }`
	tokens, ld := lexer.Lex("t.tln", src)
	if ld.HasErrors() {
		t.Fatalf("lex: %v", ld)
	}
	prog, pd := parser.Parse("t.tln", tokens)
	if pd.HasErrors() {
		t.Fatalf("parse: %v", pd)
	}
	cond := prog.Blocks[0].(*ast.DetectBlock).Selector.Conditions[0]

	if ok, err := EvalConditionAt(cond, map[string]any{"due_on": "2026-09-20"}, now); err != nil || !ok {
		t.Fatalf("due in 4 days should match: ok=%v err=%v", ok, err)
	}
	if ok, err := EvalConditionAt(cond, map[string]any{"due_on": "2026-10-30"}, now); err != nil || ok {
		t.Fatalf("due in 44 days should NOT match: ok=%v err=%v", ok, err)
	}
}

// A date value stringifies ISO so it drops into a query window; a timestamp
// keeps its time.
func TestDateStringify(t *testing.T) {
	now := time.Date(2026, 9, 16, 9, 5, 0, 0, time.UTC)
	if got := stringify(dateOnly(now)); got != "2026-09-16" {
		t.Errorf("date stringify = %q, want 2026-09-16", got)
	}
	if got := stringify(now); got != "2026-09-16T09:05:00Z" {
		t.Errorf("timestamp stringify = %q, want RFC3339", got)
	}
}

// An unrecognizable date is a clear error, not a silent zero.
func TestDaysUntilBadDate(t *testing.T) {
	src := `detect "T" { for records where days_until(attr "d") == 0 }`
	tokens, _ := lexer.Lex("t.tln", src)
	prog, _ := parser.Parse("t.tln", tokens)
	cmp := prog.Blocks[0].(*ast.DetectBlock).Selector.Conditions[0].(*ast.CompareCondition)
	if _, err := EvalExpr(cmp.Left, map[string]any{"d": "not-a-date"}, time.Now().UTC()); err == nil {
		t.Fatal("expected an error for an unrecognizable date")
	}
}
