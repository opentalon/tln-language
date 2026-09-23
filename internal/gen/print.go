// Package gen renders tln AST nodes back into canonical .tln source.
//
// It is a pure printer: no I/O, no FactStore dependency, no evaluation. The
// discovery algorithms that produce specs (workflow compilation, threshold
// discovery, pattern mining, feedback-adjusted rules) live host-side in
// OpenTalon; this package is the one canonical renderer they — and IDE
// auto-fix, the REPL, and `tln explain` — share instead of ad-hoc
// fmt.Sprintf calls. It lives here because it is intrinsically tied to the
// grammar: every new clause or block type teaches the printer one more shape,
// kept in lockstep with internal/parser and internal/ast.
//
// The guarantee: for any AST the parser produces, Print → Lex → Parse yields
// an AST equivalent to the input. Round-trip tests in print_test.go encode
// that invariant per block type.
//
// Known non-round-trippable shapes (language gaps, not printer bugs): a
// Duration literal embedded in an arithmetic expression has no surface
// syntax. The only producer is the `approaching` sugar's desugared upper
// bound, which the printer re-sugars back to `approaching` so it round-trips.
package gen

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/opentalon/tln-language/internal/ast"
)

// Program renders an entire program: imports first (in order), then blocks
// separated by a single blank line. Output ordering matches input; the caller
// decides the order.
func Program(p *ast.Program) string {
	if p == nil {
		return ""
	}
	var parts []string
	if len(p.Imports) > 0 {
		var imp strings.Builder
		for _, im := range p.Imports {
			imp.WriteString("import ")
			imp.WriteString(quote(im.Path))
			imp.WriteByte('\n')
		}
		parts = append(parts, strings.TrimRight(imp.String(), "\n"))
	}
	for _, b := range p.Blocks {
		parts = append(parts, render(b))
	}
	return strings.Join(parts, "\n\n")
}

// Block dispatches on the concrete AST type — convenience for callers that
// hold an ast.Block and don't want to type-switch.
func Block(b ast.Block) string { return render(b) }

// Detect renders an ast.DetectBlock back to .tln source.
func Detect(b *ast.DetectBlock) string { return render(b) }

// Rule renders an ast.RuleBlock back to source.
func Rule(b *ast.RuleBlock) string { return render(b) }

// Recommend renders an ast.RecommendBlock back to source.
func Recommend(b *ast.RecommendBlock) string { return render(b) }

// Workflow renders an ast.WorkflowBlock back to source. Useful for host-side
// workflow compilation (turning a recorded tool-call sequence into a workflow
// block).
func Workflow(b *ast.WorkflowBlock) string { return render(b) }

func render(b ast.Block) string {
	p := &printer{}
	p.block(b)
	return strings.TrimRight(p.b.String(), "\n")
}

// ─── printer ────────────────────────────────────────────────────────────────

type printer struct {
	b     strings.Builder
	depth int
}

func (p *printer) line(s string) {
	for i := 0; i < p.depth; i++ {
		p.b.WriteString("  ")
	}
	p.b.WriteString(s)
	p.b.WriteByte('\n')
}

// open writes a `<header> {` line and steps in one level.
func (p *printer) open(header string) {
	p.line(header + " {")
	p.depth++
}

// close steps out one level and writes the closing brace.
func (p *printer) close() {
	p.depth--
	p.line("}")
}

func (p *printer) block(b ast.Block) {
	switch b := b.(type) {
	case *ast.DetectBlock:
		p.detect(b)
	case *ast.RuleBlock:
		p.rule(b)
	case *ast.RecommendBlock:
		p.recommend(b)
	case *ast.CombineBlock:
		p.combine(b)
	case *ast.DefineBlock:
		p.define(b)
	case *ast.WorkflowBlock:
		p.workflow(b)
	case *ast.PredictBlock:
		p.predict(b)
	case *ast.ForecastBlock:
		p.forecast(b)
	case *ast.ClusterBlock:
		p.cluster(b)
	case *ast.ClassifyBlock:
		p.classify(b)
	case *ast.DecideBlock:
		p.decide(b)
	case *ast.SimilarBlock:
		p.similar(b)
	case *ast.RelatedBlock:
		p.related(b)
	case *ast.OnBlock:
		p.on(b)
	case *ast.ConstraintBlock:
		p.constraint(b)
	case *ast.StateMachineBlock:
		p.stateMachine(b)
	case *ast.EnrichBlock:
		p.enrich(b)
	case *ast.CollectBlock:
		p.collect(b)
	case *ast.ThresholdBlock:
		p.threshold(b)
	case *ast.ConnectorBlock:
		p.connector(b)
	case *ast.DeriveBlock:
		p.derive(b)
	case *ast.ModelBlock:
		p.model(b)
	case *ast.ModuleBlock:
		p.module(b)
	case *ast.TestBlock:
		p.test(b)
	default:
		p.line(fmt.Sprintf("// unsupported block type %T", b))
	}
}

// ─── detect ─────────────────────────────────────────────────────────────────

