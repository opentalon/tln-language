package executor

import (
	"context"
	"reflect"
	"testing"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/factstore"
	"github.com/opentalon/tln-language/internal/lexer"
	"github.com/opentalon/tln-language/internal/parser"
	"github.com/opentalon/tln-language/internal/planner"
	"github.com/opentalon/tln-language/internal/validator"
)

// runActionSrc compiles src, seeds the given facts into a MemoryStore, and
// runs every block, returning the results keyed by block name.
func runActionSrc(t *testing.T, src string, given []ast.TestDatum) map[string]*BlockResult {
	t.Helper()
	tokens, ld := lexer.Lex("actions.tln", src)
	if ld.HasErrors() {
		t.Fatalf("lex: %v", ld)
	}
	prog, pd := parser.Parse("actions.tln", tokens)
	if pd.HasErrors() {
		t.Fatalf("parse: %v", pd)
	}
	if vd := validator.Validate("actions.tln", prog); vd.HasErrors() {
		t.Fatalf("validate: %v", vd)
	}
	plans, planDiags := planner.Plan(prog)
	if planDiags.HasErrors() {
		t.Fatalf("plan: %v", planDiags)
	}

	store := factstore.NewMemoryStore()
	exec := &Executor{Client: store}
	if _, err := exec.Seed(context.Background(), &ast.Program{
		Blocks: []ast.Block{&ast.TestBlock{Given: given}},
	}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	results, err := exec.RunAll(context.Background(), plans)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	return results
}

func lowRiskPR(id int) []ast.TestDatum {
	return []ast.TestDatum{
		{Kind: "record", ID: id, Fields: map[string]any{"type": "pr"}},
		{Kind: "attr", ID: id, Fields: map[string]any{"risk": "low"}},
		{Kind: "attr", ID: id, Fields: map[string]any{"owner": "@alice"}},
		{Kind: "attr", ID: id, Fields: map[string]any{"files": 3.0}},
	}
}

const approveRule = `
rule "Tenant approve" {
  for records where type == "pr" and attr "risk" == "low"
  do approve "pr" attr "owner"
  do comment "pr" "{attr.owner} touched {attr.files} files"
}
`

// The runtime — not the test runner — must hand back the resolved actions.
func TestFireActions_RuntimeEmitsResolvedActions(t *testing.T) {
	results := runActionSrc(t, approveRule, lowRiskPR(7))
	got := results["Tenant approve"].Actions
	want := []FiredAction{
		{EntityID: 7, Rule: "Tenant approve", Verb: "approve", Args: []any{"pr", "@alice"}},
		{EntityID: 7, Rule: "Tenant approve", Verb: "comment", Args: []any{"pr", "@alice touched 3 files"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actions:\n got %#v\nwant %#v", got, want)
	}
}

// A rule with no `do` clauses reports an empty list, never nil — a host
// reading the API boundary shouldn't have to tell one from the other.
func TestFireActions_NoDoClausesIsEmptyNotNil(t *testing.T) {
	src := `
rule "Silent" {
  for records where type == "pr" and attr "risk" == "low"
  allow "merge"
}
`
	got := flatActions(runActionSrc(t, src, lowRiskPR(7)))
	if got == nil {
		t.Fatal("Actions is nil; want empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("want no actions, got %#v", got)
	}
}

// A rule whose condition matches nothing fires nothing.
func TestFireActions_NoMatchingRowsFiresNothing(t *testing.T) {
	facts := []ast.TestDatum{
		{Kind: "record", ID: 7, Fields: map[string]any{"type": "pr"}},
		{Kind: "attr", ID: 7, Fields: map[string]any{"risk": "high"}},
	}
	got := flatActions(runActionSrc(t, approveRule, facts))
	if len(got) != 0 {
		t.Fatalf("want no actions for an unmatched row, got %#v", got)
	}
}

// An `attr` argument naming a fact the row does not carry stays in the arg
// list as nil. Dropping it would shift every positional argument after it,
// and a nil is what keeps "missing" distinct from "empty" for the host.
func TestFireActions_MissingAttrIsNilNotDropped(t *testing.T) {
	src := `
rule "Assign" {
  for records where type == "pr" and attr "risk" == "low"
  do assign attr "reviewer" "fallback"
}
`
	facts := []ast.TestDatum{
		{Kind: "record", ID: 7, Fields: map[string]any{"type": "pr"}},
		{Kind: "attr", ID: 7, Fields: map[string]any{"risk": "low"}},
	}
	got := runActionSrc(t, src, facts)["Assign"].Actions
	if len(got) != 1 {
		t.Fatalf("want 1 action, got %#v", got)
	}
	want := []any{nil, "fallback"}
	if !reflect.DeepEqual(got[0].Args, want) {
		t.Fatalf("args: got %#v, want %#v", got[0].Args, want)
	}
}

// Resolution runs before firing: a rule defeated for a row contributes no
// actions for it, while the winner's still fire.
func TestFireActions_DefeatedRuleFiresNothing(t *testing.T) {
	src := approveRule + `
strict rule "Block secrets" {
  for records where type == "pr" and attr "risk" == "low"
  overrides "Tenant approve"
  do block "pr"
}
`
	results := runActionSrc(t, src, lowRiskPR(7))
	if got := results["Tenant approve"].Actions; len(got) != 0 {
		t.Fatalf("defeated rule fired %#v", got)
	}
	want := []FiredAction{{EntityID: 7, Rule: "Block secrets", Verb: "block", Args: []any{"pr"}}}
	if got := results["Block secrets"].Actions; !reflect.DeepEqual(got, want) {
		t.Fatalf("winner actions: got %#v, want %#v", got, want)
	}
}

// Defeat is per row: a rule defeated for the row the winner also matched
// still fires for rows the winner didn't match.
func TestFireActions_DefeatIsPerRow(t *testing.T) {
	src := approveRule + `
strict rule "Block secrets" {
  for records where type == "pr" and attr "secrets" == true
  overrides "Tenant approve"
  do block "pr"
}
`
	facts := append(lowRiskPR(7), lowRiskPR(8)...)
	facts = append(facts, ast.TestDatum{Kind: "attr", ID: 8, Fields: map[string]any{"secrets": true}})

	results := runActionSrc(t, src, facts)
	for _, a := range results["Tenant approve"].Actions {
		if a.EntityID == 8 {
			t.Fatalf("row 8 was defeated but still fired %#v", a)
		}
	}
	fired := 0
	for _, a := range results["Tenant approve"].Actions {
		if a.EntityID == 7 {
			fired++
		}
	}
	if fired != 2 {
		t.Fatalf("row 7 should still fire both actions, got %d", fired)
	}
}

// Same facts, same ruleset, twice — identical action lists. Downstream
// determinism claims rest on this.
func TestFireActions_Deterministic(t *testing.T) {
	src := approveRule + `
rule "Label size" {
  for records where type == "pr" and attr "files" > 1
  do label "pr" "size/small"
}
`
	facts := append(lowRiskPR(7), lowRiskPR(9)...)
	for i := 0; i < 5; i++ {
		a := flatActions(runActionSrc(t, src, facts))
		b := flatActions(runActionSrc(t, src, facts))
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("run %d differs:\n%#v\n%#v", i, a, b)
		}
	}
}

// flatActions concatenates every block's actions in sorted block order,
// mirroring what pkg/tln exposes as the run-level list.
func flatActions(results map[string]*BlockResult) []FiredAction {
	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	// Small sets; insertion sort keeps the helper dependency-free.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	out := []FiredAction{}
	for _, n := range names {
		out = append(out, results[n].Actions...)
	}
	return out
}

func TestFireActions_DottedAttrTemplateRefRenders(t *testing.T) {
	src := `
rule "Deps" {
  for records where type == "pr" and attr "pr.new_dependencies" > 0
  do comment "pr" "{attr.pr.new_dependencies} new deps, {pr.lines_changed} lines, draft {attr.pr.draft}"
}
`
	facts := []ast.TestDatum{
		{Kind: "record", ID: 7, Fields: map[string]any{"type": "pr"}},
		{Kind: "attr", ID: 7, Fields: map[string]any{"pr.new_dependencies": 2.0}},
		{Kind: "attr", ID: 7, Fields: map[string]any{"pr.lines_changed": 41.0}},
		{Kind: "attr", ID: 7, Fields: map[string]any{"pr.draft": false}},
	}
	got := runActionSrc(t, src, facts)["Deps"].Actions
	if len(got) != 1 {
		t.Fatalf("want 1 action, got %#v", got)
	}
	want := []any{"pr", "2 new deps, 41 lines, draft false"}
	if !reflect.DeepEqual(got[0].Args, want) {
		t.Fatalf("args: got %#v, want %#v", got[0].Args, want)
	}
}

func TestFireActions_UnsetDottedAttrTemplateRefStaysVisible(t *testing.T) {
	src := `
rule "Owner" {
  for records where type == "pr" and attr "risk" == "low"
  do comment "pr" "owned by {attr.user.owner}, ctx {context.user.role}"
}
`
	facts := []ast.TestDatum{
		{Kind: "record", ID: 7, Fields: map[string]any{"type": "pr"}},
		{Kind: "attr", ID: 7, Fields: map[string]any{"risk": "low"}},
	}
	got := runActionSrc(t, src, facts)["Owner"].Actions
	if len(got) != 1 {
		t.Fatalf("want 1 action, got %#v", got)
	}
	want := []any{"pr", "owned by {attr.user.owner}, ctx {context.user.role}"}
	if !reflect.DeepEqual(got[0].Args, want) {
		t.Fatalf("args: got %#v, want %#v", got[0].Args, want)
	}
}
