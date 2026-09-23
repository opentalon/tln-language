package testrunner

import (
	"fmt"
	"strings"
	"time"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/explain"
	tlnlog "github.com/opentalon/tln-language/internal/log"
	"github.com/opentalon/tln-language/internal/mlruntime"
	"github.com/opentalon/tln-language/internal/planner"
	"github.com/opentalon/tln-language/internal/template"
)

// Decisions runs every test block and returns the per-test list of
// Tier-1 Decisions for the entities that fired. Cross-block linking
// (recommend → detect) is followed automatically.
//
// The result maps the .tln.test block name to the slice of decisions
// produced by that test's `when` clause.
func Decisions(prog *ast.Program, plans map[string]*planner.QueryPlan) map[string][]explain.Decision {
	blocks := indexBlocks(prog)
	tunings := computeTunings(prog, plans)
	out := map[string][]explain.Decision{}

	for _, b := range prog.Blocks {
		tb, ok := b.(*ast.TestBlock)
		if !ok {
			continue
		}
		entities := buildEntities(tb.Given)

		root, ok := blocks[tb.WhenBlock]
		if !ok {
			continue
		}
		decisions := buildDecisionsForBlock(root, blocks, plans, entities, time.Now().UTC(), tunings)
		out[tb.Name] = decisions
	}
	return out
}

// indexBlocks builds a name → Block map covering all top-level program
// blocks (excluding TestBlock).
func indexBlocks(prog *ast.Program) map[string]ast.Block {
	m := map[string]ast.Block{}
	for _, b := range prog.Blocks {
		if _, ok := b.(*ast.TestBlock); ok {
			continue
		}
		m[b.BlockName()] = b
	}
	return m
}

// buildDecisionsForBlock runs the named block's plan against the given
// in-memory entity set and returns one Decision per flagged entity.
// Recommend / detect / forecast / rule blocks are all handled here; the
// shape is the same.
func buildDecisionsForBlock(
	b ast.Block,
	blocks map[string]ast.Block,
	plans map[string]*planner.QueryPlan,
	entities map[int]*entity,
	now time.Time,
	tunings map[string]*tuningResult,
) []explain.Decision {
	plan, ok := plans[b.BlockName()]
	if !ok {
		return nil
	}

	flagged, evidenceByEntity, dn := executeForDecisions(plan, entities, tunings, b.BlockName())

	// Recommend blocks usually have no flagged entities of their own —
	// they fire when an upstream detect matches. Derive their flagged set
	// from the union of upstream block matches against the same entity set.
	if len(flagged) == 0 {
		for _, up := range upstreamBlockMatches(b) {
			ub, ok := blocks[up]
			if !ok {
				continue
			}
			upFlagged, upEvidence, _ := executeForDecisions(plans[up], entities, tunings, up)
			for _, id := range upFlagged {
				if _, seen := evidenceByEntity[id]; !seen {
					flagged = append(flagged, id)
					evidenceByEntity[id] = upEvidence[id]
				}
			}
			_ = ub
		}
	}

	var out []explain.Decision
	for _, id := range flagged {
		ent := entities[id]
		why := whyLines(b, ent)
		if dn.Pareto != nil {
			if sol, ok := dn.Pareto.byEntity[id]; ok {
				why = paretoWhy(sol, dn.Pareto.objs, len(dn.Pareto.byEntity))
			}
		}
		if dn.GA != nil {
			if w := gaWhy(id, *dn.GA); w != nil {
				why = w
			}
		}
		if dn.ACO != nil {
			if w := acoWhy(id, *dn.ACO); w != nil {
				why = w
			}
		}
		if dn.ILP != nil && dn.ILP.feasible {
			if w := ilpWhy(id, *dn.ILP); w != nil {
				why = w
			}
		}
		// Surface a supervised primitive's decision path — the CART splits
		// taken, or the kNN vote — so `tln explain` reads "operating_hours
		// > 3000 AND repair_count > 3" rather than just a class.
		if w := dn.Why[id]; len(w) > 0 {
			why = append(why, w...)
		}
		// Surface auto-tuning provenance into the Decision so explain output
		// can render "threshold = 2.7 (tuned via ABC on labeled fixture
		// 'foo' — F1=0.93)" instead of just the threshold value.
		if tr, ok := tunings[b.BlockName()]; ok {
			why = appendTuningWhy(why, tr)
		}
		evidence := evidenceByEntity[id]
		if tr, ok := tunings[b.BlockName()]; ok {
			evidence = appendTuningEvidence(evidence, tr)
		}

		d := explain.Decision{
			BlockName:  b.BlockName(),
			BlockKind:  blockKind(b),
			EntityID:   id,
			EntityName: entityName(ent),
			FiredAt:    now,
			Priority:   blockPriority(b),
			Why:        why,
			Evidence:   evidence,
		}
		if score, source := blockProvenance(b); score != nil || source != "" {
			d.Score = score
			d.Source = source
		}
		ctx := renderContextFor(ent, flagged, entities, now, dn.Calc, dn.Class[id])
		if tmpl := blockTemplate(b); tmpl != nil {
			d.Action = template.Render(*tmpl, ctx)
		}
		// Fire any per-row logger statements declared on this block.
		// Templates render against the same context the label uses, so
		// `{item.name}` / `{attr.x}` / `{count}` work the same way.
		for _, ls := range blockLoggers(b) {
			emitLogger(ls, b.BlockName(), id, ctx)
		}

		// Cross-block chaining: if this is a recommend whose `when`
		// references another block via `<kind> "name" matches`, recurse.
		for _, upstreamName := range upstreamBlockMatches(b) {
			up, ok := blocks[upstreamName]
			if !ok {
				continue
			}
			ups := buildDecisionsForBlock(up, blocks, plans, entities, now, tunings)
			for _, u := range ups {
				if u.EntityID == id {
					d.TriggeredBy = append(d.TriggeredBy, u)
				}
			}
			// If the upstream block fired for *any* entity but not for
			// this one (e.g. a forecast that operates on different rows),
			// still include the first upstream decision so the chain
			// stays informative.
			if len(d.TriggeredBy) == 0 && len(ups) > 0 {
				d.TriggeredBy = append(d.TriggeredBy, ups[0])
			}
		}

		out = append(out, d)
	}
	return out
}