func (p *printer) detect(b *ast.DetectBlock) {
	p.open("detect " + quote(b.Name))
	p.selector(b.Selector)
	if b.Flag != nil {
		p.line("flag matching " + b.Flag.Kind)
	}
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	if b.Pattern != nil {
		p.line(patternStr(b.Pattern))
	}
	if b.Confidence != nil {
		p.line("confidence >= " + numStr(*b.Confidence))
	}
	if b.Score != nil {
		p.line("confidence " + numStr(*b.Score))
	}
	if b.Source != nil {
		p.line("source " + quote(*b.Source))
	}
	for _, c := range b.Calculate {
		p.line(calculateStr(c))
	}
	for _, h := range b.Having {
		p.line("having " + condStr(h))
	}
	for _, l := range b.Loggers {
		p.line(loggerStr(l))
	}
	if b.Anomaly != nil {
		p.line(anomalyClauseStr(b.Anomaly))
	}
	if b.Predict != nil {
		p.line(predictClauseStr(b.Predict))
	}
	if b.Forecast != nil {
		p.line(forecastClauseStr(b.Forecast))
	}
	if b.Cluster != nil {
		p.line("cluster by " + exprListStr(b.Cluster.ByAttrs))
	}
	if b.Similar != nil {
		p.line(similarClauseStr(b.Similar))
	}
	if b.Related != nil {
		p.line(relatedClauseStr(b.Related))
	}
	if b.Tune != nil {
		p.line("tune against test " + quote(b.Tune.AgainstTest))
	}
	if b.Remediate != nil {
		p.remediate(b.Remediate)
	}
	if b.Recommend != nil {
		p.recommend(b.Recommend)
	}
	p.close()
}

// ─── rule ───────────────────────────────────────────────────────────────────

