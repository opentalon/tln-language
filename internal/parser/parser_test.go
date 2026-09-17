package parser

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/lexer"
)

func mustParse(t *testing.T, src string) *ast.Program {
	t.Helper()
	tokens, ld := lexer.Lex("test.tln", src)
	if ld.HasErrors() {
		t.Fatalf("lex errors: %v", ld)
	}
	prog, pd := Parse("test.tln", tokens)
	if pd.HasErrors() {
		t.Fatalf("parse errors: %v", pd)
	}
	return prog
}

func block[T ast.Block](t *testing.T, prog *ast.Program, i int) T {
	t.Helper()
	if i >= len(prog.Blocks) {
		t.Fatalf("block[%d]: out of range (len=%d)", i, len(prog.Blocks))
	}
	b, ok := prog.Blocks[i].(T)
	if !ok {
		t.Fatalf("block[%d]: wrong type, got %T", i, prog.Blocks[i])
	}
	return b
}

// ─── Empty ─────────────────────────────────────────────────────────────────────

func TestParseEmpty(t *testing.T) {
	prog := mustParse(t, "")
	if len(prog.Blocks) != 0 {
		t.Fatalf("empty source: expected 0 blocks, got %d", len(prog.Blocks))
	}
}

// ─── detect ────────────────────────────────────────────────────────────────────