// executeForDecisions runs the plan against the in-memory entity set and
// returns the final flagged entity IDs plus the per-entity Evidence facts
// observed during evaluation. This is a thinner variant of runOne that
// skips trace assembly and assertion checking.
//
// If the plan ends with a FuncOptimizePareto step, the per-entity evidence
// is populated with population-relative facts (rank, dominated_by, crowding)
// instead of the generic field dump; pareto carries non-nil so callers can
// render Pareto-specific "why" lines.
// decisionNarrowings collects every kind of narrowing the executor produced
// for one block's plan; per-block "why" and "evidence" pick from these in
// preference order.
type decisionNarrowings struct {
	Pareto *paretoNarrowing
	GA     *gaNarrowing
	ACO    *acoNarrowing
	ILP    *ilpNarrowing
	Calc   map[string]float64 // calculate scalars, by variable name
	Class  map[int]string     // classify_knn / predict predicted class, by entity id
	Why    map[int][]string   // supervised decision path (tree splits), by entity id
	// decide-block typed decisions, by entity id: the calibrated distribution
	// over choices and its argmax. Populated alongside Class from the
	// classify_knn explanation when the block asked for a distribution.
	Dist   map[int]map[string]float64
	Chosen map[int]string
}

func executeForDecisions(
	plan *planner.QueryPlan,
	entities map[int]*entity,
	tunings map[string]*tuningResult,
	blockName string,
) ([]int, map[int][]explain.Fact, decisionNarrowings) {
	vars := map[string]any{}
	var flagged []int
	flaggedSet := false
	dn := decisionNarrowings{Calc: map[string]float64{}, Class: map[int]string{}, Why: map[int][]string{}, Dist: map[int]map[string]float64{}, Chosen: map[int]string{}}

	for _, step := range plan.Steps {
		switch s := step.(type) {
		case *planner.FactQuery:
			if s.Reduce == "wma" {
				dn.Calc[s.Into] = mlruntime.LinearWMA(evalSeriesInMemory(s.Query, entities))
				vars[s.Into] = dn.Calc[s.Into]
				break
			}
			if len(s.Query.Aggregates) > 0 {
				if v, ok := evalAggregateInMemory(s.Query, entities); ok {
					dn.Calc[s.Into] = v
					vars[s.Into] = v
				}
				break
			}
			ids := evalQueryInMemory(s.Query, entities)
			vars[s.Into] = ids
			if !flaggedSet {
				flagged = ids
				flaggedSet = true
			}
		case *planner.MLComputation:
			// Same tuning hook as runOneTuned — when a Decision pipeline
			// crosses a detect block with `tune against test`, inject the
			// ABC-discovered parameter (anomaly threshold, learned percentile,
			// etc.) before invoking the primitive. The real registry ensures
			// Decisions reflect post-ML narrowing rather than every selector
			// candidate (which would make audit output worse than useless
			// when an ML step is present).
			reg := mlruntime.NewRegistry()
			stepToRun := s
			if tunings != nil {
				if tr, ok := tunings[blockName]; ok && tr.Function == s.Function {
					cp := *s
					cp.Params = cloneParams(s.Params)
					cp.Params[tr.ParamName] = tr.ParamValue
					stepToRun = &cp
				}
			}
			narrowed, explanations := narrowByML(reg, stepToRun, flagged, entities, vars)
			if narrowed != nil {
				flagged = narrowed
			}
			for _, e := range explanations {
				if cls, ok := e.Inputs["class"].(string); ok {
					dn.Class[e.EntityID] = cls
				}
				if chosen, ok := e.Inputs["chosen"].(string); ok {
					dn.Chosen[e.EntityID] = chosen
				}
				if probs, ok := e.Inputs["probabilities"].(map[string]float64); ok {
					dn.Dist[e.EntityID] = probs
				}
				if w := ruleWhyLines(e.Rules); len(w) > 0 {
					dn.Why[e.EntityID] = w
				}
			}
		case *planner.GoComputation:
			switch s.Function {
			case planner.FuncOptimizePareto:
				n := narrowByPareto(s, flagged, entities)
				if n.frontier != nil {
					flagged = n.frontier
				}
				dn.Pareto = &n
			case planner.FuncOptimizeGA:
				n := narrowByGA(s, flagged, entities)
				if n.flagged != nil {
					flagged = n.flagged
				}
				dn.GA = &n
			case planner.FuncOptimizeACO:
				n := narrowByACO(s, flagged, entities)
				if n.flagged != nil {
					flagged = n.flagged
				}
				dn.ACO = &n
			case planner.FuncOptimizeILP:
				n := narrowByILP(s, flagged, entities)
				if n.flagged != nil {
					flagged = n.flagged
				}
				dn.ILP = &n
			}
		case *planner.Filter:
			flagged = applyFilterConditions(s.Conditions, flagged, entities, dn.Calc)
		}
	}

	evidence := map[int][]explain.Fact{}
	if dn.Pareto != nil && dn.Pareto.byEntity != nil {
		for _, id := range flagged {
			sol, ok := dn.Pareto.byEntity[id]
			if !ok {
				continue
			}
			evidence[id] = paretoEvidence(sol, dn.Pareto.objs)
		}
		return flagged, evidence, dn
	}
	if dn.GA != nil && dn.GA.entityToSubsets != nil {
		for _, id := range flagged {
			evidence[id] = gaEvidence(id, *dn.GA)
		}
		return flagged, evidence, dn
	}
	if dn.ACO != nil && dn.ACO.posByID != nil {
		for _, id := range flagged {
			evidence[id] = acoEvidence(id, *dn.ACO)
		}
		return flagged, evidence, dn
	}
	if dn.ILP != nil && dn.ILP.feasible {
		for _, id := range flagged {
			evidence[id] = ilpEvidence(id, *dn.ILP)
		}
		return flagged, evidence, dn
	}

	for _, id := range flagged {
		ent := entities[id]
		if ent == nil {
			continue
		}
		for attr, val := range ent.fields {
			if !isInterestingAttr(attr) {
				continue
			}
			evidence[id] = append(evidence[id], explain.Fact{
				Attribute: shortAttr(attr),
				Value:     val,
			})
		}
	}
	return flagged, evidence, dn
}