func (p *printer) rule(b *ast.RuleBlock) {
	header := "rule " + quote(b.Name)
	if b.Strict {
		header = "strict " + header
	}
	p.open(header)
	switch {
	case b.Selector != nil:
		p.selector(*b.Selector)
	case b.When != nil:
		p.line("when " + condStr(b.When))
	}
	if b.Every != nil {
		p.line(fmt.Sprintf("every %d %s on attr %s", b.Every.Value, b.Every.Unit, quote(b.Every.OnAttr)))
	}
	if b.Before != nil {
		p.line("before " + quote(*b.Before))
	}
	if b.After != nil {
		p.line("after " + quote(*b.After))
	}
	if len(b.Overrides) > 0 {
		names := make([]string, len(b.Overrides))
		for i, n := range b.Overrides {
			names[i] = quote(n)
		}
		p.line("overrides " + strings.Join(names, ", "))
	}
	switch {
	case b.Block != nil:
		p.line("block " + quote(*b.Block))
	case b.Allow != nil:
		p.line("allow " + quote(*b.Allow))
	case b.Requires != nil:
		p.line(requiresStr(b.Requires))
	}
	for _, do := range b.Do {
		p.line(doStr(do))
	}
	if b.Reason != nil {
		p.line("reason " + template(b.Reason))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	if b.Score != nil {
		p.line("confidence " + numStr(*b.Score))
	}
	if b.Source != nil {
		p.line("source " + quote(*b.Source))
	}
	for _, l := range b.Loggers {
		p.line(loggerStr(l))
	}
	p.close()
}

// ─── recommend ──────────────────────────────────────────────────────────────

func (p *printer) recommend(b *ast.RecommendBlock) {
	p.open("recommend " + quote(b.Name))
	if b.When != nil {
		p.line("when " + condStr(b.When))
	}
	for _, c := range b.Calculate {
		p.line(calculateStr(c))
	}
	if b.Suggest != nil {
		s := "suggest " + template(b.Suggest)
		if b.SuggestProbability > 0 {
			s += " with probability " + numStr(b.SuggestProbability)
			if b.FeedbackWindowDays > 0 {
				s += fmt.Sprintf(" learn from feedback within %d days", b.FeedbackWindowDays)
			}
		}
		p.line(s)
	}
	if b.Remediate != nil {
		p.remediate(b.Remediate)
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	for _, l := range b.Loggers {
		p.line(loggerStr(l))
	}
	p.close()
}

// ─── combine ────────────────────────────────────────────────────────────────

func (p *printer) combine(b *ast.CombineBlock) {
	p.open("combine " + quote(b.Name))
	p.selector(b.Selector)
	for _, o := range b.Optimize {
		p.line(o.Direction + " " + exprStr(o.Attr))
	}
	for _, c := range b.Constraints {
		p.line("subject_to " + exprStr(c.Left) + " " + c.Op + " " + exprStr(c.Right))
	}
	if b.Select != nil {
		p.line(fmt.Sprintf("select %d from records", b.Select.Size))
	}
	if b.Seed != nil {
		p.line(fmt.Sprintf("seed %d", *b.Seed))
	}
	if b.Sequence {
		p.line("sequence")
	}
	if b.Coordinates != nil {
		p.line("coordinates " + exprStr(b.Coordinates.X) + ", " + exprStr(b.Coordinates.Y))
	}
	if b.Solver != "" {
		p.line("solver " + b.Solver)
	}
	if len(b.Return) > 0 {
		p.line("return " + strings.Join(b.Return, ", "))
	}
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	p.close()
}

// ─── define ─────────────────────────────────────────────────────────────────

func (p *printer) define(b *ast.DefineBlock) {
	header := "define " + quote(b.Name)
	if len(b.Params) > 0 {
		header += "(" + strings.Join(b.Params, ", ") + ")"
	}
	p.open(header)
	for _, c := range b.Conditions {
		p.line(condStr(c))
	}
	if b.ForEach != nil {
		p.open("for each " + b.ForEach.Variable + " in " + exprStr(b.ForEach.Over))
		for _, c := range b.ForEach.Body {
			p.line(condStr(c))
		}
		p.close()
	}
	p.close()
}

// ─── workflow ───────────────────────────────────────────────────────────────

func (p *printer) workflow(b *ast.WorkflowBlock) {
	p.open("workflow " + quote(b.Name))
	for _, s := range b.Steps {
		header := "step " + quote(s.Name)
		switch len(s.DependsOn) {
		case 0:
		case 1:
			header += " depends_on " + quote(s.DependsOn[0])
		default:
			deps := make([]string, len(s.DependsOn))
			for i, d := range s.DependsOn {
				deps[i] = quote(d)
			}
			header += " depends_on [" + strings.Join(deps, ", ") + "]"
		}
		p.open(header)
		if s.MCPCall != nil {
			p.mcpCall(s.MCPCall)
		}
		p.close()
	}
	p.close()
}

// ─── ML blocks ──────────────────────────────────────────────────────────────

func (p *printer) predict(b *ast.PredictBlock) {
	p.open("predict " + quote(b.Name))
	p.selector(b.Selector)
	if len(b.Features) > 0 {
		p.line("features [" + exprListStr(b.Features) + "]")
	}
	if b.TrainedOn != nil {
		p.line("trained_on records where " + condStr(b.TrainedOn.Conditions[0]))
	}
	if b.UsingModel != "" {
		p.line("using model " + quote(b.UsingModel))
	}
	if b.LabelAttr != "" {
		p.line("label_attr " + quote(b.LabelAttr))
	}
	if b.Confidence != nil {
		p.line("confidence >= " + numStr(*b.Confidence))
	}
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	p.close()
}

func (p *printer) forecast(b *ast.ForecastBlock) {
	p.open("forecast " + quote(b.Name))
	p.selector(b.Selector)
	p.line(seriesStr(b.Series))
	if b.Predict != nil {
		p.line(forecastPredictStr(b.Predict))
	}
	if b.When != nil {
		p.line("when " + condStr(b.When))
	}
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	p.close()
}

func (p *printer) cluster(b *ast.ClusterBlock) {
	p.open("cluster " + quote(b.Name))
	p.selector(b.Selector)
	p.line("by " + exprListStr(b.ByAttrs))
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	p.close()
}

func (p *printer) classify(b *ast.ClassifyBlock) {
	p.open("classify " + quote(b.Name))
	p.selector(b.Selector)
	if len(b.Features) > 0 {
		p.line("features [" + exprListStr(b.Features) + "]")
	}
	if b.TrainedOn != nil {
		p.line("trained_on records where " + condStr(b.TrainedOn.Conditions[0]))
	}
	if b.UsingModel != "" {
		p.line("using model " + quote(b.UsingModel))
	}
	if b.LabelAttr != "" {
		p.line("label_attr " + quote(b.LabelAttr))
	}
	if b.Confidence != nil {
		p.line("confidence >= " + numStr(*b.Confidence))
	}
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	p.close()
}

// decide prints a `decide "name" { ... }` typed-decision block. It emits the
// clauses for whichever mode the block uses (deterministic features/trained_on
// or model ask/using-model); the validator guarantees they never coexist.
func (p *printer) decide(b *ast.DecideBlock) {
	p.open("decide " + quote(b.Name))
	p.selector(b.Selector)
	if len(b.Choices) > 0 {
		p.line("choices [" + exprListStr(b.Choices) + "]")
	}
	if len(b.Features) > 0 {
		p.line("features [" + exprListStr(b.Features) + "]")
	}
	if b.TrainedOn != nil {
		p.line("trained_on records where " + condStr(b.TrainedOn.Conditions[0]))
	}
	if b.LabelAttr != "" {
		p.line("label_attr " + quote(b.LabelAttr))
	}
	if b.Ask != nil {
		p.line("ask " + exprStr(b.Ask))
	}
	if b.UsingModel != "" {
		p.line("using model " + quote(b.UsingModel))
	}
	if b.Confidence != nil {
		p.line("confidence >= " + numStr(*b.Confidence))
	}
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	p.close()
}

// model prints a `model "name" { ... }` block with inline fitted params.
func (p *printer) model(b *ast.ModelBlock) {
	p.open("model " + quote(b.Name))
	switch b.Algo {
	case "classify_knn":
		p.line(fmt.Sprintf("classify knn k %d", b.K))
	case "predict_decision_tree":
		p.line("predict tree")
	}
	if len(b.Features) > 0 {
		p.line("features [" + exprListStr(b.Features) + "]")
	}
	if len(b.Examples) > 0 {
		p.open("fitted")
		for _, ex := range b.Examples {
			nums := make([]string, len(ex.Features))
			for i, f := range ex.Features {
				nums[i] = numStr(f)
			}
			p.line("example [" + strings.Join(nums, ", ") + "] label " + quote(ex.Label))
		}
		p.close()
	}
	if len(b.Tree) > 0 {
		p.open("fitted tree")
		for _, n := range b.Tree {
			if n.Leaf {
				p.line(fmt.Sprintf("node %d leaf %s %s", n.Index, quote(n.Class), numStr(n.Purity)))
			} else {
				p.line(fmt.Sprintf("node %d split %s %s left %d right %d",
					n.Index, quote(n.Feature), numStr(n.Threshold), n.Left, n.Right))
			}
		}
		p.close()
	}
	if b.ComputedFrom != "" {
		p.line("computed_from " + quote(b.ComputedFrom))
	}
	if b.ValidUntil != "" {
		p.line("valid_until " + quote(b.ValidUntil))
	}
	p.close()
}

// module prints a `module "ns" { export <block> ... }` block.
func (p *printer) module(b *ast.ModuleBlock) {
	p.open("module " + quote(b.Namespace))
	for _, mem := range b.Members {
		p.line("export")
		p.block(mem)
	}
	p.close()
}

func (p *printer) similar(b *ast.SimilarBlock) {
	p.open("find similar " + quote(b.Name))
	p.selector(b.Selector)
	if b.To != nil {
		p.line("to " + exprStr(b.To))
	}
	if b.VectorScope != "" {
		p.line("using vector scope " + quote(b.VectorScope))
	}
	if b.TopK != nil {
		p.line(fmt.Sprintf("top %d", *b.TopK))
	}
	if b.Within != nil {
		p.line("within " + numStr(*b.Within))
	}
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	p.close()
}

func (p *printer) related(b *ast.RelatedBlock) {
	p.open("find related " + quote(b.Name))
	p.selector(b.Selector)
	if b.To != nil {
		p.line("to " + exprStr(b.To))
	}
	if len(b.Seeds) > 0 {
		p.line("seeds [" + exprListStr(b.Seeds) + "]")
	}
	if b.TopK != nil {
		p.line(fmt.Sprintf("top_k %d", *b.TopK))
	}
	if b.Damping != nil {
		p.line("damping " + numStr(*b.Damping))
	}
	if b.Tol != nil {
		p.line("tolerance " + numStr(*b.Tol))
	}
	if b.MaxIter != nil {
		p.line(fmt.Sprintf("max_iterations %d", *b.MaxIter))
	}
	if b.Label != nil {
		p.line("label " + template(b.Label))
	}
	if b.Priority != nil {
		p.line("priority " + priorityStr(*b.Priority))
	}
	p.close()
}

// ─── on (reactive) ──────────────────────────────────────────────────────────

func (p *printer) on(b *ast.OnBlock) {
	var trigger string
	switch b.Trigger {
	case "change":
		trigger = "on change attr " + quote(b.Attr)
		if b.ToValue != nil {
			trigger += " to " + exprStr(b.ToValue)
		}
	case "assert":
		trigger = "on assert " + b.FactType
	case "retract":
		trigger = "on retract " + b.FactType
	}
	p.open(trigger)
	if b.When != nil {
		p.line("when " + condStr(b.When))
	}
	for _, a := range b.Actions {
		switch a := a.(type) {
		case *ast.LoggerAction:
			p.line(loggerStr(a))
		case *ast.BlockRefAction:
			p.line(a.Kind + " " + quote(a.Name))
		}
	}
	p.close()
}

// ─── constraint ─────────────────────────────────────────────────────────────

func (p *printer) constraint(b *ast.ConstraintBlock) {
	p.open("constraint " + quote(b.Name))
	p.selector(b.Selector)
	if b.Require != nil {
		p.line("require " + condStr(b.Require))
	}
	viol := "on_violation " + b.OnViolation.Mode
	if b.OnViolation.Message != "" {
		viol += " " + quote(b.OnViolation.Message)
	}
	p.line(viol)
	p.close()
}

// ─── state_machine ──────────────────────────────────────────────────────────

func (p *printer) stateMachine(b *ast.StateMachineBlock) {
	p.open("state_machine " + quote(b.Name))
	p.selector(b.Selector)
	if len(b.States) > 0 {
		names := make([]string, len(b.States))
		for i, s := range b.States {
			names[i] = s.Name // already "parent/child" for substates
		}
		p.line("states " + strings.Join(names, ", "))
	}
	if b.Initial != "" {
		p.line("initial " + b.Initial)
	}
	if b.StateAttr != "" {
		p.line("state_attr " + quote(b.StateAttr))
	}
	for _, t := range b.Transitions {
		s := "transition " + t.From + " -> " + t.To
		if t.When != nil {
			s += " when " + condStr(t.When)
		}
		p.line(s)
	}
	for _, inv := range b.Invariants {
		p.line("invariant in " + inv.State + " require " + condStr(inv.Required))
	}
	p.close()
}

// ─── enrich ─────────────────────────────────────────────────────────────────

func (p *printer) enrich(b *ast.EnrichBlock) {
	p.open("enrich " + quote(b.Name))
	p.selector(b.Selector)
	p.line("stale_after " + durationStr(b.StaleAfter))
	if b.Call != nil {
		p.mcpCall(b.Call)
	}
	for _, u := range b.Updates {
		p.line("update attr " + quote(u.Attr) + " from result." + u.ResultPath)
	}
	p.close()
}

// ─── collect ────────────────────────────────────────────────────────────────

func (p *printer) collect(b *ast.CollectBlock) {
	p.open("collect " + quote(b.Name))
	p.line("schedule " + scheduleStr(b.Schedule))
	if b.Call != nil {
		p.mcpCall(b.Call)
	}
	store := "store results as " + b.StoreAs
	if b.Tag != "" {
		store += " tag " + quote(b.Tag)
	}
	p.line(store)
	p.close()
}

func (p *printer) derive(b *ast.DeriveBlock) {
	p.open("derive " + b.Name + "(" + b.Var + ")")
	p.selector(b.Selector)
	p.close()
}

func (p *printer) threshold(b *ast.ThresholdBlock) {
	p.open("threshold " + quote(b.Name))
	p.line("value " + numStr(b.Value))
	if b.ComputedFrom != "" {
		p.line("computed_from " + quote(b.ComputedFrom))
	}
	if b.ValidUntil != "" {
		p.line("valid_until " + quote(b.ValidUntil))
	}
	p.close()
}

// connector renders a connector block. Config keys are emitted in sorted order
// for deterministic output.
func (p *printer) connector(b *ast.ConnectorBlock) {
	p.open("connector " + quote(b.Name) + " via " + b.Plugin)
	keys := make([]string, 0, len(b.Config))
	for k := range b.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.line(k + " " + exprStr(b.Config[k]))
	}
	p.close()
}

// scheduleStr renders the stored schedule metadata back to its source form.
// Stored values: "weekly"|"daily"|"hourly", "every N unit", "cron:<expr>", or
// an opaque string.
func scheduleStr(s string) string {
	switch {
	case strings.HasPrefix(s, "cron:"):
		return "cron " + quote(strings.TrimPrefix(s, "cron:"))
	case strings.HasPrefix(s, "every "), s == "weekly", s == "daily", s == "hourly":
		return s
	default:
		return quote(s)
	}
}

// ─── test blocks ────────────────────────────────────────────────────────────

func (p *printer) test(b *ast.TestBlock) {
	p.open("test " + quote(b.Name))
	if len(b.Given) > 0 {
		p.open("given")
		for _, d := range b.Given {
			p.line(testDatumStr(d))
		}
		p.close()
	}
	if b.WhenBlock != "" {
		p.line("when " + b.WhenKind + " " + quote(b.WhenBlock))
	}
	for _, m := range b.Mocks {
		p.mock(m)
	}
	if len(b.Expect) > 0 || len(b.MCPCalls) > 0 || len(b.Actions) > 0 {
		p.open("expect")
		for _, a := range b.Expect {
			p.line(testAssertionStr(a))
		}
		for _, a := range b.Actions {
			p.line(actionAssertionStr(a))
		}
		for _, c := range b.MCPCalls {
			p.line(mcpCalledStr(c))
		}
		p.close()
	}
	p.close()
}

func testDatumStr(d ast.TestDatum) string {
	switch d.Kind {
	case "record":
		s := fmt.Sprintf("record %d", d.ID)
		for _, k := range sortedKeys(d.Fields) {
			s += " " + k + " " + valueStr(d.Fields[k])
		}
		return s
	case "attr":
		// exactly one field: the attribute name → value
		for _, k := range sortedKeys(d.Fields) {
			return fmt.Sprintf("attr %d %s %s", d.ID, quote(k), valueStr(d.Fields[k]))
		}
		return fmt.Sprintf("attr %d", d.ID)
	}
	return ""
}

func testAssertionStr(a ast.TestAssertion) string {
	switch a.Kind {
	case "flagged":
		return fmt.Sprintf("flagged %d", a.ID)
	case "not_flagged":
		return fmt.Sprintf("not flagged %d", a.ID)
	case "label":
		return "label " + a.Op + " " + quote(a.Value)
	case "priority":
		return "priority " + a.Op + " " + a.Value
	case "threshold":
		return "threshold " + a.Op + " " + a.Value
	case "score":
		return fmt.Sprintf("score %d %s %s", a.ID, a.Op, a.Value)
	default:
		return a.Kind + " " + a.Op + " " + a.Value
	}
}

func actionAssertionStr(a ast.ActionAssertion) string {
	s := "did"
	if a.Negate {
		s = "did_not"
	}
	s += fmt.Sprintf(" %d %s", a.ID, a.Verb)
	for _, arg := range a.Args {
		if arg.Contains {
			s += " contains " + valueStr(arg.Value)
			continue
		}
		s += " " + valueStr(arg.Value)
	}
	return s
}

func (p *printer) mock(m ast.MockClause) {
	p.open("mock tool " + quote(m.Server) + " " + quote(m.Tool))
	if m.Returns != nil {
		var kvs []string
		for _, k := range sortedKeys(m.Returns) {
			kvs = append(kvs, k+" "+valueStr(m.Returns[k]))
		}
		p.line("returns { " + strings.Join(kvs, " ") + " }")
	}
	if m.Fails {
		switch {
		case m.FailAfter > 0:
			p.line(fmt.Sprintf("fails after %d", m.FailAfter))
		case m.FailMsg != "":
			p.line("fails " + quote(m.FailMsg))
		default:
			p.line("fails")
		}
	}
	p.close()
}

func mcpCalledStr(c ast.MCPCalledAssertion) string {
	s := "tool_called " + quote(c.Server) + " " + quote(c.Tool)
	if len(c.Args) > 0 {
		var preds []string
		for _, a := range c.Args {
			preds = append(preds, a.Name+" "+a.Op+" "+valueStr(a.Value))
		}
		s += " with { " + strings.Join(preds, " ") + " }"
	}
	return s
}

// ─── shared clause helpers ──────────────────────────────────────────────────

func (p *printer) selector(sel ast.Selector) {
	if len(sel.Conditions) == 0 {
		return
	}
	p.line("for records where " + condStr(sel.Conditions[0]))
}

func (p *printer) remediate(r *ast.RemediateClause) {
	header := "remediate"
	if r.Mode != "" && r.Mode != "propose" {
		header += " " + r.Mode
	}
	p.open(header)
	if r.Role != "" {
		p.line("requires role " + quote(r.Role))
	}
	if r.Batch != "" {
		p.line("batch " + quote(r.Batch))
	}
	p.actionBody(r.Body)
	p.close()
}

// actionBody prints an imperative action body: MCP calls plus the
// control-flow forms (if/else, for-each, while), round-tripping the source.
func (p *printer) actionBody(actions []ast.Action) {
	for _, a := range actions {
		switch act := a.(type) {
		case *ast.MCPAction:
			p.mcpCall(act.Call)
		case *ast.IfAction:
			p.open("if " + condStr(act.Cond))
			p.actionBody(act.Then)
			p.close()
			if len(act.Else) > 0 {
				p.open("else")
				p.actionBody(act.Else)
				p.close()
			}
		case *ast.ForEachAction:
			p.open("for each " + act.Variable + " in " + exprStr(act.Over))
			p.actionBody(act.Body)
			p.close()
		case *ast.WhileAction:
			p.open("while " + condStr(act.Cond))
			p.actionBody(act.Body)
			p.close()
		}
	}
}

func (p *printer) mcpCall(c *ast.MCPCall) {
	p.open("tool " + quote(c.Server) + " " + quote(c.Tool))
	for _, k := range sortedExprKeys(c.Args) {
		p.line(k + " " + exprStr(c.Args[k]))
	}
	if c.OnError != nil {
		p.onError(c.OnError)
	}
	p.close()
}

func (p *printer) onError(oe *ast.OnErrorClause) {
	p.open("on_error")
	for _, a := range oe.Actions {
		switch a := a.(type) {
		case *ast.RetryAction:
			p.line(fmt.Sprintf("retry %d times", a.Times))
		case *ast.LogErrorAction:
			p.line("log " + template(&a.Message))
		case *ast.SkipAction:
			p.line("skip")
		case *ast.FailAction:
			p.line("fail")
		}
	}
	p.close()
}

func calculateStr(c ast.CalculateClause) string {
	s := "calculate " + c.Name + " from " + c.From
	if c.Value != nil {
		s += " of " + exprStr(c.Value)
	}
	if len(c.Where) > 0 {
		s += " where " + condStr(c.Where[0])
	}
	if c.Method != "" {
		s += " " + methodKeyword(c.Method)
	}
	if c.Within != nil {
		s += " within last " + durationStr(*c.Within)
	}
	return s
}

// methodKeyword maps a stored calculate Method back to its canonical surface
// keyword.
func methodKeyword(m string) string {
	if m == "wma" {
		return "weighted_moving_average"
	}
	return m // "average", "sum", "count"
}

func patternStr(pat *ast.PatternExpr) string {
	s := fmt.Sprintf("when %d+ records", pat.MinCount)
	if pat.GroupBy != "" {
		s += " same " + pat.GroupBy
	}
	if pat.Window != nil {
		s += " within " + durationStr(*pat.Window)
	}
	return s
}

func requiresStr(r *ast.RequiresClause) string {
	if r.Approval != nil {
		return "requires approval from role " + quote(r.Approval.Role)
	}
	return "requires " + quote(r.What)
}

func doStr(d *ast.DoAction) string {
	s := "do " + d.Verb
	for _, a := range d.Args {
		s += " " + exprStr(a)
	}
	return s
}

func loggerStr(l *ast.LoggerAction) string {
	return "logger." + l.Level + " " + template(&l.Message)
}

func anomalyClauseStr(a *ast.AnomalyClause) string {
	s := "is anomaly"
	if a.Method != "" {
		s += " using " + a.Method
	}
	s += " compared_to last " + durationStr(a.Window)
	return s
}

func predictClauseStr(c *ast.PredictClause) string {
	s := "predict"
	if len(c.Features) > 0 {
		s += " features [" + exprListStr(c.Features) + "]"
	}
	if c.TrainedOn != nil {
		s += " trained_on records where " + condStr(c.TrainedOn.Conditions[0])
	}
	if c.Confidence != nil {
		s += " confidence >= " + numStr(*c.Confidence)
	}
	return s
}

func forecastClauseStr(c *ast.ForecastClause) string {
	s := "forecast " + seriesStr(c.Series)
	if c.Predict != nil {
		s += " " + forecastPredictStr(c.Predict)
	}
	return s
}

func seriesStr(s ast.SeriesClause) string {
	return "series " + exprStr(s.Attr) + " over last " + durationStr(s.Window)
}

func forecastPredictStr(c *ast.ForecastPredictClause) string {
	return "predict " + c.Variable + " value " + condStr(c.Condition)
}

func similarClauseStr(c *ast.SimilarClause) string {
	s := "find similar to " + exprStr(c.To)
	if c.Within != nil {
		s += " within " + numStr(*c.Within)
	}
	return s
}

func relatedClauseStr(c *ast.RelatedClause) string {
	s := "find related"
	if c.To != nil {
		s += " to " + exprStr(c.To)
	}
	if len(c.Seeds) > 0 {
		s += " seeds [" + exprListStr(c.Seeds) + "]"
	}
	if c.TopK != nil {
		s += fmt.Sprintf(" top_k %d", *c.TopK)
	}
	if c.Damping != nil {
		s += " damping " + numStr(*c.Damping)
	}
	return s
}

// ─── conditions ─────────────────────────────────────────────────────────────

func condStr(c ast.Condition) string {
	switch c := c.(type) {
	case *ast.CompareCondition:
		return exprStr(c.Left) + " " + c.Op + " " + exprStr(c.Right)
	case *ast.LogicalCondition:
		if s, ok := resugarApproaching(c); ok {
			return s
		}
		return condStr(c.Left) + " " + c.Op + " " + condStr(c.Right)
	case *ast.NotCondition:
		return "not " + condStr(c.Inner)
	case *ast.MembershipCondition:
		op := "in"
		if c.Negated {
			op = "not in"
		}
		if len(c.Members) == 1 {
			if ct, ok := c.Members[0].(*ast.CategoryTreeExpr); ok {
				return exprStr(c.Expr) + " " + op + " " + exprStr(ct)
			}
		}
		return exprStr(c.Expr) + " " + op + " [" + exprListStr(c.Members) + "]"
	case *ast.IsCondition:
		if c.Subject != nil {
			return exprStr(c.Subject) + " is " + quote(c.Name)
		}
		return "is " + quote(c.Name)
	case *ast.HasCondition:
		return "has record type " + quote(c.Type)
	case *ast.StringMatchCondition:
		if c.Op == "matches_phrase" {
			return exprStr(c.Subject) + " matches phrase " + quote(c.Value)
		}
		return exprStr(c.Subject) + " " + c.Op + " " + quote(c.Value)
	case *ast.AnomalyCondition:
		s := ""
		if c.Subject != nil {
			s = exprStr(c.Subject) + " "
		}
		s += "is anomaly"
		if c.Method != "" {
			s += " using " + c.Method
		}
		s += " compared_to last " + durationStr(c.Window)
		return s
	case *ast.CorrelationCondition:
		return exprStr(c.Left) + " correlates_with " + exprStr(c.Right) +
			" over last " + durationStr(c.Window) + " " + c.Op + " " + numStr(c.Threshold)
	case *ast.TemporalCondition:
		return exprStr(c.Subject) + " " + c.Op + " " + durationStr(c.Value)
	case *ast.ChangedToCondition:
		return c.Attribute + " changed_to " + exprStr(c.Value)
	case *ast.AsOfCondition:
		return "was (" + condStr(c.Inner) + ") " + durationStr(c.Delta) + " ago"
	case *ast.BlockMatchesCondition:
		return c.Kind + " " + quote(c.Name) + " matches"
	case *ast.PredicateCallCondition:
		return c.Name + "(" + c.Var + ")"
	case *ast.EventSequenceCondition:
		steps := make([]string, len(c.Steps))
		for i, st := range c.Steps {
			steps[i] = quote(st)
		}
		s := "event_sequence " + strings.Join(steps, " -> ")
		if c.Window.Unit != "" {
			s += " within " + durationStr(c.Window)
		}
		return s
	case *ast.RecordSequenceCondition:
		var s string
		for i, st := range c.Steps {
			if i > 0 {
				s += " followed_by "
			}
			s += "record type " + quote(st)
		}
		if c.On != "" && c.On != "item" {
			s += " on same " + c.On
		}
		if c.Window.Unit != "" {
			s += " within " + durationStr(c.Window)
		}
		return s
	default:
		return fmt.Sprintf("/* unsupported condition %T */", c)
	}
}

// resugarApproaching recognises the desugared shape the parser produces for
// `EXPR approaching within N UNIT`:
//
//	EXPR >= today  and  EXPR <= today + N UNIT
//
// and re-emits the `approaching` sugar. This is the only producer of a
// Duration literal inside a BinaryExpr, which has no other surface syntax, so
// re-sugaring is what makes the shape round-trip.
func resugarApproaching(c *ast.LogicalCondition) (string, bool) {
	if c.Op != "and" {
		return "", false
	}
	lo, ok := c.Left.(*ast.CompareCondition)
	if !ok || lo.Op != ">=" {
		return "", false
	}
	if _, ok := lo.Right.(*ast.TodayExpr); !ok {
		return "", false
	}
	hi, ok := c.Right.(*ast.CompareCondition)
	if !ok || hi.Op != "<=" {
		return "", false
	}
	if !reflect.DeepEqual(lo.Left, hi.Left) {
		return "", false
	}
	bin, ok := hi.Right.(*ast.BinaryExpr)
	if !ok || bin.Op != "+" {
		return "", false
	}
	if _, ok := bin.Left.(*ast.TodayExpr); !ok {
		return "", false
	}
	lit, ok := bin.Right.(*ast.LiteralExpr)
	if !ok {
		return "", false
	}
	dur, ok := lit.Value.(ast.Duration)
	if !ok {
		return "", false
	}
	return exprStr(lo.Left) + " approaching within " + durationStr(dur), true
}

// ─── expressions ────────────────────────────────────────────────────────────

func exprStr(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.AttrExpr:
		return "attr " + quote(e.Name)
	case *ast.LiteralExpr:
		return literalStr(e)
	case *ast.IdentExpr:
		return e.Name
	case *ast.BinaryExpr:
		return exprStrParen(e.Left) + " " + e.Op + " " + exprStrParen(e.Right)
	case *ast.UnaryExpr:
		return e.Op + exprStrParen(e.Operand)
	case *ast.ListExpr:
		return "[" + exprListStr(e.Elements) + "]"
	case *ast.ContextExpr:
		return "context." + e.Field
	case *ast.StepResultExpr:
		return "step(" + quote(e.StepName) + ")." + e.Field
	case *ast.CategoryTreeExpr:
		return "category_tree(" + quote(e.Root) + ")"
	case *ast.TodayExpr:
		return "today"
	case *ast.ThresholdRefExpr:
		return "threshold " + quote(e.Name)
	case *ast.EnvExpr:
		return "env " + quote(e.Name)
	case *ast.MapExpr:
		return exprStr(e.Source) + ".map(" + e.Field + ")"
	case *ast.LearnedThresholdExpr:
		return "learned_threshold " + e.Method + " of " + exprStr(e.Subject) +
			" over last " + durationStr(e.Window)
	case *ast.AggregateExpr:
		if e.Arg == nil {
			return e.Fn + "(records)"
		}
		return e.Fn + "(" + exprStr(e.Arg) + ")"
	case *ast.CallExpr:
		return e.Func + "(" + exprListStr(e.Args) + ")"
	default:
		return fmt.Sprintf("/* unsupported expr %T */", e)
	}
}