func TestParseDetectMinimal(t *testing.T) {
	prog := mustParse(t, `
detect "Cement running low" {
  for records where type == "stock_item"
  flag matching items
  label "{item.name}: low"
  priority CRITICAL
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Name != "Cement running low" {
		t.Errorf("Name: got %q", b.Name)
	}
	if b.Flag == nil || b.Flag.Kind != "items" {
		t.Errorf("Flag: got %v", b.Flag)
	}
	if b.Label == nil || b.Label.Raw != "{item.name}: low" {
		t.Errorf("Label: got %v", b.Label)
	}
	if b.Priority == nil || *b.Priority != ast.PriorityCritical {
		t.Errorf("Priority: got %v", b.Priority)
	}
}

func TestParseDetectSelectorConditions(t *testing.T) {
	prog := mustParse(t, `
detect "Multi-cond" {
  for records where type == "item"
    and status == "active"
    and attr "km" > 20000
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	// Selector should have a LogicalCondition tree
	if b.Selector.Target != "records" {
		t.Errorf("Selector.Target: got %q", b.Selector.Target)
	}
	if len(b.Selector.Conditions) == 0 {
		t.Error("Selector.Conditions: empty")
	}
}

func TestParseDetectConfidence(t *testing.T) {
	prog := mustParse(t, `
detect "High confidence" {
  for records where type == "item"
  confidence >= 0.9
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Confidence == nil || *b.Confidence != 0.9 {
		t.Errorf("Confidence: got %v", b.Confidence)
	}
}

func TestParseDetectAnomalyClause(t *testing.T) {
	prog := mustParse(t, `
detect "Unusual consumption" {
  for records where type == "stock_item"
  is anomaly compared_to last 12 weeks
  flag matching items
  priority HIGH
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Anomaly == nil {
		t.Fatal("Anomaly: nil")
	}
	if b.Anomaly.Window.Value != 12 || b.Anomaly.Window.Unit != "weeks" {
		t.Errorf("Anomaly.Window: got %+v", b.Anomaly.Window)
	}
}

// ─── rule ──────────────────────────────────────────────────────────────────────

func TestParseRuleWithBlock(t *testing.T) {
	prog := mustParse(t, `
rule "No assignment during maintenance" {
  for records where type == "item"
  block "assign"
  reason "Item has open maintenance ticket"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if b.Name != "No assignment during maintenance" {
		t.Errorf("Name: got %q", b.Name)
	}
	if b.Block == nil || *b.Block != "assign" {
		t.Errorf("Block: got %v", b.Block)
	}
	if b.Reason == nil || b.Reason.Raw != "Item has open maintenance ticket" {
		t.Errorf("Reason: got %v", b.Reason)
	}
}

func TestParseRuleWithWhen(t *testing.T) {
	prog := mustParse(t, `
rule "Regional data restriction" {
  when tool_action starts_with "inventory"
  block reason "You don't have access to this organisational unit"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if b.When == nil {
		t.Fatal("When: nil")
	}
	if b.Reason == nil {
		t.Fatal("Reason: nil")
	}
}

func TestParseRuleWithEvery(t *testing.T) {
	prog := mustParse(t, `
rule "Brake inspection every 20k km" {
  for records where category == "van"
  every 20000 km on attr "km"
  requires "brake_inspection"
  priority HIGH
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if b.Every == nil {
		t.Fatal("Every: nil")
	}
	if b.Every.Value != 20000 || b.Every.Unit != "km" || b.Every.OnAttr != "km" {
		t.Errorf("Every: got %+v", b.Every)
	}
	if b.Requires == nil || b.Requires.What != "brake_inspection" {
		t.Errorf("Requires: got %v", b.Requires)
	}
}

func TestParseRuleWithApproval(t *testing.T) {
	prog := mustParse(t, `
rule "Manager approval for high value" {
  for records where is "high_value"
  before "status_change"
  requires approval from role "manager"
  reason "Items over 10,000 require manager approval"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if b.Before == nil || *b.Before != "status_change" {
		t.Errorf("Before: got %v", b.Before)
	}
	if b.Requires == nil || b.Requires.Approval == nil || b.Requires.Approval.Role != "manager" {
		t.Errorf("Requires.Approval: got %v", b.Requires)
	}
}

// ─── recommend ─────────────────────────────────────────────────────────────────

func TestParseRecommend(t *testing.T) {
	prog := mustParse(t, `
recommend "Order cement" {
  when detect "Cement running low" matches
  suggest "Order more cement"
  priority HIGH
}`)
	b := block[*ast.RecommendBlock](t, prog, 0)
	if b.Name != "Order cement" {
		t.Errorf("Name: got %q", b.Name)
	}
	bmc, ok := b.When.(*ast.BlockMatchesCondition)
	if !ok {
		t.Fatalf("When: expected BlockMatchesCondition, got %T", b.When)
	}
	if bmc.Kind != "detect" || bmc.Name != "Cement running low" {
		t.Errorf("BlockMatchesCondition: got %+v", bmc)
	}
	if b.Suggest == nil || b.Suggest.Raw != "Order more cement" {
		t.Errorf("Suggest: got %v", b.Suggest)
	}
}

func TestParseRecommendWithCalculate(t *testing.T) {
	prog := mustParse(t, `
recommend "Order cement" {
  when detect "Cement running low" matches
  calculate avg_weekly from activities within last 30 days
  suggest "Order {avg_weekly * 4} bags"
}`)
	b := block[*ast.RecommendBlock](t, prog, 0)
	if len(b.Calculate) != 1 {
		t.Fatalf("Calculate: expected 1, got %d", len(b.Calculate))
	}
	calc := b.Calculate[0]
	if calc.Name != "avg_weekly" || calc.From != "activities" {
		t.Errorf("Calculate: got %+v", calc)
	}
	if calc.Within == nil || calc.Within.Value != 30 || calc.Within.Unit != "days" {
		t.Errorf("Calculate.Within: got %v", calc.Within)
	}
}

// ─── define ────────────────────────────────────────────────────────────────────

func TestParseDefine(t *testing.T) {
	prog := mustParse(t, `
define "high_value" {
  attr "price" > 10000
}`)
	b := block[*ast.DefineBlock](t, prog, 0)
	if b.Name != "high_value" {
		t.Errorf("Name: got %q", b.Name)
	}
	if len(b.Conditions) != 1 {
		t.Errorf("Conditions: expected 1, got %d", len(b.Conditions))
	}
}

func TestParseDefineWithParams(t *testing.T) {
	prog := mustParse(t, `
define "overdue" {
  attr "last_service_date" older_than 90 days
}`)
	b := block[*ast.DefineBlock](t, prog, 0)
	if len(b.Conditions) != 1 {
		t.Errorf("Conditions: expected 1, got %d", len(b.Conditions))
	}
	_, ok := b.Conditions[0].(*ast.TemporalCondition)
	if !ok {
		t.Errorf("Condition: expected TemporalCondition, got %T", b.Conditions[0])
	}
}

// ─── workflow ──────────────────────────────────────────────────────────────────

func TestParseWorkflow(t *testing.T) {
	prog := mustParse(t, `
workflow "Onboard new team member" {
  step "create_person" {
    tool "hr" "create-person" {
      first_name context.first_name
    }
  }
  step "assign_equipment" depends_on "create_person" {
    tool "inventory" "assign-item" {
      person_id step("create_person").result.id
    }
  }
}`)
	b := block[*ast.WorkflowBlock](t, prog, 0)
	if b.Name != "Onboard new team member" {
		t.Errorf("Name: got %q", b.Name)
	}
	if len(b.Steps) != 2 {
		t.Fatalf("Steps: expected 2, got %d", len(b.Steps))
	}
	if b.Steps[0].Name != "create_person" {
		t.Errorf("Step[0].Name: got %q", b.Steps[0].Name)
	}
	if b.Steps[1].Name != "assign_equipment" {
		t.Errorf("Step[1].Name: got %q", b.Steps[1].Name)
	}
	if len(b.Steps[1].DependsOn) != 1 || b.Steps[1].DependsOn[0] != "create_person" {
		t.Errorf("Step[1].DependsOn: got %v", b.Steps[1].DependsOn)
	}
}

func TestParseWorkflowMapExpr(t *testing.T) {
	prog := mustParse(t, `
workflow "Delete items" {
  step "find" {
    tool "srv" "list" {
      query "test"
      collect_all true
    }
  }
  step "delete" depends_on "find" {
    tool "srv" "batch-delete" {
      ids step("find").result.items.map(id)
    }
  }
}`)
	b := block[*ast.WorkflowBlock](t, prog, 0)
	if len(b.Steps) != 2 {
		t.Fatalf("Steps: expected 2, got %d", len(b.Steps))
	}
	// Verify collect_all arg on step 0
	ca, ok := b.Steps[0].MCPCall.Args["collect_all"]
	if !ok {
		t.Fatal("missing collect_all arg")
	}
	if lit, ok := ca.(*ast.LiteralExpr); !ok || lit.Value != true {
		t.Errorf("collect_all: got %v", ca)
	}
	// Verify .map(id) produces a MapExpr
	ids := b.Steps[1].MCPCall.Args["ids"]
	me, ok := ids.(*ast.MapExpr)
	if !ok {
		t.Fatalf("ids arg: expected MapExpr, got %T", ids)
	}
	if me.Field != "id" {
		t.Errorf("MapExpr.Field: got %q", me.Field)
	}
	src, ok := me.Source.(*ast.StepResultExpr)
	if !ok {
		t.Fatalf("MapExpr.Source: expected StepResultExpr, got %T", me.Source)
	}
	if src.StepName != "find" || src.Field != "result.items" {
		t.Errorf("StepResultExpr: got step=%q field=%q", src.StepName, src.Field)
	}
}

func TestParseWorkflowStepWhenGuard(t *testing.T) {
	prog := mustParse(t, `
workflow "dedup" {
  step "search" {
    tool "srv" "list-tickets" { query "item_ids:42" }
  }
  step "create" depends_on "search" {
    when length(step("search").tickets) == 0
    tool "srv" "create-ticket" { title "Repair" }
  }
}`)
	b := block[*ast.WorkflowBlock](t, prog, 0)
	if b.Steps[0].When != nil {
		t.Errorf("step 0 should have no guard, got %#v", b.Steps[0].When)
	}
	cmp, ok := b.Steps[1].When.(*ast.CompareCondition)
	if !ok {
		t.Fatalf("step 1 When: expected CompareCondition, got %T", b.Steps[1].When)
	}
	if cmp.Op != "==" {
		t.Errorf("guard op: got %q", cmp.Op)
	}
	call, ok := cmp.Left.(*ast.CallExpr)
	if !ok || call.Func != "length" {
		t.Fatalf("guard lhs: expected length(...) CallExpr, got %#v", cmp.Left)
	}
	sr, ok := call.Args[0].(*ast.StepResultExpr)
	if !ok || sr.StepName != "search" || sr.Field != "tickets" {
		t.Errorf("guard operand: got %#v", call.Args[0])
	}
}

// ─── top-level ML blocks ───────────────────────────────────────────────────────

func TestParseForecastBlock(t *testing.T) {
	prog := mustParse(t, `
forecast "Cement stock-out date" {
  for records where type == "stock_item"
  series attr "current_stock" over last 30 days
  label "{item.name}: hits zero in ~{days_until} days"
  priority CRITICAL
}`)
	b := block[*ast.ForecastBlock](t, prog, 0)
	if b.Name != "Cement stock-out date" {
		t.Errorf("Name: got %q", b.Name)
	}
	if b.Series.Window.Value != 30 || b.Series.Window.Unit != "days" {
		t.Errorf("Series.Window: got %+v", b.Series.Window)
	}
}

func TestParsePredictBlock(t *testing.T) {
	prog := mustParse(t, `
predict "Equipment failure risk" {
  for records where type == "item"
  features [
    attr "operating_hours",
    attr "repair_count"
  ]
  confidence >= 0.7
  label "{item.name}: failure risk"
  priority HIGH
}`)
	b := block[*ast.PredictBlock](t, prog, 0)
	if b.Name != "Equipment failure risk" {
		t.Errorf("Name: got %q", b.Name)
	}
	if len(b.Features) != 2 {
		t.Errorf("Features: expected 2, got %d", len(b.Features))
	}
	if b.Confidence == nil || *b.Confidence != 0.7 {
		t.Errorf("Confidence: got %v", b.Confidence)
	}
}

// ─── conditions ────────────────────────────────────────────────────────────────

func TestParseMembershipCondition(t *testing.T) {
	prog := mustParse(t, `
detect "In list" {
  for records where status in ["active", "pending"]
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	cond := b.Selector.Conditions[0].(*ast.MembershipCondition)
	if cond.Negated {
		t.Error("Negated: expected false")
	}
	if len(cond.Members) != 2 {
		t.Errorf("Members: expected 2, got %d", len(cond.Members))
	}
}

func TestParseNotInMembership(t *testing.T) {
	prog := mustParse(t, `
detect "Not in list" {
  for records where status not in ["closed", "archived"]
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	cond := b.Selector.Conditions[0].(*ast.MembershipCondition)
	if !cond.Negated {
		t.Error("Negated: expected true")
	}
}

func TestParseStringMatchConditions(t *testing.T) {
	prog := mustParse(t, `
rule "String match" {
  when tool_action starts_with "inventory"
  block "action"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	cond, ok := b.When.(*ast.StringMatchCondition)
	if !ok {
		t.Fatalf("When: expected StringMatchCondition, got %T", b.When)
	}
	if cond.Op != "starts_with" || cond.Value != "inventory" {
		t.Errorf("StringMatch: op=%q value=%q", cond.Op, cond.Value)
	}
}

func TestParseIsCondition(t *testing.T) {
	prog := mustParse(t, `
detect "Is high value" {
  for records where is "high_value"
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	cond, ok := b.Selector.Conditions[0].(*ast.IsCondition)
	if !ok {
		t.Fatalf("Condition: expected IsCondition, got %T", b.Selector.Conditions[0])
	}
	if cond.Name != "high_value" {
		t.Errorf("IsCondition.Name: got %q", cond.Name)
	}
}

func TestParseTemporalCondition(t *testing.T) {
	prog := mustParse(t, `
detect "Overdue" {
  for records where attr "last_service_date" older_than 90 days
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	cond, ok := b.Selector.Conditions[0].(*ast.TemporalCondition)
	if !ok {
		t.Fatalf("Condition: expected TemporalCondition, got %T", b.Selector.Conditions[0])
	}
	if cond.Op != "older_than" || cond.Value.Value != 90 || cond.Value.Unit != "days" {
		t.Errorf("TemporalCondition: %+v", cond)
	}
}

func TestParseAnomalyCondition(t *testing.T) {
	prog := mustParse(t, `
detect "Unusual" {
  for records where attr "weekly_consumption" is anomaly compared_to last 12 weeks
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	cond, ok := b.Selector.Conditions[0].(*ast.AnomalyCondition)
	if !ok {
		t.Fatalf("Condition: expected AnomalyCondition, got %T", b.Selector.Conditions[0])
	}
	if cond.Window.Value != 12 || cond.Window.Unit != "weeks" {
		t.Errorf("AnomalyCondition.Window: %+v", cond.Window)
	}
}

func TestParseChangedToCondition(t *testing.T) {
	prog := mustParse(t, `
predict "Failure" {
  for records where status changed_to "defective"
  features [attr "age"]
  label "risk"
}`)
	b := block[*ast.PredictBlock](t, prog, 0)
	cond, ok := b.Selector.Conditions[0].(*ast.ChangedToCondition)
	if !ok {
		t.Fatalf("Condition: expected ChangedToCondition, got %T", b.Selector.Conditions[0])
	}
	if cond.Attribute != "status" {
		t.Errorf("Attribute: got %q", cond.Attribute)
	}
}

func TestParseAndOrChaining(t *testing.T) {
	prog := mustParse(t, `
detect "Chained" {
  for records where type == "item"
    and status == "active"
    and attr "km" > 1000
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	// Root should be a LogicalCondition (and)
	_, ok := b.Selector.Conditions[0].(*ast.LogicalCondition)
	if !ok {
		t.Fatalf("Expected LogicalCondition root, got %T", b.Selector.Conditions[0])
	}
}

func TestParseNotCondition(t *testing.T) {
	prog := mustParse(t, `
detect "Not active" {
  for records where not status == "inactive"
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	_, ok := b.Selector.Conditions[0].(*ast.NotCondition)
	if !ok {
		t.Fatalf("Expected NotCondition, got %T", b.Selector.Conditions[0])
	}
}

// ─── Error recovery ────────────────────────────────────────────────────────────

func TestParseErrorRecovery(t *testing.T) {
	// Bad block, followed by valid block — must recover and parse the second
	tokens, _ := lexer.Lex("test.tln", `
detect "Bad block" {
  @@@ invalid syntax @@@
}
detect "Good block" {
  for records where status == "ok"
  flag matching items
}`)
	prog, diags := Parse("test.tln", tokens)
	if !diags.HasErrors() {
		t.Fatal("expected parse errors for bad syntax")
	}
	// Should still have parsed the good block
	var found bool
	for _, b := range prog.Blocks {
		if db, ok := b.(*ast.DetectBlock); ok && db.Name == "Good block" {
			found = true
		}
	}
	if !found {
		t.Error("error recovery failed: 'Good block' not in AST")
	}
}

// ─── Multiple blocks ───────────────────────────────────────────────────────────

func TestParseMultipleBlocks(t *testing.T) {
	prog := mustParse(t, `
detect "D1" {
  for records where status == "active"
  flag matching items
}
rule "R1" {
  for records where status == "pending"
  block "action"
}
define "helper" {
  attr "price" > 100
}`)
	if len(prog.Blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(prog.Blocks))
	}
	block[*ast.DetectBlock](t, prog, 0)
	block[*ast.RuleBlock](t, prog, 1)
	block[*ast.DefineBlock](t, prog, 2)
}

// ─── test-block ML assertions ─────────────────────────────────────────────────

func TestParseTestAssertionsScoreAndThreshold(t *testing.T) {
	prog := mustParse(t, `
test "anomaly trace" {
  given {
    record 1 type "stock_item" status "active"
    attr 1 "weekly_consumption" 50
  }
  when detect "Unusual consumption"
  expect {
    flagged 1
    score 1 > 2.5
    threshold ~= 95
    threshold >= 90
  }
}`)
	if len(prog.Blocks) != 1 {
		t.Fatalf("blocks: %d", len(prog.Blocks))
	}
	tb, ok := prog.Blocks[0].(*ast.TestBlock)
	if !ok {
		t.Fatalf("block type %T", prog.Blocks[0])
	}
	if len(tb.Expect) != 4 {
		t.Fatalf("want 4 assertions, got %d", len(tb.Expect))
	}
	score := tb.Expect[1]
	if score.Kind != "score" || score.ID != 1 || score.Op != ">" || score.Value != "2.5" {
		t.Errorf("score: %+v", score)
	}
	thr := tb.Expect[2]
	if thr.Kind != "threshold" || thr.Op != "~=" || thr.Value != "95" {
		t.Errorf("threshold ~=: %+v", thr)
	}
	thr2 := tb.Expect[3]
	if thr2.Kind != "threshold" || thr2.Op != ">=" || thr2.Value != "90" {
		t.Errorf("threshold >=: %+v", thr2)
	}
}

// ─── learned_threshold expression ─────────────────────────────────────────────

func TestParseLearnedThresholdInCompare(t *testing.T) {
	prog := mustParse(t, `
detect "High mileage" {
  for records where type == "item"
    and attr "km" > learned_threshold p95 of attr "km" over last 90 days
  flag matching items
  priority HIGH
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if len(b.Selector.Conditions) != 1 {
		t.Fatalf("want 1 selector cond, got %d", len(b.Selector.Conditions))
	}
	log, ok := b.Selector.Conditions[0].(*ast.LogicalCondition)
	if !ok {
		t.Fatalf("want LogicalCondition, got %T", b.Selector.Conditions[0])
	}
	cmp, ok := log.Right.(*ast.CompareCondition)
	if !ok {
		t.Fatalf("want CompareCondition on right, got %T", log.Right)
	}
	if cmp.Op != ">" {
		t.Errorf("op: got %q", cmp.Op)
	}
	lt, ok := cmp.Right.(*ast.LearnedThresholdExpr)
	if !ok {
		t.Fatalf("want LearnedThresholdExpr, got %T", cmp.Right)
	}
	if lt.Method != "p95" {
		t.Errorf("method: got %q", lt.Method)
	}
	sub, ok := lt.Subject.(*ast.AttrExpr)
	if !ok || sub.Name != "km" {
		t.Errorf("subject: got %+v", lt.Subject)
	}
	if lt.Window.Value != 90 || lt.Window.Unit != "days" {
		t.Errorf("window: got %+v", lt.Window)
	}
}

// ─── Nested recommend inside detect ───────────────────────────────────────────

func TestParseNestedRecommend(t *testing.T) {
	prog := mustParse(t, `
detect "Low stock" {
  for records where type == "stock_item"
  flag matching items
  priority HIGH
  recommend "Reorder" {
    when detect "Low stock" matches
    suggest "Place reorder"
  }
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Recommend == nil {
		t.Fatal("Recommend: nil")
	}
	if b.Recommend.Name != "Reorder" {
		t.Errorf("Recommend.Name: got %q", b.Recommend.Name)
	}
}

// ─── find related (PPR) ───────────────────────────────────────────────────────

func TestParseFindRelatedBlockSeeds(t *testing.T) {
	prog := mustParse(t, `
find related "Co-consumed parts" {
  for records where type == "stock_item"
  seeds [808, 809]
  top_k 5
  damping 0.85
  label "{item.name}: ppr {score}"
  priority MEDIUM
}`)
	b := block[*ast.RelatedBlock](t, prog, 0)
	if b.Name != "Co-consumed parts" {
		t.Errorf("Name: got %q", b.Name)
	}
	if len(b.Seeds) != 2 {
		t.Errorf("Seeds: got %d, want 2", len(b.Seeds))
	}
	if b.TopK == nil || *b.TopK != 5 {
		t.Errorf("TopK: got %v", b.TopK)
	}
	if b.Damping == nil || *b.Damping != 0.85 {
		t.Errorf("Damping: got %v", b.Damping)
	}
	if b.Priority == nil || *b.Priority != ast.PriorityMedium {
		t.Errorf("Priority: got %v", b.Priority)
	}
}

func TestParseFindRelatedBlockSingleTo(t *testing.T) {
	prog := mustParse(t, `
find related "Single seed" {
  for records where type == "item"
  to attr "id"
  top_k 3
  tolerance 0.0001
  max_iterations 50
}`)
	b := block[*ast.RelatedBlock](t, prog, 0)
	if b.To == nil {
		t.Fatal("To: nil")
	}
	if b.Tol == nil || *b.Tol != 0.0001 {
		t.Errorf("Tol: got %v", b.Tol)
	}
	if b.MaxIter == nil || *b.MaxIter != 50 {
		t.Errorf("MaxIter: got %v", b.MaxIter)
	}
}

func TestParseFindRelatedClauseInDetect(t *testing.T) {
	prog := mustParse(t, `
detect "Investigate related parts" {
  for records where type == "stock_item"
  flag matching items
  find related to attr "id" top_k 5 damping 0.85
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Related == nil {
		t.Fatal("Related clause: nil")
	}
	if b.Related.To == nil {
		t.Fatal("Related.To: nil")
	}
	if b.Related.TopK == nil || *b.Related.TopK != 5 {
		t.Errorf("TopK: got %v", b.Related.TopK)
	}
}

func TestParseFindSimilarStillWorks(t *testing.T) {
	// Make sure adding `find related` did not break the existing
	// `find similar` parser dispatch.
	prog := mustParse(t, `
find similar "Sim" {
  for records where type == "item"
  to attr "id"
  within 0.2
}`)
	b := block[*ast.SimilarBlock](t, prog, 0)
	if b.To == nil || b.Within == nil || *b.Within != 0.2 {
		t.Errorf("similar block parse regressed: %+v", b)
	}
}

// ─── defeasible (strict + overrides) ──────────────────────────────────────────

func TestParseStrictRule(t *testing.T) {
	prog := mustParse(t, `
strict rule "Expired cert blocks assignment" {
  for records where type == "person"
  block "assign"
  reason "Safety certification expired"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if !b.Strict {
		t.Error("expected Strict = true")
	}
}

func TestParseRuleOverrides(t *testing.T) {
	prog := mustParse(t, `
rule "Cleanup crew can delete" {
  when tool_action contains "delete"
  overrides "Block all deletions"
  allow "delete"
  priority HIGH
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if len(b.Overrides) != 1 || b.Overrides[0] != "Block all deletions" {
		t.Errorf("Overrides: got %v", b.Overrides)
	}
}

func TestParseRuleMultipleOverrides(t *testing.T) {
	prog := mustParse(t, `
rule "Mega override" {
  when tool_action contains "x"
  overrides "A", "B", "C"
  block "x"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if len(b.Overrides) != 3 || b.Overrides[2] != "C" {
		t.Errorf("Overrides: got %v", b.Overrides)
	}
}

// ─── reactive (on change/assert/retract) ──────────────────────────────────────

func TestParseOnChange(t *testing.T) {
	prog := mustParse(t, `
on change attr "current_stock" {
  logger.warn "stock changed: {item.name}"
}`)
	b := block[*ast.OnBlock](t, prog, 0)
	if b.Trigger != "change" || b.Attr != "current_stock" {
		t.Errorf("Trigger/Attr: got %s / %s", b.Trigger, b.Attr)
	}
	if len(b.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(b.Actions))
	}
	la, ok := b.Actions[0].(*ast.LoggerAction)
	if !ok {
		t.Fatalf("expected LoggerAction, got %T", b.Actions[0])
	}
	if la.Level != "warn" {
		t.Errorf("Level: got %s", la.Level)
	}
}

func TestParseOnAssert(t *testing.T) {
	prog := mustParse(t, `
on assert activity {
  detect "Defective item without ticket"
}`)
	b := block[*ast.OnBlock](t, prog, 0)
	if b.Trigger != "assert" || b.FactType != "activity" {
		t.Errorf("Trigger/FactType: got %s / %s", b.Trigger, b.FactType)
	}
	ref, ok := b.Actions[0].(*ast.BlockRefAction)
	if !ok || ref.Kind != "detect" || ref.Name != "Defective item without ticket" {
		t.Errorf("BlockRefAction: got %+v", b.Actions[0])
	}
}

func TestParseOnRetract(t *testing.T) {
	prog := mustParse(t, `
on retract item {
  logger.info "item removed: {item.id}"
}`)
	b := block[*ast.OnBlock](t, prog, 0)
	if b.Trigger != "retract" || b.FactType != "item" {
		t.Errorf("Trigger/FactType: got %s / %s", b.Trigger, b.FactType)
	}
}

func TestParseOnChangeWorkflowAction(t *testing.T) {
	prog := mustParse(t, `
on change attr "current_stock" to 0 {
  logger.warn "stock-out: {item.name}"
  workflow "Refill stock"
}`)
	b := block[*ast.OnBlock](t, prog, 0)
	if len(b.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(b.Actions))
	}
	ref, ok := b.Actions[1].(*ast.BlockRefAction)
	if !ok || ref.Kind != "workflow" || ref.Name != "Refill stock" {
		t.Errorf("BlockRefAction: got %+v", b.Actions[1])
	}
}

// ─── constraint ───────────────────────────────────────────────────────────────

func TestParseConstraint(t *testing.T) {
	prog := mustParse(t, `
constraint "Stock cannot be negative" {
  for records where type == "stock_item"
  require attr "current_stock" >= 0
  on_violation reject "stock must be non-negative"
}`)
	b := block[*ast.ConstraintBlock](t, prog, 0)
	if b.Name != "Stock cannot be negative" {
		t.Errorf("Name: got %q", b.Name)
	}
	if b.Require == nil {
		t.Fatal("Require: nil")
	}
	if b.OnViolation.Mode != "reject" {
		t.Errorf("Mode: got %q", b.OnViolation.Mode)
	}
	if b.OnViolation.Message != "stock must be non-negative" {
		t.Errorf("Message: got %q", b.OnViolation.Message)
	}
}

func TestParseConstraintQuarantine(t *testing.T) {
	prog := mustParse(t, `
constraint "Suspicious dates" {
  for records where type == "activity"
  require attr "date" >= 0
  on_violation quarantine "needs review"
}`)
	b := block[*ast.ConstraintBlock](t, prog, 0)
	if b.OnViolation.Mode != "quarantine" {
		t.Errorf("Mode: got %q", b.OnViolation.Mode)
	}
}

// ─── Provenance annotations (#3) ──────────────────────────────────────────────

func TestParseDetectScoreAndSource(t *testing.T) {
	prog := mustParse(t, `
detect "auto-discovered: External Workshop correlation" {
  for records where category == "van"
  flag matching items
  confidence 0.82
  source "mined from 14 months of data, 47 matching cases"
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Score == nil || *b.Score != 0.82 {
		t.Errorf("Score: got %v, want 0.82", b.Score)
	}
	if b.Source == nil || *b.Source != "mined from 14 months of data, 47 matching cases" {
		t.Errorf("Source: got %v", b.Source)
	}
	// The ML filter form is a separate field and should remain nil.
	if b.Confidence != nil {
		t.Errorf("Confidence (ML filter): unexpectedly set to %v", *b.Confidence)
	}
}

func TestParseDetectConfidenceFilterStillWorks(t *testing.T) {
	// Regression: adding the bare-NUMBER annotation form must not break
	// the existing `>=` filter form on ML-style detect blocks.
	prog := mustParse(t, `
detect "High confidence ML" {
  for records where type == "item"
  flag matching items
  confidence >= 0.9
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Confidence == nil || *b.Confidence != 0.9 {
		t.Errorf("Confidence filter: got %v", b.Confidence)
	}
	if b.Score != nil {
		t.Errorf("Score annotation: unexpectedly set to %v", *b.Score)
	}
}

func TestParseRuleScoreAndSource(t *testing.T) {
	prog := mustParse(t, `
rule "Auto: block delete on stale records" {
  when tool_action contains "delete"
  block "delete"
  confidence 0.65
  source "discovered from 90 days of audit logs"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if b.Score == nil || *b.Score != 0.65 {
		t.Errorf("Score: got %v", b.Score)
	}
	if b.Source == nil || *b.Source != "discovered from 90 days of audit logs" {
		t.Errorf("Source: got %v", b.Source)
	}
}

func TestParseRuleRejectsConfidenceFilter(t *testing.T) {
	// On a rule, only the bare `confidence N` provenance form is valid.
	// `confidence >= N` is the ML filter and must be rejected with a
	// clean diagnostic so users learn the correct shape.
	tokens, ld := lexer.Lex("t.tln", `
rule "Bad" {
  when tool_action contains "x"
  block "x"
  confidence >= 0.9
}`)
	if ld.HasErrors() {
		t.Fatalf("lex: %v", ld)
	}
	_, pd := Parse("t.tln", tokens)
	if !pd.HasErrors() {
		t.Fatal("expected a parse error for `confidence >= N` inside rule")
	}
	found := false
	for _, d := range pd {
		if strings.Contains(d.Message, "ML filter") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ML-filter diagnostic, got:\n%v", pd)
	}
}

// ─── Logger statements in detect/rule/recommend (#19) ────────────────────────

func TestParseDetectWithLoggerStatements(t *testing.T) {
	prog := mustParse(t, `
detect "Watch items" {
  for records where type == "item"
  flag matching items
  logger.info "matched: {item.name}"
  logger.warn "{count} items overdue"
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if len(b.Loggers) != 2 {
		t.Fatalf("want 2 logger statements, got %d", len(b.Loggers))
	}
	if b.Loggers[0].Level != "info" || b.Loggers[1].Level != "warn" {
		t.Errorf("levels: %v %v", b.Loggers[0].Level, b.Loggers[1].Level)
	}
	if b.Loggers[0].Message.Raw != "matched: {item.name}" {
		t.Errorf("message: %q", b.Loggers[0].Message.Raw)
	}
	// Templates pre-parse Nodes so {item.name} interpolation works at render time.
	if len(b.Loggers[0].Message.Nodes) == 0 {
		t.Error("logger template Nodes not populated by parser")
	}
}

func TestParseRuleWithLoggerStatement(t *testing.T) {
	prog := mustParse(t, `
rule "Audit deletes" {
  when tool_action contains "delete"
  block "delete"
  logger.warn "blocked delete attempt from {context.role}"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if len(b.Loggers) != 1 || b.Loggers[0].Level != "warn" {
		t.Fatalf("want one warn logger, got %v", b.Loggers)
	}
}

func TestParseRecommendWithLoggerStatement(t *testing.T) {
	prog := mustParse(t, `
recommend "Order more" {
  when detect "Low stock" matches
  suggest "order {item.name}"
  logger.info "recommendation fired for {item.name}"
}`)
	b := block[*ast.RecommendBlock](t, prog, 0)
	if len(b.Loggers) != 1 {
		t.Fatalf("want one logger, got %v", b.Loggers)
	}
}

func TestParseLoggerRejectsUnknownLevel(t *testing.T) {
	tokens, _ := lexer.Lex("t.tln", `
detect "Bad" {
  for records where type == "x"
  flag matching items
  logger.shout "loud"
}`)
	_, pd := Parse("t.tln", tokens)
	if !pd.HasErrors() {
		t.Fatal("expected error for unknown log level")
	}
	found := false
	for _, d := range pd {
		if strings.Contains(d.Message, "logger level") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected logger-level diagnostic, got %v", pd)
	}
}

func TestParseDetectRemediate(t *testing.T) {
	prog := mustParse(t, `
detect "Defective without ticket" {
  for records where status == "defective"
  flag matching items
  remediate {
    tool "inventory" "create-ticket" {
      title "Auto: {item.name}"
      item_id attr "id"
    }
    tool "slack" "notify" { text "defective item" }
  }
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Remediate == nil {
		t.Fatal("Remediate clause not parsed")
	}
	calls := remediateCalls(b.Remediate.Body)
	if len(calls) != 2 {
		t.Fatalf("expected 2 mcp calls, got %d", len(calls))
	}
	first := calls[0]
	if first.Server != "inventory" || first.Tool != "create-ticket" {
		t.Errorf("first call target: got %s/%s", first.Server, first.Tool)
	}
	if _, ok := first.Args["title"]; !ok {
		t.Errorf("first call missing title arg: %+v", first.Args)
	}
}

func TestParseRecommendRemediate(t *testing.T) {
	prog := mustParse(t, `
recommend "Order more" {
  when detect "Low stock" matches
  suggest "Order now"
  remediate {
    tool "inventory" "create-order" { quantity 50 }
  }
}`)
	b := block[*ast.RecommendBlock](t, prog, 0)
	if b.Remediate == nil || len(remediateCalls(b.Remediate.Body)) != 1 {
		t.Fatalf("recommend remediate not parsed: %+v", b.Remediate)
	}
}

// TestParseModelAndModule: a `module` namespaces an exported `model` block
// with inline fitted params, and a classify block references it.
func TestParseModelAndModule(t *testing.T) {
	prog := mustParse(t, `
module "fleet.ml" {
  export model "failure_risk" {
    classify knn k 3
    features [attr "km", attr "age"]
    fitted {
      example [50000, 8] label "high"
      example [10000, 2] label "low"
    }
    computed_from "1204 vehicles"
    valid_until "2026-12-31"
  }
}
classify "Risk" {
  for records where type == "vehicle"
  using model "fleet.ml.failure_risk"
}`)
	mod, ok := prog.Blocks[0].(*ast.ModuleBlock)
	if !ok || mod.Namespace != "fleet.ml" || len(mod.Members) != 1 {
		t.Fatalf("module not parsed: %#v", prog.Blocks[0])
	}
	if mod.QualifiedName("failure_risk") != "fleet.ml.failure_risk" {
		t.Fatalf("qualified name: %q", mod.QualifiedName("failure_risk"))
	}
	model, ok := mod.Members[0].(*ast.ModelBlock)
	if !ok || model.Algo != "classify_knn" || model.K != 3 {
		t.Fatalf("model not parsed: %#v", mod.Members[0])
	}
	if len(model.Examples) != 2 || model.Examples[0].Label != "high" ||
		len(model.Examples[0].Features) != 2 || model.Examples[0].Features[0] != 50000 {
		t.Fatalf("fitted examples: %#v", model.Examples)
	}
	cls, ok := prog.Blocks[1].(*ast.ClassifyBlock)
	if !ok || cls.UsingModel != "fleet.ml.failure_risk" {
		t.Fatalf("classify using model: %#v", prog.Blocks[1])
	}
}

// TestParseStringBuiltinCall: a builtin function in expression position
// parses as a CallExpr, and a same-shaped non-builtin stays a predicate call.
func TestParseStringBuiltinCall(t *testing.T) {
	prog := mustParse(t, `
detect "T" {
  for records where type == "vehicle"
    and upper(substring(attr "vin", 0, 3)) == "1FT"
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	// selector: type == "vehicle" AND <compare with a CallExpr on the left>
	top, ok := b.Selector.Conditions[0].(*ast.LogicalCondition)
	if !ok {
		t.Fatalf("selector should be a logical AND, got %T", b.Selector.Conditions[0])
	}
	cmp, ok := top.Right.(*ast.CompareCondition)
	if !ok {
		t.Fatalf("right conjunct should be a compare, got %T", top.Right)
	}
	call, ok := cmp.Left.(*ast.CallExpr)
	if !ok || call.Func != "upper" || len(call.Args) != 1 {
		t.Fatalf("left should be upper(<expr>), got %#v", cmp.Left)
	}
	if inner, ok := call.Args[0].(*ast.CallExpr); !ok || inner.Func != "substring" || len(inner.Args) != 3 {
		t.Fatalf("nested arg should be substring(_,_,_), got %#v", call.Args[0])
	}
}

// TestParsePredicateCallStillWorks: a non-builtin `name(var)` in condition
// position remains a derived-predicate call, unaffected by string builtins.
func TestParsePredicateCallStillWorks(t *testing.T) {
	prog := mustParse(t, `
derive overdue(v) { for records where type == "vehicle" }
detect "D" { for records where overdue(x) }`)
	b := block[*ast.DetectBlock](t, prog, 1)
	if _, ok := b.Selector.Conditions[0].(*ast.PredicateCallCondition); !ok {
		t.Fatalf("expected a predicate call, got %T", b.Selector.Conditions[0])
	}
}

// TestParseRemediateControlFlow covers the imperative action body: if/else
// (with else-if), for-each, and while parse into the expected AST shapes.
func TestParseRemediateControlFlow(t *testing.T) {
	prog := mustParse(t, `
detect "Escalate" {
  for records where type == "vehicle"
  flag matching items
  remediate {
    if attr "priority" == "CRITICAL" {
      tool "ops" "page" { id attr "id" }
    } else if attr "priority" == "HIGH" {
      tool "ops" "ticket" { id attr "id" }
    } else {
      tool "ops" "log" { id attr "id" }
    }
    for each channel in ["a", "b"] {
      tool "slack" "notify" { channel channel }
    }
    while attr "open" == 1 {
      tool "ops" "retry" { id attr "id" }
    }
  }
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Remediate == nil || len(b.Remediate.Body) != 3 {
		t.Fatalf("expected 3 top-level actions, got %+v", b.Remediate)
	}

	// if / else-if / else
	iff, ok := b.Remediate.Body[0].(*ast.IfAction)
	if !ok {
		t.Fatalf("action 0 should be IfAction, got %T", b.Remediate.Body[0])
	}
	if len(iff.Then) != 1 || len(iff.Else) != 1 {
		t.Fatalf("if should have then + else-if chain, got then=%d else=%d", len(iff.Then), len(iff.Else))
	}
	if _, ok := iff.Else[0].(*ast.IfAction); !ok {
		t.Fatalf("else-if should nest an IfAction, got %T", iff.Else[0])
	}

	// for each
	fe, ok := b.Remediate.Body[1].(*ast.ForEachAction)
	if !ok || fe.Variable != "channel" {
		t.Fatalf("action 1 should be ForEachAction over 'channel', got %+v", b.Remediate.Body[1])
	}
	if _, ok := fe.Over.(*ast.ListExpr); !ok {
		t.Fatalf("for-each collection should be a ListExpr, got %T", fe.Over)
	}

	// while (bounded)
	wh, ok := b.Remediate.Body[2].(*ast.WhileAction)
	if !ok {
		t.Fatalf("action 2 should be WhileAction, got %T", b.Remediate.Body[2])
	}
	if wh.MaxIter != ast.DefaultWhileMaxIter {
		t.Fatalf("while should carry the default iteration cap, got %d", wh.MaxIter)
	}
}

// remediateCalls extracts the leaf MCP calls (in order) from a remediate
// action body, skipping the control-flow wrappers.
func remediateCalls(body []ast.Action) []*ast.MCPCall {
	var out []*ast.MCPCall
	for _, a := range body {
		if m, ok := a.(*ast.MCPAction); ok {
			out = append(out, m.Call)
		}
	}
	return out
}

// Regression: an MCP arg whose name collides with a tln keyword (e.g.
// `priority`) must parse, not spin the arg loop forever.
func TestParseMCPCallKeywordArgKey(t *testing.T) {
	prog := mustParse(t, `
detect "Act" {
  for records where status == "defective"
  flag matching items
  remediate {
    tool "inventory" "create-ticket" {
      priority "high"
      status "open"
      title "x"
    }
  }
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	calls := remediateCalls(b.Remediate.Body)
	if b.Remediate == nil || len(calls) != 1 {
		t.Fatalf("remediate not parsed: %+v", b.Remediate)
	}
	args := calls[0].Args
	for _, k := range []string{"priority", "status", "title"} {
		if _, ok := args[k]; !ok {
			t.Errorf("missing arg %q; got keys %v", k, args)
		}
	}
}

func TestParseEnrich(t *testing.T) {
	prog := mustParse(t, `
enrich "Refresh stock" {
  for records where type == "stock_item"
  stale_after 1 hour
  tool "inventory" "show-item" { id attr "id" }
  update attr "current_stock" from result.current_stock
  update attr "stock_assigned" from result.data.assigned
}`)
	b := block[*ast.EnrichBlock](t, prog, 0)
	if b.Call == nil || b.Call.Server != "inventory" || b.Call.Tool != "show-item" {
		t.Fatalf("mcp call: %+v", b.Call)
	}
	if b.StaleAfter.Value != 1 || b.StaleAfter.Unit != "hour" {
		t.Errorf("stale_after: %+v", b.StaleAfter)
	}
	if len(b.Updates) != 2 {
		t.Fatalf("expected 2 updates, got %+v", b.Updates)
	}
	if b.Updates[0].Attr != "current_stock" || b.Updates[0].ResultPath != "current_stock" {
		t.Errorf("update 0: %+v", b.Updates[0])
	}
	if b.Updates[1].ResultPath != "data.assigned" {
		t.Errorf("update 1 path: %q", b.Updates[1].ResultPath)
	}
}

func TestParseMCPOnError(t *testing.T) {
	prog := mustParse(t, `
workflow "W" {
  step "s" {
    tool "inv" "create" {
      title "x"
      on_error {
        retry 3 times
        then log "failed: {error}"
        then skip
      }
    }
  }
}`)
	wf := block[*ast.WorkflowBlock](t, prog, 0)
	oe := wf.Steps[0].MCPCall.OnError
	if oe == nil || len(oe.Actions) != 3 {
		t.Fatalf("on_error actions: %+v", oe)
	}
	if r, ok := oe.Actions[0].(*ast.RetryAction); !ok || r.Times != 3 {
		t.Errorf("action 0 not retry 3: %+v", oe.Actions[0])
	}
	if _, ok := oe.Actions[1].(*ast.LogErrorAction); !ok {
		t.Errorf("action 1 not log: %+v", oe.Actions[1])
	}
	if _, ok := oe.Actions[2].(*ast.SkipAction); !ok {
		t.Errorf("action 2 not skip: %+v", oe.Actions[2])
	}
}

func TestParseTestMockAndMCPCalled(t *testing.T) {
	prog := mustParse(t, `
test "t" {
  given { record 1 type "item" status "defective" }
  mock tool "inventory" "create-ticket" { returns { id 801  status "open" } }
  when detect "D"
  expect {
    flagged 1
    tool_called "inventory" "create-ticket" with {
      item_id == 501
      title contains "Drill"
    }
  }
}`)
	tb := block[*ast.TestBlock](t, prog, 0)
	if len(tb.Mocks) != 1 {
		t.Fatalf("mocks: %+v", tb.Mocks)
	}
	if tb.Mocks[0].Server != "inventory" || tb.Mocks[0].Returns["id"] != 801 || tb.Mocks[0].Returns["status"] != "open" {
		t.Errorf("mock: %+v", tb.Mocks[0])
	}
	if len(tb.MCPCalls) != 1 || len(tb.MCPCalls[0].Args) != 2 {
		t.Fatalf("tool_called: %+v", tb.MCPCalls)
	}
	if tb.MCPCalls[0].Args[0].Name != "item_id" || tb.MCPCalls[0].Args[0].Op != "==" || tb.MCPCalls[0].Args[0].Value != 501 {
		t.Errorf("arg 0: %+v", tb.MCPCalls[0].Args[0])
	}
	if tb.MCPCalls[0].Args[1].Op != "contains" {
		t.Errorf("arg 1 op: %+v", tb.MCPCalls[0].Args[1])
	}
	// flagged assertion still lands in Expect.
	if len(tb.Expect) != 1 || tb.Expect[0].Kind != "flagged" {
		t.Errorf("expect: %+v", tb.Expect)
	}
}

func TestParseCollect(t *testing.T) {
	prog := mustParse(t, `
collect "Failure training data" {
  schedule weekly
  tool "inventory" "list-items" { query "status:defective"  per_page 100 }
  store results as training_facts tag "failure_training"
}
collect "Snapshot" {
  schedule every 6 hours
  tool "inventory" "list" {}
  store results as snap
}`)
	a := block[*ast.CollectBlock](t, prog, 0)
	if a.Schedule != "weekly" || a.StoreAs != "training_facts" || a.Tag != "failure_training" {
		t.Errorf("collect A: %+v", a)
	}
	if a.Call == nil || a.Call.Server != "inventory" || a.Call.Tool != "list-items" {
		t.Errorf("collect A mcp: %+v", a.Call)
	}
	b := block[*ast.CollectBlock](t, prog, 1)
	if b.Schedule != "every 6 hours" || b.StoreAs != "snap" || b.Tag != "" {
		t.Errorf("collect B: %+v", b)
	}
}

func TestParseRemediateModes(t *testing.T) {
	prog := mustParse(t, `
detect "d" {
  for records where status == "obsolete"
  flag matching items
  remediate approve {
    requires role "manager"
    tool "inventory" "delete-item" { item_id attr "id" }
  }
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if b.Remediate == nil || b.Remediate.Mode != "approve" || b.Remediate.Role != "manager" {
		t.Fatalf("remediate approve: %+v", b.Remediate)
	}

	prog2 := mustParse(t, `
detect "q" {
  for records where status == "stale"
  flag matching items
  remediate queue { batch "weekly"  tool "inv" "update" {} }
}`)
	q := block[*ast.DetectBlock](t, prog2, 0)
	if q.Remediate.Mode != "queue" || q.Remediate.Batch != "weekly" {
		t.Errorf("remediate queue: %+v", q.Remediate)
	}

	// No mode → propose default.
	prog3 := mustParse(t, `
detect "p" {
  for records where status == "x"
  flag matching items
  remediate { tool "inv" "t" {} }
}`)
	p := block[*ast.DetectBlock](t, prog3, 0)
	if p.Remediate.Mode != "propose" {
		t.Errorf("default mode: %q", p.Remediate.Mode)
	}
}

// ─── #62 sugar: has_open / has_expired / approaching ──────────────────────────

func TestDesugarHasOpen(t *testing.T) {
	got := desugarHasOpen("maintenance_ticket")
	want := &ast.LogicalCondition{
		Op:   "and",
		Left: &ast.HasCondition{Type: "maintenance_ticket"},
		Right: &ast.CompareCondition{
			Left:  &ast.AttrExpr{Name: "maintenance_ticket.status"},
			Op:    "!=",
			Right: &ast.LiteralExpr{Value: "closed"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("has_open desugar mismatch:\n got  %#v\n want %#v", got, want)
	}
}

func TestDesugarHasExpired(t *testing.T) {
	where := &ast.CompareCondition{
		Left: &ast.AttrExpr{Name: "certification_type"}, Op: "==", Right: &ast.LiteralExpr{Value: "safety"},
	}
	inner := &ast.LogicalCondition{
		Op:   "and",
		Left: &ast.HasCondition{Type: "certification"},
		Right: &ast.CompareCondition{
			Left: &ast.AttrExpr{Name: "certification.expires_at"}, Op: "<", Right: &ast.TodayExpr{},
		},
	}
	if got := desugarHasExpired("certification", nil); !reflect.DeepEqual(got, inner) {
		t.Errorf("has_expired (no where):\n got  %#v\n want %#v", got, inner)
	}
	want := &ast.LogicalCondition{Op: "and", Left: inner, Right: where}
	if got := desugarHasExpired("certification", where); !reflect.DeepEqual(got, want) {
		t.Errorf("has_expired (with where):\n got  %#v\n want %#v", got, want)
	}
}

func TestParseApproachingDesugars(t *testing.T) {
	prog := mustParse(t, `
detect "Service soon" {
  for records where attr "next_service_date" approaching within 7 days
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if len(b.Selector.Conditions) != 1 {
		t.Fatalf("expected 1 selector condition, got %d: %#v", len(b.Selector.Conditions), b.Selector.Conditions)
	}
	want := &ast.LogicalCondition{
		Op:   "and",
		Left: &ast.CompareCondition{Left: &ast.AttrExpr{Name: "next_service_date"}, Op: ">=", Right: &ast.TodayExpr{}},
		Right: &ast.CompareCondition{
			Left: &ast.AttrExpr{Name: "next_service_date"}, Op: "<=",
			Right: &ast.BinaryExpr{Left: &ast.TodayExpr{}, Op: "+", Right: &ast.LiteralExpr{Value: ast.Duration{Value: 7, Unit: "days"}}},
		},
	}
	if !reflect.DeepEqual(b.Selector.Conditions[0], want) {
		t.Errorf("approaching desugar:\n got  %#v\n want %#v", b.Selector.Conditions[0], want)
	}
}

// ─── was ... ago (time-travel) ──────────────────────────────────────────────

func TestParseWasAgo(t *testing.T) {
	prog := mustParse(t, `
detect "Regressed machines" {
  for records where was (attr "status" == "certified") 90 days ago
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if len(b.Selector.Conditions) != 1 {
		t.Fatalf("want 1 selector condition, got %d", len(b.Selector.Conditions))
	}
	want := &ast.AsOfCondition{
		Inner: &ast.CompareCondition{
			Left:  &ast.AttrExpr{Name: "status"},
			Op:    "==",
			Right: &ast.LiteralExpr{Value: "certified"},
		},
		Delta: ast.Duration{Value: 90, Unit: "days"},
	}
	if !reflect.DeepEqual(b.Selector.Conditions[0], want) {
		t.Errorf("was...ago parse:\n got  %#v\n want %#v", b.Selector.Conditions[0], want)
	}
}

// ─── correlates_with ────────────────────────────────────────────────────────

func TestParseCorrelatesWith(t *testing.T) {
	prog := mustParse(t, `
detect "Corr" {
  for records where attr "km" correlates_with attr "failure_count" over last 90 days > 0.7
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if len(b.Selector.Conditions) != 1 {
		t.Fatalf("want 1 condition, got %d", len(b.Selector.Conditions))
	}
	cc, ok := b.Selector.Conditions[0].(*ast.CorrelationCondition)
	if !ok {
		t.Fatalf("want *ast.CorrelationCondition, got %T", b.Selector.Conditions[0])
	}
	if cc.Method != "pearson" || cc.Op != ">" || cc.Threshold != 0.7 {
		t.Errorf("got Method=%q Op=%q Threshold=%v", cc.Method, cc.Op, cc.Threshold)
	}
	if cc.Window.Value != 90 || cc.Window.Unit != "days" {
		t.Errorf("window = %+v, want {90 days}", cc.Window)
	}
	lx, _ := cc.Left.(*ast.AttrExpr)
	ly, _ := cc.Right.(*ast.AttrExpr)
	if lx == nil || lx.Name != "km" || ly == nil || ly.Name != "failure_count" {
		t.Errorf("attrs: left=%#v right=%#v", cc.Left, cc.Right)
	}
}

// ─── calculate methods + of attr + having ───────────────────────────────────

func TestParseCalculateMethodAndHaving(t *testing.T) {
	prog := mustParse(t, `
detect "C" {
  for records where type == "s"
  calculate rate from activities of attr "amount" weighted_moving_average last 7 days
  having rate > 0
  flag matching items
}`)
	b := block[*ast.DetectBlock](t, prog, 0)
	if len(b.Calculate) != 1 {
		t.Fatalf("want 1 calculate, got %d", len(b.Calculate))
	}
	c := b.Calculate[0]
	if c.Name != "rate" || c.From != "activities" || c.Method != "wma" {
		t.Errorf("calc = %+v", c)
	}
	if av, ok := c.Value.(*ast.AttrExpr); !ok || av.Name != "amount" {
		t.Errorf("value = %#v, want attr amount", c.Value)
	}
	if c.Within == nil || c.Within.Value != 7 || c.Within.Unit != "days" {
		t.Errorf("window = %+v, want {7 days}", c.Within)
	}
	if len(b.Having) != 1 {
		t.Errorf("want 1 having condition, got %d", len(b.Having))
	}
}

// ─── do clauses ────────────────────────────────────────────────────────────────

func TestParseRuleWithDoActions(t *testing.T) {
	prog := mustParse(t, `
rule "Critical path" {
  for records where type == "pr"
  requires "review.senior"
  do require "review.senior"
  do assign "pr" attr "user.owner"
  do comment "pr" "Owned by {attr.user.owner}"
  reason "touches a critical path"
  priority HIGH
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if len(b.Do) != 3 {
		t.Fatalf("Do: got %d actions, want 3", len(b.Do))
	}
	if b.Do[0].Verb != "require" || len(b.Do[0].Args) != 1 {
		t.Errorf("Do[0]: got %q with %d args", b.Do[0].Verb, len(b.Do[0].Args))
	}
	// The argument list stops at the next clause keyword, not at a newline.
	if b.Do[2].Verb != "comment" || len(b.Do[2].Args) != 2 {
		t.Errorf("Do[2]: got %q with %d args", b.Do[2].Verb, len(b.Do[2].Args))
	}
	if b.Reason == nil || b.Reason.Raw != "touches a critical path" {
		t.Errorf("Reason: got %v — the do argument list swallowed it", b.Reason)
	}
	if b.Priority == nil || *b.Priority != ast.PriorityHigh {
		t.Errorf("Priority: got %v", b.Priority)
	}
}

// `block` is both a clause keyword and a plausible verb. The verb position
// takes whatever token follows `do`, so `do block "pr.merge"` is an action and
// not an empty verb followed by a stray verdict clause.
func TestParseRuleDoBlockVerb(t *testing.T) {
	prog := mustParse(t, `
rule "Conflicts" {
  for records where type == "pr"
  do block "pr.merge"
}`)
	b := block[*ast.RuleBlock](t, prog, 0)
	if len(b.Do) != 1 || b.Do[0].Verb != "block" {
		t.Fatalf("Do: got %+v", b.Do)
	}
	if b.Block != nil {
		t.Errorf("Block clause: got %v, want nil — `do block` is not a verdict", *b.Block)
	}
}

func TestParseRuleDoRejectsQuotedVerb(t *testing.T) {
	tokens, ld := lexer.Lex("t.tln", `
rule "Quoted" {
  for records where type == "pr"
  do "approve" "pr"
}`)
	if ld.HasErrors() {
		t.Fatalf("lex: %v", ld)
	}
	_, pd := Parse("t.tln", tokens)
	if !pd.HasErrors() {
		t.Fatal("expected a parse error for a quoted action verb")
	}
	found := false
	for _, d := range pd {
		if strings.Contains(d.Message, "must not be quoted") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected the quoted-verb diagnostic, got:\n%v", pd)
	}
}

func TestParseRuleDoRequiresVerb(t *testing.T) {
	tokens, ld := lexer.Lex("t.tln", `
rule "Empty" {
  for records where type == "pr"
  do
}`)
	if ld.HasErrors() {
		t.Fatalf("lex: %v", ld)
	}
	if _, pd := Parse("t.tln", tokens); !pd.HasErrors() {
		t.Fatal("expected a parse error for `do` with no verb")
	}
}

// An argument the action resolver cannot evaluate must be rejected here rather
// than resolving to nothing at run time and handing the host a silently empty
// payload.
func TestParseRuleDoRejectsUnsupportedArgument(t *testing.T) {
	for _, src := range []string{
		`rule "R" { for records where type == "pr" do notify "chan" -1 }`,
		`rule "R" { for records where type == "pr" do dispatch ["a", "b"] }`,
		`rule "R" { for records where type == "pr" do x attr "n" + 1 }`,
	} {
		tokens, ld := lexer.Lex("t.tln", src)
		if ld.HasErrors() {
			t.Fatalf("lex %s: %v", src, ld)
		}
		if _, pd := Parse("t.tln", tokens); !pd.HasErrors() {
			t.Errorf("expected a parse error for %s", src)
		}
	}
}

// A boolean argument used to hang the parser: `true` lexes as TokenBool, which
// parseLiteralValue did not handle, so it fell through to a number parse that
// errored without consuming the token.
func TestParseActionAssertionBoolArgument(t *testing.T) {
	done := make(chan *ast.Program, 1)
	go func() {
		done <- mustParse(t, `
test "bool arg" {
  given { record 1 type "pr" }
  when rule "R"
  expect { did 1 setflag "x" true }
}`)
	}()
	select {
	case prog := <-done:
		b := block[*ast.TestBlock](t, prog, 0)
		if len(b.Actions) != 1 || len(b.Actions[0].Args) != 2 {
			t.Fatalf("Actions: got %+v", b.Actions)
		}
		if b.Actions[0].Args[1].Value != true {
			t.Errorf("arg 1: got %#v, want true", b.Actions[0].Args[1].Value)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parser hung on a boolean action-assertion argument")
	}
}

// A `did` with the verb omitted must not eat the expect block's closing brace,
// which would desync brace matching for the rest of the file.
func TestParseActionAssertionRequiresVerb(t *testing.T) {
	tokens, ld := lexer.Lex("t.tln", `
test "no verb" {
  given { record 1 type "pr" }
  when rule "R"
  expect { did 1 }
}

rule "Later" {
  for records where type == "pr"
  block "merge"
}`)
	if ld.HasErrors() {
		t.Fatalf("lex: %v", ld)
	}
	prog, pd := Parse("t.tln", tokens)
	if !pd.HasErrors() {
		t.Fatal("expected a parse error for a did assertion with no verb")
	}
	// The rule after the malformed test must still parse.
	var gotRule bool
	for _, b := range prog.Blocks {
		if r, ok := b.(*ast.RuleBlock); ok && r.Name == "Later" {
			gotRule = true
		}
	}
	if !gotRule {
		t.Errorf("the block after the malformed assertion did not parse — brace matching desynced")
	}
}

func TestParseTestListAttrAndActionAssertions(t *testing.T) {
	prog := mustParse(t, `
test "lists and actions" {
  given {
    record 1 type "pr"
    attr 1 "pr.changed_files" ["internal/auth/a.go", "README.md"]
    attr 2 "pr.changed_files" []
  }
  when rule "R"
  expect {
    flagged 1
    count == 1
    did 1 approve "pr"
    did 1 comment "pr" contains "owned by"
    did_not 2 approve "pr"
  }
}`)
	tb := block[*ast.TestBlock](t, prog, 0)
	files, ok := tb.Given[1].Fields["pr.changed_files"].([]any)
	if !ok || len(files) != 2 || files[0] != "internal/auth/a.go" {
		t.Fatalf("list attr: got %#v", tb.Given[1].Fields["pr.changed_files"])
	}
	// An empty list is a value, not an absent attribute — the two differ to
	// the evaluator and the test file has to be able to say which it means.
	empty, ok := tb.Given[2].Fields["pr.changed_files"].([]any)
	if !ok || len(empty) != 0 {
		t.Fatalf("empty list attr: got %#v", tb.Given[2].Fields["pr.changed_files"])
	}
	if len(tb.Expect) != 2 {
		t.Fatalf("Expect: got %d, want flagged + count", len(tb.Expect))
	}
	if len(tb.Actions) != 3 {
		t.Fatalf("Actions: got %d, want 3", len(tb.Actions))
	}
	if !tb.Actions[1].Args[1].Contains {
		t.Errorf("Actions[1] arg 1: want a contains matcher, got %+v", tb.Actions[1].Args[1])
	}
	if !tb.Actions[2].Negate {
		t.Errorf("Actions[2]: want did_not")
	}
}

func TestParseFindExpr(t *testing.T) {
	prog := mustParse(t, `
workflow "w" {
  step "cats" { tool "srv" "list-ticket-categories" {} }
  step "create" depends_on "cats" {
    tool "srv" "create-ticket" {
      ticket_category_id step("cats").ticket_categories.find(name == "Defect").id
    }
  }
}`)
	b := block[*ast.WorkflowBlock](t, prog, 0)
	arg := b.Steps[1].MCPCall.Args["ticket_category_id"]
	fe, ok := arg.(*ast.FindExpr)
	if !ok {
		t.Fatalf("expected FindExpr, got %T", arg)
	}
	if fe.Field != "id" {
		t.Errorf("FindExpr.Field: got %q, want id", fe.Field)
	}
	src, ok := fe.Source.(*ast.StepResultExpr)
	if !ok || src.StepName != "cats" || src.Field != "ticket_categories" {
		t.Errorf("FindExpr.Source: got %#v", fe.Source)
	}
	cmp, ok := fe.Cond.(*ast.CompareCondition)
	if !ok || cmp.Op != "==" {
		t.Fatalf("FindExpr.Cond: got %#v", fe.Cond)
	}
}

// TestParseCallExprStrayTokenTerminates is a regression guard for an OOM bug:
// parsePrimary returned an error node WITHOUT consuming the offending token, so
// parseCallExpr's arg loop (gated only by `)`/EOF) spun forever, appending an
// error node each pass until the process was OOM-killed. The parser must now
// make progress and report the error instead of hanging. go test's timeout also
// catches a regression (a hang fails the run).
func TestParseCallExprStrayTokenTerminates(t *testing.T) {
	src := `workflow "w" {
  step "s" {
    tool "a" "b" { q concat("x", }) }
  }
}`
	tokens, ld := lexer.Lex("t.tln", src)
	if ld.HasErrors() {
		t.Fatalf("unexpected lex error: %v", ld)
	}
	_, pd := Parse("t.tln", tokens) // must RETURN (not hang)
	if !pd.HasErrors() {
		t.Fatal("expected a parse error for the stray `}` in the call args, got none")
	}
}

// TestParseConcatWithDateWindow confirms the real inspection-workflow pattern —
// a relative date window built with the date toolkit inside a concat() arg —
// parses cleanly (and the spin fix didn't break valid programs).
func TestParseConcatWithDateWindow(t *testing.T) {
	prog := mustParse(t, `workflow "Inspection reminder" {
  step "search" {
    tool "timly-api" "list_items" {
      query concat("custom_attributes.next_inspection.value:[", today, " TO ", today + 7 days, "]")
    }
  }
}`)
	if len(prog.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(prog.Blocks))
	}
}