// whyLines walks the block's selector conditions and produces one human-
// readable bullet per compare condition that was satisfied for the entity.
// Conditions that can't be cheaply rendered (membership, has, anomaly…)
// are summarised generically.
func whyLines(b ast.Block, ent *entity) []string {
	if ent == nil {
		return nil
	}
	var sel *ast.Selector
	switch bb := b.(type) {
	case *ast.DetectBlock:
		sel = &bb.Selector
	case *ast.RuleBlock:
		sel = bb.Selector
	case *ast.PredictBlock:
		sel = &bb.Selector
	case *ast.ForecastBlock:
		sel = &bb.Selector
	case *ast.ClusterBlock:
		sel = &bb.Selector
	case *ast.ClassifyBlock:
		sel = &bb.Selector
	case *ast.DecideBlock:
		sel = &bb.Selector
	case *ast.SimilarBlock:
		sel = &bb.Selector
	}
	if sel == nil {
		return nil
	}

	var out []string
	for _, cond := range sel.Conditions {
		out = append(out, renderConditionWhy(cond, ent)...)
	}
	return out
}

// ruleWhyLines formats a supervised primitive's decision path (the CART splits
// taken, or an anomaly bound) into human-readable "why" bullets, e.g.
// "operating_hours > 3000 (observed 3100)".
func ruleWhyLines(rules []mlruntime.Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.Attr == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s %s %v (observed %v)", r.Attr, r.Op, r.Value, r.Observed))
	}
	return out
}