// exprStrParen wraps compound sub-expressions in parentheses so the AST
// grouping survives a round-trip. The parser discards redundant parens (they
// produce no wrapper node), so this is always safe.
func exprStrParen(e ast.Expr) string {
	switch e.(type) {
	case *ast.BinaryExpr, *ast.UnaryExpr:
		return "(" + exprStr(e) + ")"
	}
	return exprStr(e)
}

func exprListStr(es []ast.Expr) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = exprStr(e)
	}
	return strings.Join(parts, ", ")
}

func literalStr(l *ast.LiteralExpr) string {
	switch v := l.Value.(type) {
	case string:
		return quote(v)
	case float64:
		return numStr(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case ast.Duration:
		return durationStr(v)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// valueStr renders a Go value (test-datum field, mock return, arg predicate)
// as source. Distinct from literalStr because these carry native Go types
// rather than LiteralExpr nodes.
func valueStr(v any) string {
	switch v := v.(type) {
	case string:
		return quote(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return numStr(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case []any:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = valueStr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ─── primitives ─────────────────────────────────────────────────────────────

func priorityStr(pr ast.Priority) string {
	switch pr {
	case ast.PriorityLow:
		return "LOW"
	case ast.PriorityMedium:
		return "MEDIUM"
	case ast.PriorityHigh:
		return "HIGH"
	case ast.PriorityCritical:
		return "CRITICAL"
	default:
		return "LOW"
	}
}

func durationStr(d ast.Duration) string {
	return strconv.Itoa(d.Value) + " " + d.Unit
}

// numStr formats a float with the minimum digits that round-trip, and never
// in exponent form — the lexer's number scanner accepts only DIGIT+ [.DIGIT+],
// so 'e' notation (e.g. 2.6794e+06) would fail to re-lex.
func numStr(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// template re-emits the template's Raw source (not a re-render of Nodes) so
// quote-escaping and interpolation braces stay exact, per issue #73.
func template(t *ast.Template) string {
	return quote(t.Raw)
}

// quote wraps s in double quotes, re-escaping the characters the lexer
// unescapes when scanning a string literal.
func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedExprKeys(m map[string]ast.Expr) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