func renderConditionWhy(cond ast.Condition, ent *entity) []string {
	switch c := cond.(type) {
	case *ast.CompareCondition:
		line := renderCompareWhy(c, ent)
		if line == "" {
			return nil
		}
		return []string{line}
	case *ast.LogicalCondition:
		var out []string
		out = append(out, renderConditionWhy(c.Left, ent)...)
		out = append(out, renderConditionWhy(c.Right, ent)...)
		return out
	case *ast.MembershipCondition:
		if path, ok := exprAttrPath(c.Expr); ok {
			if v, ok := ent.fields[path]; ok {
				return []string{fmt.Sprintf("%s = %v (in allowed set)", shortAttr(path), v)}
			}
		}
	case *ast.IsCondition:
		if c.Name != "" {
			return []string{fmt.Sprintf("matches define %q", c.Name)}
		}
	case *ast.PredicateCallCondition:
		return []string{fmt.Sprintf("satisfies derived %s(%s)", c.Name, c.Var)}
	}
	return nil
}

// renderCompareWhy turns a CompareCondition into a one-line human reason
// like "current_stock 12 ≤ minimum_amount 50" using the entity's actual
// observed values. Trivial selector predicates (`type == "stock_item"`,
// `status == "active"`) are dropped — they describe what kind of thing
// the rule looks at, not why this particular thing fired.
func renderCompareWhy(c *ast.CompareCondition, ent *entity) string {
	if c.Op == "==" {
		if path, ok := exprAttrPath(c.Left); ok && isBoilerplateAttr(path) {
			return ""
		}
	}
	lpath, lOk := exprAttrPath(c.Left)
	rpath, rOk := exprAttrPath(c.Right)
	op := niceOp(c.Op)

	switch {
	case lOk && rOk:
		lv, lhas := ent.fields[lpath]
		rv, rhas := ent.fields[rpath]
		if lhas && rhas {
			return fmt.Sprintf("%s %v %s %s %v", shortAttr(lpath), lv, op, shortAttr(rpath), rv)
		}
	case lOk:
		lv, lhas := ent.fields[lpath]
		rv, rok := literalValue(c.Right)
		if lhas && rok {
			return fmt.Sprintf("%s %v %s %v", shortAttr(lpath), lv, op, rv)
		}
	case rOk:
		rv, rhas := ent.fields[rpath]
		lv, lok := literalValue(c.Left)
		if lok && rhas {
			return fmt.Sprintf("%v %s %s %v", lv, op, shortAttr(rpath), rv)
		}
	}
	return ""
}

// renderContextFor builds a template.RenderContext for one matched
// entity. The per-row context flattens ":record/x" and ":attr/x" keys
// to bare `x` so templates can write `{item.name}` / `{attr.km}` /
// bare `{status}` and have them all resolve.
//
// AggregateRows carries the same flattened shape for every flagged
// entity, so `{count}` / `{total(attr.x)}` / `{avg(attr.x)}` etc. work
// without an extra FactStore round-trip.
func renderContextFor(ent *entity, flagged []int, all map[int]*entity, now time.Time, calc map[string]float64, class string) template.RenderContext {
	ctx := template.RenderContext{Now: now, Calc: calc, Class: class}
	if ent != nil {
		ctx.Row = entityToRow(ent)
	}
	if len(flagged) > 0 {
		ctx.AggregateRows = make([]template.Row, 0, len(flagged))
		for _, id := range flagged {
			if e, ok := all[id]; ok && e != nil {
				ctx.AggregateRows = append(ctx.AggregateRows, entityToRow(e))
			}
		}
	}
	return ctx
}

// entityToRow flattens the namespaced ":record/x" / ":attr/x" keys an
// entity carries into the bare-key form the template renderer wants.
// Conflicts (same bare name appearing under both namespaces) prefer the
// attr value, matching how `attr "name"` shadows `name` elsewhere.
func entityToRow(e *entity) template.Row {
	row := template.Row{}
	for k, v := range e.fields {
		switch {
		case strings.HasPrefix(k, ":record/"):
			row[strings.TrimPrefix(k, ":record/")] = v
		case strings.HasPrefix(k, ":attr/"):
			row[strings.TrimPrefix(k, ":attr/")] = v
		}
	}
	return row
}

func upstreamBlockMatches(b ast.Block) []string {
	rec, ok := b.(*ast.RecommendBlock)
	if !ok {
		return nil
	}
	var names []string
	walkAllConds(rec.When, func(c ast.Condition) {
		if bm, ok := c.(*ast.BlockMatchesCondition); ok {
			names = append(names, bm.Name)
		}
	})
	return names
}

func walkAllConds(c ast.Condition, fn func(ast.Condition)) {
	if c == nil {
		return
	}
	fn(c)
	switch v := c.(type) {
	case *ast.LogicalCondition:
		walkAllConds(v.Left, fn)
		walkAllConds(v.Right, fn)
	case *ast.NotCondition:
		walkAllConds(v.Inner, fn)
	}
}

func blockKind(b ast.Block) string {
	switch b.(type) {
	case *ast.DetectBlock:
		return "detect"
	case *ast.RecommendBlock:
		return "recommend"
	case *ast.RuleBlock:
		return "rule"
	case *ast.PredictBlock:
		return "predict"
	case *ast.ForecastBlock:
		return "forecast"
	case *ast.ClusterBlock:
		return "cluster"
	case *ast.ClassifyBlock:
		return "classify"
	case *ast.DecideBlock:
		return "decide"
	case *ast.SimilarBlock:
		return "find similar"
	case *ast.CombineBlock:
		return "combine"
	}
	return "block"
}

// blockProvenance returns the (score, source) provenance annotations
// for a block — currently supported on detect and rule blocks for
// auto-discovered / layer-3 rules. Both halves are optional; a missing
// annotation surfaces as the zero value.
func blockProvenance(b ast.Block) (*float64, string) {
	switch bb := b.(type) {
	case *ast.DetectBlock:
		return bb.Score, derefString(bb.Source)
	case *ast.RuleBlock:
		return bb.Score, derefString(bb.Source)
	}
	return nil, ""
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// blockLoggers returns the `logger.<level> "..."` statements declared
// on a block, if any. detect / rule / recommend blocks support per-row
// logger statements (issue #19). Other block types return nil — they
// either have their own logger pathway (`on { }` via the reactive
// dispatcher) or no use case yet.
func blockLoggers(b ast.Block) []*ast.LoggerAction {
	switch bb := b.(type) {
	case *ast.DetectBlock:
		return bb.Loggers
	case *ast.RuleBlock:
		return bb.Loggers
	case *ast.RecommendBlock:
		return bb.Loggers
	}
	return nil
}

// FireBlockLoggers emits the per-row logger statements declared on a
// block, given the matched entity set. Used by both the testrunner's
// run path (`tln test`) and the explain pathway (`tln explain`) so
// loggers fire consistently regardless of which entry point ran the
// rule. Returns the number of records emitted, mainly for tests.
func FireBlockLoggers(b ast.Block, flagged []int, entities map[int]*entity, now time.Time) int {
	loggers := blockLoggers(b)
	if len(loggers) == 0 {
		return 0
	}
	emitted := 0
	for _, id := range flagged {
		ent := entities[id]
		ctx := renderContextFor(ent, flagged, entities, now, nil, "")
		for _, ls := range loggers {
			emitLogger(ls, b.BlockName(), id, ctx)
			emitted++
		}
	}
	return emitted
}

// emitLogger renders one logger statement against the matched-row
// context and writes it through internal/log. The output level maps to
// the statement's declared level (info / warn / error); unknown levels
// fall back to info so a typo never silently swallows output.
//
// The structured record carries the block kind + name + entity ID so a
// downstream log aggregator can pivot on rule firings — the same
// shape #51 introduced for the internal `block_eval` events.
func emitLogger(ls *ast.LoggerAction, blockName string, entityID int, ctx template.RenderContext) {
	if ls == nil {
		return
	}
	msg := template.Render(ls.Message, ctx)
	logger := tlnlog.Default().With(
		"source", "block_logger",
		"block", blockName,
		"entity_id", entityID,
	)
	switch strings.ToLower(ls.Level) {
	case "warn", "warning":
		logger.Warn(msg)
	case "error":
		logger.Error(msg)
	default:
		logger.Info(msg)
	}
}

// blockTemplate returns the user-supplied template for a block —
// `label` for detect / predict / forecast / cluster / classify /
// similar, `suggest` for recommend, `reason` for rule. Returns nil
// when the block has no template, so callers can skip rendering
// rather than fall through to an empty action string.
func blockTemplate(b ast.Block) *ast.Template {
	switch bb := b.(type) {
	case *ast.DetectBlock:
		return bb.Label
	case *ast.RecommendBlock:
		return bb.Suggest
	case *ast.PredictBlock:
		return bb.Label
	case *ast.ForecastBlock:
		return bb.Label
	case *ast.ClusterBlock:
		return bb.Label
	case *ast.ClassifyBlock:
		return bb.Label
	case *ast.DecideBlock:
		return bb.Label
	case *ast.SimilarBlock:
		return bb.Label
	case *ast.RuleBlock:
		return bb.Reason
	}
	return nil
}

func blockPriority(b ast.Block) string {
	var pr *ast.Priority
	switch bb := b.(type) {
	case *ast.DetectBlock:
		pr = bb.Priority
	case *ast.RecommendBlock:
		pr = bb.Priority
	case *ast.RuleBlock:
		pr = bb.Priority
	case *ast.PredictBlock:
		pr = bb.Priority
	case *ast.ForecastBlock:
		pr = bb.Priority
	case *ast.ClusterBlock:
		pr = bb.Priority
	case *ast.ClassifyBlock:
		pr = bb.Priority
	case *ast.DecideBlock:
		pr = bb.Priority
	case *ast.SimilarBlock:
		pr = bb.Priority
	}
	if pr == nil {
		return ""
	}
	switch *pr {
	case ast.PriorityLow:
		return "LOW"
	case ast.PriorityMedium:
		return "MEDIUM"
	case ast.PriorityHigh:
		return "HIGH"
	case ast.PriorityCritical:
		return "CRITICAL"
	}
	return ""
}

func entityName(ent *entity) string {
	if ent == nil {
		return ""
	}
	if v, ok := ent.fields[":attr/name"]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func exprAttrPath(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.AttrExpr:
		return ":attr/" + v.Name, true
	case *ast.IdentExpr:
		switch v.Name {
		case "type":
			return ":record/type", true
		case "status":
			return ":record/status", true
		case "category":
			return ":record/category", true
		default:
			return ":record/" + v.Name, true
		}
	}
	return "", false
}

func literalValue(e ast.Expr) (any, bool) {
	lit, ok := e.(*ast.LiteralExpr)
	if !ok {
		return nil, false
	}
	return lit.Value, true
}

func niceOp(op string) string {
	switch op {
	case "<=":
		return "≤"
	case ">=":
		return "≥"
	case "==":
		return "="
	case "!=":
		return "≠"
	}
	return op
}

func shortAttr(path string) string {
	if strings.HasPrefix(path, ":attr/") {
		return path[len(":attr/"):]
	}
	if strings.HasPrefix(path, ":record/") {
		return path[len(":record/"):]
	}
	return path
}

// isInterestingAttr filters out boilerplate fields we don't want in
// Tier-1 evidence: type, status, name (already in the header).
func isInterestingAttr(attr string) bool {
	switch attr {
	case ":record/type", ":record/status", ":attr/name":
		return false
	}
	return true
}

// isBoilerplateAttr identifies attribute paths that describe what kind
// of record a rule selects against, not the substantive condition that
// fired. Used to drop noise from Why bullets.
func isBoilerplateAttr(attr string) bool {
	switch attr {
	case ":record/type", ":record/status", ":record/category":
		return true
	}
	return false
}
