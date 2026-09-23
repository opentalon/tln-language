package planner

import (
	"fmt"
	"strings"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/diagnostic"
	"github.com/opentalon/tln-language/internal/factstore"
	"github.com/opentalon/tln-language/internal/mlmodel"
)

// GoComputation function names.
const (
	FuncAnomalyZscore        = "anomaly_zscore"
	FuncAnomalyGrubbs        = "anomaly_grubbs"
	FuncLearnedThreshold     = "learned_threshold"
	FuncCorrelationPearson   = "correlation_pearson"
	FuncWMA                  = "weighted_moving_average"
	FuncPredictDecisionTree  = "predict_decision_tree"
	FuncForecastExpSmoothing = "forecast_exponential_smoothing"
	FuncClusterDBSCAN        = "cluster_dbscan"
	FuncSimilarityCosine     = "similarity_cosine"
	FuncClassifyKNN          = "classify_knn"
	FuncDecideModel          = "decide_model"
	FuncPPRTopK              = "ppr_topk"
	FuncRenderTemplate       = "render_template"
	FuncRemediateMCP         = "remediate_mcp"
	FuncEnrichMCP            = "enrich_mcp"
	FuncOptimizePareto       = "optimize_pareto"
	FuncOptimizeGA           = "optimize_ga"
	FuncOptimizeACO          = "optimize_aco"
	FuncOptimizeILP          = "optimize_ilp"
	FuncAsOfIntersect        = "asof_intersect"
	FuncFireActions          = "fire_actions"
)

// ActionsVar is the variable a rule plan's fire_actions step binds its
// resolved actions to. The executor reads it back off the scope to build
// the block result's action list.
const ActionsVar = "fired_actions"

// ─── Plan step types ──────────────────────────────────────────────────────────

// QueryPlan is the execution plan for one tln block.
type QueryPlan struct {
	BlockName string
	Steps     []PlanStep
	// Rule is the source rule block this plan compiles, set for rule plans
	// only. The runtime needs it after execution to resolve defeasible
	// conflicts between rules that matched the same row — including rules
	// with no `do` clauses, which defeat without firing anything.
	Rule *ast.RuleBlock
}

// PlanStep is implemented by FactQuery, GoComputation, MLComputation, and Filter.
type PlanStep interface {
	stepType() string
}

// FactQuery is a structured read step against any FactStore implementation.
// The planner emits these; the executor passes them to the backend. The
// Datalevin client translates the structured form to its native Datalog at
// the call boundary; MemoryStore interprets the form directly.
type FactQuery struct {
	Query    factstore.Query // structured query over patterns/predicates
	BindVars map[string]any  // parameters bound at query time (reserved)
	Into     string          // result variable name
	// AsOfDelta, when non-nil, makes this a time-travel read: the executor
	// evaluates Query against the store's state at now−AsOfDelta via the
	// TimeTraveler capability. Emitted for `was <cond> N <unit> ago`.
	AsOfDelta *ast.Duration
	// Reduce, when non-empty, marks a `calculate` step that reduces the
	// query's value column (row[1]) to a single scalar bound to Into, using
	// a non-SQL method the store can't express — today only "wma" (linearly-
	// weighted moving average, ordered by entity id, newest heaviest).
	Reduce string
	// Auxiliary marks a query whose rows feed a later step rather than being
	// the block's candidate stream — e.g. a classify block's training set.
	// The runtime binds Into but must not treat these rows as "flagged".
	Auxiliary bool
}

// GoComputation runs a non-ML Go function over a result set
// (template rendering, block-match resolution, MCP calls, combinatorial optimize).
type GoComputation struct {
	Function string         // function name constant (FuncXxx)
	Input    string         // result variable from a previous step
	Params   map[string]any // function parameters
	Into     string         // output variable name
}

// MLComputation runs an ML primitive (one of the 7 keywords).
// The result row carries an explanation alongside its value so downstream steps
// and `tln trace` can surface the decision path. See ADR-0001.
type MLComputation struct {
	Function string         // function name constant (FuncXxx, ML subset)
	Input    string         // result variable from a previous step
	Params   map[string]any // primitive parameters (features, window, series…)
	Into     string         // output variable name
}

// Filter applies a Go-side predicate to a result set. The planner
// emits a Filter step for any condition the structured Query cannot
// express (cross-attribute arithmetic, complex expressions). The
// runtime walks flagged rows and keeps those for which every
// condition evaluates true.
//
// Condition is the human-readable description retained for trace
// output; Conditions is the structured form the executor /
// testrunner actually evaluates. Old callers reading Condition keep
// working because we still populate it; the structured field
// supersedes it for execution.
type Filter struct {
	Input      string
	Condition  string          // human-readable trace label
	Conditions []ast.Condition // the predicates to apply per row
	Into       string
}

// GraphSnapshot is a plan step that asks the executor to build (or load
// from cache) a *factstore.GraphSnapshot and bind it to a variable for
// downstream PPR computation. The Options field carries the same fields
// as factstore.SnapshotOptions, but is left as map[string]any to keep
// the planner free of factstore imports — the executor unpacks it.
type GraphSnapshot struct {
	CacheKey string
	Options  map[string]any
	Into     string
}

// StateMachineStep applies a finite-state machine to every entity
// matched by Input. For each row, the executor reads the current
// state from the row's StateColumn, iterates Transitions in source
// order, and writes the target state when the first matching
// guard holds. Invariants run after the transition; violations
// attach to outcomes. Stays in the plan-step family (not
// GoComputation) because the semantics — guard ordering, conflict
// resolution, side-effects to the FactStore — are too specific to
// generic dispatch.
//
// Columns names the unqualified attribute exposed by each Find-var
// in the FactQuery row, in column order, so the executor can build
// a flat attr map for the guard evaluator without follow-up
// queries to the FactStore. Index 0 is conventionally "?e" /
// entity ID and is not in Columns; Columns starts at row[1].
type StateMachineStep struct {
	BlockName   string
	Input       string
	StateAttr   string
	StateColumn int      // index in row where the current state lives
	Columns     []string // unqualified attribute names per row column starting at index 1
	Initial     string
	States      []string
	Transitions []StateTransition
	Invariants  []StateInvariantSpec
	Into        string
}

// StateTransition is one transition arrow with its guard condition.
type StateTransition struct {
	From string
	To   string
	When ast.Condition // optional; nil = always-fires guard
}

// StateInvariantSpec is one state-scoped requirement.
type StateInvariantSpec struct {
	State    string
	Required ast.Condition
}

func (*FactQuery) stepType() string          { return "FactQuery" }
func (*GoComputation) stepType() string      { return "GoComputation" }
func (*MLComputation) stepType() string      { return "MLComputation" }
func (*Filter) stepType() string             { return "Filter" }
func (*GraphSnapshot) stepType() string      { return "GraphSnapshot" }
func (*StateMachineStep) stepType() string   { return "StateMachineStep" }
func (*EventSequenceStep) stepType() string  { return "EventSequenceStep" }
func (*RecordSequenceStep) stepType() string { return "RecordSequenceStep" }
func (*VectorSimilarStep) stepType() string  { return "VectorSimilarStep" }

// EventSequenceStep filters Input rows to those whose entity has an
// event history matching the given ordered Steps within the given
// Window (a duration in seconds; planner converts grammar units to
// canonical seconds). The executor queries the FactStore for
// `:event/name` + `:event/at` facts per candidate entity and walks
// them in time order.
type EventSequenceStep struct {
	BlockName     string
	Input         string
	Steps         []string
	WindowSeconds float64
	Into          string
}

// VectorSimilarStep narrows Input to the top-K nearest neighbours of
// the entity referenced by To, querying the talon-db HNSW index under
// Scope. To is the entity-id expression from `to record(123)` in the
// detect-style syntax; the executor reads the entity's seed vector
// from the FactStore (`:vector/<scope>` attribute) and forwards it to
// VectorSearch. Within, when non-nil, post-filters hits whose
// metric-distance exceeds the threshold.
type VectorSimilarStep struct {
	BlockName string
	Input     string
	Scope     string
	To        ast.Expr
	TopK      int
	Within    *float64
	Into      string
}

// RecordSequenceStep keeps Input rows whose entity is the grouping
// target of an ordered, time-bounded set of records. For each candidate
// row's entity id E, the executor pulls records whose
// `:record/<On>` attribute equals E and whose `:record/type` is one of
// Steps; it then walks them by `:record/at` and looks for an in-order
// match where (last.at - first.at) ≤ WindowSeconds. A WindowSeconds of
// 0 means "unbounded".
type RecordSequenceStep struct {
	BlockName     string
	Input         string
	Steps         []string
	On            string // grouping attribute key on the linking record (e.g. "item")
	WindowSeconds float64
	Into          string
}

// anomalyFunctionFor maps an `is anomaly using <METHOD>` method string to
// the planner function constant the executor dispatches on. Empty string
// (no `using` clause given) falls back to z-score for back-compat with
// the original `is anomaly compared_to ...` syntax.
func anomalyFunctionFor(method string) string {
	switch method {
	case "grubbs":
		return FuncAnomalyGrubbs
	case "", "zscore":
		return FuncAnomalyZscore
	}
	// Unknown method — validator already reported the error. Falling back
	// to z-score keeps the plan well-formed so downstream steps can run.
	return FuncAnomalyZscore
}

// IsMLFunction reports whether fn names one of the 7 ML primitives.
func IsMLFunction(fn string) bool {
	switch fn {
	case FuncAnomalyZscore,
		FuncAnomalyGrubbs,
		FuncLearnedThreshold,
		FuncCorrelationPearson,
		FuncWMA,
		FuncPredictDecisionTree,
		FuncForecastExpSmoothing,
		FuncClusterDBSCAN,
		FuncSimilarityCosine,
		FuncClassifyKNN,
		FuncPPRTopK:
		return true
	}
	return false
}

// QueryOf returns the structured query for a FactQuery step. Kept as a
// helper so external callers don't dereference the field directly (room
// to swap representations later).
func QueryOf(q *FactQuery) factstore.Query {
	return q.Query
}

// ─── Entry point ──────────────────────────────────────────────────────────────

// Plan compiles a validated AST into per-block QueryPlans.
// define blocks are inlined at compile time and produce no plan of their own.
func Plan(prog *ast.Program) (map[string]*QueryPlan, diagnostic.List) {
	return PlanWithModels(prog, nil)
}

// PlanWithModels is Plan with an optional registry of Go-provided ML models.
// `using model "name"` references resolve against tln `model`/`module`
// blocks first, then this registry — so a host can serve models it built in
// Go under the same qualified names tln source uses.
func PlanWithModels(prog *ast.Program, goModels *mlmodel.Registry) (map[string]*QueryPlan, diagnostic.List) {
	// Inline cached `threshold "name"` references to their literal value before
	// planning, so the query builder and Go condition evaluator only ever see
	// plain numbers — the resolution is a single lookup, not a per-eval series
	// walk (issue #74). Undeclared refs are left intact for the validator.
	resolveThresholdRefs(prog, collectThresholds(prog))
	p := &planner{
		prog:    prog,
		defines: collectDefines(prog),
		derives: collectDerives(prog),
		models:  mlmodel.NewResolver(collectModels(prog), goModels),
	}
	return p.planAll()
}

// collectModels indexes every tln `model` block by the name a reference
// uses: top-level models by bare name, and `module "ns"` members by their
// qualified `ns.name`.
func collectModels(prog *ast.Program) map[string]*mlmodel.Model {
	m := map[string]*mlmodel.Model{}
	add := func(name string, b *ast.ModelBlock) {
		m[name] = mlmodel.FromAST(b, attrVarName)
	}
	for _, b := range prog.Blocks {
		switch bb := b.(type) {
		case *ast.ModelBlock:
			add(bb.Name, bb)
		case *ast.ModuleBlock:
			for _, mem := range bb.Members {
				if mb, ok := mem.(*ast.ModelBlock); ok {
					add(bb.QualifiedName(mb.Name), mb)
				}
			}
		}
	}
	return m
}

// collectThresholds indexes cached threshold blocks by name → value.
func collectThresholds(prog *ast.Program) map[string]float64 {
	m := map[string]float64{}
	for _, b := range prog.Blocks {
		if t, ok := b.(*ast.ThresholdBlock); ok {
			m[t.Name] = t.Value
		}
	}
	return m
}

// resolveThresholdRefs rewrites every `threshold "name"` expression to a
// LiteralExpr of the cached value, in place across all blocks. Refs to names
// with no declared block are left as ThresholdRefExpr for the validator to
// report.
func resolveThresholdRefs(prog *ast.Program, th map[string]float64) {
	if len(th) == 0 {
		return
	}
	for _, b := range prog.Blocks {
		rewriteBlockThresholds(b, th)
	}
}

func rewriteBlockThresholds(b ast.Block, th map[string]float64) {
	sel := func(s *ast.Selector) {
		for _, c := range s.Conditions {
			rewriteThresholdCond(c, th)
		}
	}
	conds := func(cs []ast.Condition) {
		for _, c := range cs {
			rewriteThresholdCond(c, th)
		}
	}
	switch bb := b.(type) {
	case *ast.DetectBlock:
		sel(&bb.Selector)
		conds(bb.Having)
		for i := range bb.Calculate {
			conds(bb.Calculate[i].Where)
		}
	case *ast.RuleBlock:
		if bb.Selector != nil {
			sel(bb.Selector)
		}
		if bb.When != nil {
			rewriteThresholdCond(bb.When, th)
		}
	case *ast.RecommendBlock:
		if bb.When != nil {
			rewriteThresholdCond(bb.When, th)
		}
		for i := range bb.Calculate {
			conds(bb.Calculate[i].Where)
		}
	case *ast.ConstraintBlock:
		sel(&bb.Selector)
		if bb.Require != nil {
			rewriteThresholdCond(bb.Require, th)
		}
	case *ast.PredictBlock:
		sel(&bb.Selector)
		if bb.TrainedOn != nil {
			conds(bb.TrainedOn.Conditions)
		}
	case *ast.ClassifyBlock:
		sel(&bb.Selector)
		if bb.TrainedOn != nil {
			conds(bb.TrainedOn.Conditions)
		}
	case *ast.DecideBlock:
		sel(&bb.Selector)
		if bb.TrainedOn != nil {
			conds(bb.TrainedOn.Conditions)
		}
	case *ast.ForecastBlock:
		sel(&bb.Selector)
		if bb.When != nil {
			rewriteThresholdCond(bb.When, th)
		}
		bb.Series.Attr = rewriteThresholdExpr(bb.Series.Attr, th)
		if bb.Predict != nil {
			rewriteThresholdCond(bb.Predict.Condition, th)
		}
	case *ast.ClusterBlock:
		sel(&bb.Selector)
		for i := range bb.ByAttrs {
			bb.ByAttrs[i] = rewriteThresholdExpr(bb.ByAttrs[i], th)
		}
	case *ast.SimilarBlock:
		sel(&bb.Selector)
		bb.To = rewriteThresholdExpr(bb.To, th)
	case *ast.RelatedBlock:
		sel(&bb.Selector)
		bb.To = rewriteThresholdExpr(bb.To, th)
		for i := range bb.Seeds {
			bb.Seeds[i] = rewriteThresholdExpr(bb.Seeds[i], th)
		}
	case *ast.CombineBlock:
		sel(&bb.Selector)
		for i := range bb.Optimize {
			bb.Optimize[i].Attr = rewriteThresholdExpr(bb.Optimize[i].Attr, th)
		}
		for i := range bb.Constraints {
			bb.Constraints[i].Left = rewriteThresholdExpr(bb.Constraints[i].Left, th)
			bb.Constraints[i].Right = rewriteThresholdExpr(bb.Constraints[i].Right, th)
		}
	case *ast.StateMachineBlock:
		sel(&bb.Selector)
		for i := range bb.Transitions {
			if bb.Transitions[i].When != nil {
				rewriteThresholdCond(bb.Transitions[i].When, th)
			}
		}
		for i := range bb.Invariants {
			rewriteThresholdCond(bb.Invariants[i].Required, th)
		}
	case *ast.EnrichBlock:
		sel(&bb.Selector)
	case *ast.DeriveBlock:
		sel(&bb.Selector)
	case *ast.DefineBlock:
		conds(bb.Conditions)
		if bb.ForEach != nil {
			conds(bb.ForEach.Body)
			bb.ForEach.Over = rewriteThresholdExpr(bb.ForEach.Over, th)
		}
	}
}

func rewriteThresholdCond(c ast.Condition, th map[string]float64) {
	switch cc := c.(type) {
	case *ast.LogicalCondition:
		rewriteThresholdCond(cc.Left, th)
		rewriteThresholdCond(cc.Right, th)
	case *ast.NotCondition:
		rewriteThresholdCond(cc.Inner, th)
	case *ast.AsOfCondition:
		rewriteThresholdCond(cc.Inner, th)
	case *ast.CompareCondition:
		cc.Left = rewriteThresholdExpr(cc.Left, th)
		cc.Right = rewriteThresholdExpr(cc.Right, th)
	case *ast.MembershipCondition:
		cc.Expr = rewriteThresholdExpr(cc.Expr, th)
		for i := range cc.Members {
			cc.Members[i] = rewriteThresholdExpr(cc.Members[i], th)
		}
	case *ast.StringMatchCondition:
		cc.Subject = rewriteThresholdExpr(cc.Subject, th)
	case *ast.TemporalCondition:
		cc.Subject = rewriteThresholdExpr(cc.Subject, th)
	case *ast.IsCondition:
		cc.Subject = rewriteThresholdExpr(cc.Subject, th)
	case *ast.AnomalyCondition:
		cc.Subject = rewriteThresholdExpr(cc.Subject, th)
	case *ast.CorrelationCondition:
		cc.Left = rewriteThresholdExpr(cc.Left, th)
		cc.Right = rewriteThresholdExpr(cc.Right, th)
	case *ast.ChangedToCondition:
		cc.Value = rewriteThresholdExpr(cc.Value, th)
	}
}

func rewriteThresholdExpr(e ast.Expr, th map[string]float64) ast.Expr {
	switch ex := e.(type) {
	case nil:
		return nil
	case *ast.ThresholdRefExpr:
		if v, ok := th[ex.Name]; ok {
			return &ast.LiteralExpr{Value: v}
		}
		return ex
	case *ast.BinaryExpr:
		ex.Left = rewriteThresholdExpr(ex.Left, th)
		ex.Right = rewriteThresholdExpr(ex.Right, th)
		return ex
	case *ast.UnaryExpr:
		ex.Operand = rewriteThresholdExpr(ex.Operand, th)
		return ex
	case *ast.ListExpr:
		for i := range ex.Elements {
			ex.Elements[i] = rewriteThresholdExpr(ex.Elements[i], th)
		}
		return ex
	default:
		return e
	}
}

type planner struct {
	prog    *ast.Program
	defines map[string]*ast.DefineBlock
	derives map[string]*ast.DeriveBlock
	models  *mlmodel.Resolver
	diags   diagnostic.List
}

func collectDefines(prog *ast.Program) map[string]*ast.DefineBlock {
	m := map[string]*ast.DefineBlock{}
	for _, b := range prog.Blocks {
		if def, ok := b.(*ast.DefineBlock); ok {
			m[def.Name] = def
		}
	}
	return m
}

func collectDerives(prog *ast.Program) map[string]*ast.DeriveBlock {
	m := map[string]*ast.DeriveBlock{}
	for _, b := range prog.Blocks {
		if d, ok := b.(*ast.DeriveBlock); ok {
			m[d.Name] = d
		}
	}
	return m
}

func (p *planner) planAll() (map[string]*QueryPlan, diagnostic.List) {
	plans := map[string]*QueryPlan{}
	for _, b := range p.prog.Blocks {
		switch bb := b.(type) {
		case *ast.DefineBlock:
			// defines are inlined; no standalone plan
		case *ast.DetectBlock:
			plans[bb.Name] = p.planDetect(bb)
		case *ast.RuleBlock:
			plans[bb.Name] = p.planRule(bb)
		case *ast.RecommendBlock:
			plans[bb.Name] = p.planRecommend(bb)
		case *ast.PredictBlock:
			plans[bb.Name] = p.planPredictBlock(bb)
		case *ast.ForecastBlock:
			plans[bb.Name] = p.planForecastBlock(bb)
		case *ast.ClusterBlock:
			plans[bb.Name] = p.planClusterBlock(bb)
		case *ast.ClassifyBlock:
			plans[bb.Name] = p.planClassifyBlock(bb)
		case *ast.DecideBlock:
			plans[bb.Name] = p.planDecideBlock(bb)
		case *ast.SimilarBlock:
			plans[bb.Name] = p.planSimilarBlock(bb)
		case *ast.RelatedBlock:
			plans[bb.Name] = p.planRelatedBlock(bb)
		case *ast.CombineBlock:
			plans[bb.Name] = p.planCombine(bb)
		case *ast.WorkflowBlock:
			plans[bb.Name] = p.planWorkflow(bb)
		case *ast.StateMachineBlock:
			plans[bb.Name] = p.planStateMachine(bb)
		case *ast.EnrichBlock:
			plans[bb.Name] = p.planEnrich(bb)
		case *ast.ThresholdBlock:
			// Cached data, not an evaluable block — its value is inlined into
			// referencing expressions during resolveThresholdRefs; no plan.
		case *ast.DeriveBlock:
			// A derived predicate is inlined into referencing queries via
			// addPredicateCall; it has no standalone plan (like define).
		}
	}
	return plans, p.diags
}

// ─── Block planners ───────────────────────────────────────────────────────────

func (p *planner) planDetect(b *ast.DetectBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}

	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)

	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	last := "candidates"

	// Event-sequence conditions need per-entity event history lookups;
	// they can't ride on Filter (no FactStore access there). Each
	// condition becomes one EventSequenceStep — runs in the order the
	// selector wrote them.
	for i, es := range qb.eventSeqConds {
		into := fmt.Sprintf("eventseq_%d", i)
		plan.Steps = append(plan.Steps, &EventSequenceStep{
			BlockName:     b.Name,
			Input:         last,
			Steps:         append([]string(nil), es.Steps...),
			WindowSeconds: durationToSeconds(es.Window),
			Into:          into,
		})
		last = into
	}

	// Record-sequence conditions run after event-sequence narrowing.
	// Each condition becomes one RecordSequenceStep; output of step N
	// is input of step N+1, so multiple sequences AND together by
	// chaining narrowing operations.
	for i, rs := range qb.recordSeqConds {
		into := fmt.Sprintf("recordseq_%d", i)
		on := rs.On
		if on == "" {
			on = "item"
		}
		plan.Steps = append(plan.Steps, &RecordSequenceStep{
			BlockName:     b.Name,
			Input:         last,
			Steps:         append([]string(nil), rs.Steps...),
			On:            on,
			WindowSeconds: durationToSeconds(rs.Window),
			Into:          into,
		})
		last = into
	}

	// Time-travel conditions: for each `was (<inner>) N <unit> ago`, read
	// the inner condition against the store's past state (now−Delta) and
	// intersect with the running candidate set on entity ID. Chaining
	// intersects ANDs multiple `was…ago` conjuncts together.
	last = p.appendAsOfSteps(plan, qb.asOfConds, last)

	if len(qb.goConditions) > 0 {
		plan.Steps = append(plan.Steps, &Filter{
			Input:      last,
			Condition:  renderGoConditions(qb.goConditions),
			Conditions: append([]ast.Condition(nil), qb.goConditions...),
			Into:       "filtered",
		})
		last = "filtered"
	}

	for i, ab := range qb.anomalyConds {
		into := fmt.Sprintf("anomaly_%d", i)
		plan.Steps = append(plan.Steps, &MLComputation{
			Function: anomalyFunctionFor(ab.Method),
			Input:    last,
			Params: map[string]any{
				"attr":        ab.AttrName,
				"window":      ab.Window,
				"value_index": indexOf(qb.findVars, ab.ValueVar),
			},
			Into: into,
		})
		last = into
	}

	for i, tb := range qb.thresholdConds {
		into := fmt.Sprintf("threshold_%d", i)
		plan.Steps = append(plan.Steps, &MLComputation{
			Function: FuncLearnedThreshold,
			Input:    last,
			Params: map[string]any{
				"attr":        tb.AttrName,
				"method":      tb.Method,
				"op":          tb.Op,
				"window":      tb.Window,
				"value_index": indexOf(qb.findVars, tb.ValueVar),
			},
			Into: into,
		})
		last = into
	}

	for i, cb := range qb.correlationConds {
		into := fmt.Sprintf("correlation_%d", i)
		plan.Steps = append(plan.Steps, &MLComputation{
			Function: FuncCorrelationPearson,
			Input:    last,
			Params: map[string]any{
				"attr_x":        cb.AttrNameX,
				"attr_y":        cb.AttrNameY,
				"op":            cb.Op,
				"threshold":     cb.Threshold,
				"window":        cb.Window,
				"value_index_x": indexOf(qb.findVars, cb.ValueVarX),
				"value_index_y": indexOf(qb.findVars, cb.ValueVarY),
			},
			Into: into,
		})
		last = into
	}

	if b.Anomaly != nil {
		plan.Steps = append(plan.Steps, &MLComputation{
			Function: anomalyFunctionFor(b.Anomaly.Method),
			Input:    last,
			Params:   map[string]any{"window": b.Anomaly.Window},
			Into:     "anomaly_results",
		})
		last = "anomaly_results"
	}

	if b.Predict != nil {
		plan.Steps = append(plan.Steps, &MLComputation{
			Function: FuncPredictDecisionTree,
			Input:    last,
			Params:   map[string]any{"features": b.Predict.Features},
			Into:     "predictions",
		})
		last = "predictions"
	}

	if b.Forecast != nil {
		plan.Steps = append(plan.Steps, &MLComputation{
			Function: FuncForecastExpSmoothing,
			Input:    last,
			Params:   map[string]any{"series": b.Forecast.Series},
			Into:     "forecast_results",
		})
		last = "forecast_results"
	}

	if b.Related != nil {
		plan.Steps = append(plan.Steps, &GraphSnapshot{
			CacheKey: b.Name,
			Options:  map[string]any{},
			Into:     "graph",
		})
		plan.Steps = append(plan.Steps, &MLComputation{
			Function: FuncPPRTopK,
			Input:    last,
			Params: map[string]any{
				"graph_var":      "graph",
				"seed_expr":      b.Related.To,
				"seeds_expr":     b.Related.Seeds,
				"top_k":          ptrOrNil(b.Related.TopK),
				"damping":        ptrOrNil(b.Related.Damping),
				"flag_predicate": "topk",
			},
			Into: "related_records",
		})
		last = "related_records"
	}

	// calculate + having: reduce a series to scalars bound to their names,
	// then narrow candidates with a post-aggregation filter that may
	// reference those scalars. Both run before labeling so `{calc_var}`
	// resolves and the filter has taken effect.
	p.appendCalcSteps(plan, b.Calculate, last)
	if len(b.Having) > 0 {
		plan.Steps = append(plan.Steps, &Filter{
			Input:      last,
			Condition:  renderGoConditions(b.Having),
			Conditions: append([]ast.Condition(nil), b.Having...),
			Into:       "having_filtered",
		})
		last = "having_filtered"
	}

	if b.Label != nil {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncRenderTemplate,
			Input:    last,
			Params:   map[string]any{"template": b.Label.Raw},
			Into:     "detections",
		})
	}

	// remediate: fire MCP side effects once per flagged row. Runs on the
	// narrowed candidate set (entity ids), independent of labeling.
	if b.Remediate != nil {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncRemediateMCP,
			Input:    last,
			Params: map[string]any{
				"body":       b.Remediate.Body,
				"block_name": b.Name,
				"mode":       b.Remediate.Mode,
				"role":       b.Remediate.Role,
				"batch":      b.Remediate.Batch,
			},
			Into: "remediations",
		})
	}

	return plan
}

// planEnrich compiles an enrich block: select candidate records, then a
// single enrich_mcp step that (per stale record) calls the MCP tool and
// asserts the mapped response fields back. Staleness is evaluated in the
// executor via the FactStore's Freshness capability.
func (p *planner) planEnrich(b *ast.EnrichBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	plan.Steps = append(plan.Steps, &GoComputation{
		Function: FuncEnrichMCP,
		Input:    "candidates",
		Params: map[string]any{
			"call":        b.Call,
			"updates":     b.Updates,
			"stale_after": b.StaleAfter,
			"block_name":  b.Name,
		},
		Into: "enrichments",
	})
	return plan
}

func (p *planner) planRule(b *ast.RuleBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name, Rule: b}
	qb := p.newQueryBuilder()
	if b.Selector != nil {
		qb.addSelector(*b.Selector)
	} else if b.When != nil {
		qb.addCondition(b.When)
	}
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	last := "candidates"
	if len(qb.goConditions) > 0 {
		plan.Steps = append(plan.Steps, &Filter{
			Input:      last,
			Condition:  renderGoConditions(qb.goConditions),
			Conditions: append([]ast.Condition(nil), qb.goConditions...),
			Into:       "policy_candidates",
		})
		last = "policy_candidates"
	}

	// do: resolve the rule's action clauses against every matched row. The
	// engine hands the result back; it executes nothing itself.
	if len(b.Do) > 0 {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncFireActions,
			Input:    last,
			Params:   map[string]any{"rule": b},
			Into:     ActionsVar,
		})
	}
	return plan
}

func (p *planner) planRecommend(b *ast.RecommendBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}

	// Reference the matched detect/predict result
	if bmc, ok := b.When.(*ast.BlockMatchesCondition); ok {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: "resolve_block_matches",
			Input:    bmc.Name,
			Params:   map[string]any{"kind": bmc.Kind},
			Into:     "matches",
		})
	}

	// calculate steps
	p.appendCalcSteps(plan, b.Calculate, "matches")

	if b.Suggest != nil {
		params := map[string]any{"template": b.Suggest.Raw}
		if b.SuggestProbability > 0 && b.SuggestProbability < 1 {
			params["probability"] = b.SuggestProbability
			params["block_name"] = b.Name
		}
		// Feedback window turns the prior probability into a
		// Beta-posterior: executor queries the FactStore for
		// recent accept/reject facts and adjusts the sample
		// rate at fire time. Trace IDs are minted per fired
		// suggestion so the host can correlate user actions back
		// to the suggestion that prompted them.
		if b.FeedbackWindowDays > 0 {
			params["feedback_window_days"] = b.FeedbackWindowDays
		}
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncRenderTemplate,
			Input:    "matches",
			Params:   params,
			Into:     "suggestions",
		})
	}

	// remediate: fire MCP side effects once per matched row.
	if b.Remediate != nil {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncRemediateMCP,
			Input:    "matches",
			Params: map[string]any{
				"body":       b.Remediate.Body,
				"block_name": b.Name,
				"mode":       b.Remediate.Mode,
				"role":       b.Remediate.Role,
				"batch":      b.Remediate.Batch,
			},
			Into: "remediations",
		})
	}
	return plan
}

func (p *planner) planPredictBlock(b *ast.PredictBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	params := map[string]any{
		"features":         b.Features,
		"feature_names":    exprListToAttrNames(b.Features),
		"label_attr":       b.LabelAttr,
		"training_var":     "training",
		"max_depth":        defaultMaxDepth,
		"min_samples_leaf": defaultMinSamplesLeaf,
	}

	switch {
	case b.UsingModel != "":
		// `using model "ns.name"` — walk the model's inline fitted tree; no
		// training query. Resolved from tln or Go providers.
		model, provider, ok := p.models.Resolve(b.UsingModel)
		if !ok {
			p.diags.AddError("", b.Pos.Line, b.Pos.Col,
				fmt.Sprintf("predict %q: unknown model %q (no tln `model` block or registered Go model)", b.Name, b.UsingModel), "")
			break
		}
		params["feature_names"] = model.Features
		params["fitted_tree"] = model.Tree
		params["model_name"] = b.UsingModel
		params["model_provider"] = provider

	case b.TrainedOn != nil:
		// The CART tree trains on a second, labeled query — the same
		// supervised shape classify uses (ADR-0006 / ADR-0007). Auxiliary
		// keeps these rows out of the candidate stream; the runtime binds
		// them into Input.Training.
		tqb := p.newQueryBuilder()
		tqb.addSelector(ast.Selector{Target: "records", Conditions: b.TrainedOn.Conditions})
		plan.Steps = append(plan.Steps, &FactQuery{
			Query:     tqb.build(),
			BindVars:  tqb.bindVars(),
			Into:      "training",
			Auxiliary: true,
		})
	}

	if b.Confidence != nil {
		params["confidence"] = *b.Confidence
	}
	plan.Steps = append(plan.Steps, &MLComputation{
		Function: FuncPredictDecisionTree,
		Input:    "candidates",
		Params:   params,
		Into:     "predictions",
	})
	if b.Label != nil {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncRenderTemplate,
			Input:    "predictions",
			Params:   map[string]any{"template": b.Label.Raw},
			Into:     "results",
		})
	}
	return plan
}

// CART stop conditions when a predict block doesn't override them (issue #71).
const (
	defaultMaxDepth       = 5
	defaultMinSamplesLeaf = 5
)

func (p *planner) planForecastBlock(b *ast.ForecastBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	forecastParams := map[string]any{
		"series":     b.Series,
		"series_var": attrVarName(b.Series.Attr),
	}
	// Translate the optional `predict days_until value <= 0` clause into
	// primitive-friendly predicate + threshold params so the runtime
	// doesn't have to walk the AST.
	if b.Predict != nil {
		if op, thr, ok := forecastPredicateOf(b.Predict.Condition); ok {
			forecastParams["predicate"] = op
			forecastParams["threshold"] = thr
		}
	}
	plan.Steps = append(plan.Steps, &MLComputation{
		Function: FuncForecastExpSmoothing,
		Input:    "candidates",
		Params:   forecastParams,
		Into:     "forecasts",
	})
	if b.Label != nil {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncRenderTemplate,
			Input:    "forecasts",
			Params:   map[string]any{"template": b.Label.Raw},
			Into:     "results",
		})
	}
	return plan
}

func (p *planner) planClusterBlock(b *ast.ClusterBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	plan.Steps = append(plan.Steps, &MLComputation{
		Function: FuncClusterDBSCAN,
		Input:    "candidates",
		Params: map[string]any{
			"by":       b.ByAttrs,
			"features": exprListToAttrNames(b.ByAttrs),
		},
		Into: "clusters",
	})
	return plan
}

func (p *planner) planClassifyBlock(b *ast.ClassifyBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})

	params := map[string]any{
		"features":      b.Features,
		"feature_names": exprListToAttrNames(b.Features),
		"label_attr":    b.LabelAttr,
		"training_var":  "training",
		"k":             defaultKNN,
	}

	switch {
	case b.UsingModel != "":
		// `using model "ns.name"` — the model's inline fitted examples are the
		// training set, resolved at plan time from tln or Go providers. No
		// training query is emitted; the fitted rows ride in the params.
		model, provider, ok := p.models.Resolve(b.UsingModel)
		if !ok {
			p.diags.AddError("", b.Pos.Line, b.Pos.Col,
				fmt.Sprintf("classify %q: unknown model %q (no tln `model` block or registered Go model)", b.Name, b.UsingModel), "")
			break
		}
		params["feature_names"] = model.Features
		params["k"] = model.K
		params["fitted_rows"] = model.TrainingRows()
		params["model_name"] = b.UsingModel
		params["model_provider"] = provider

	case b.TrainedOn != nil:
		// Supervised primitives need a second query: the labeled training set
		// the vote draws from. The runtime resolves "training" into
		// Input.Training before the MLComputation runs.
		tqb := p.newQueryBuilder()
		tqb.addSelector(ast.Selector{Target: "records", Conditions: b.TrainedOn.Conditions})
		plan.Steps = append(plan.Steps, &FactQuery{
			Query:     tqb.build(),
			BindVars:  tqb.bindVars(),
			Into:      "training",
			Auxiliary: true,
		})
	}

	if b.Confidence != nil {
		params["confidence"] = *b.Confidence
	}
	plan.Steps = append(plan.Steps, &MLComputation{
		Function: FuncClassifyKNN,
		Input:    "candidates",
		Params:   params,
		Into:     "classifications",
	})
	return plan
}

// defaultKNN is the neighbour count when a classify block doesn't override it.
// Small, matching the per-tenant data volumes tln sees (issue #70).
const defaultKNN = 5

// planDecideBlock compiles a `decide` block into a query plan. The two modes
// diverge on the compute step:
//
//	deterministic (features + trained_on): reuse the classify_knn MLComputation,
//	  adding `choices` and `emit_distribution` so the primitive attaches the full
//	  per-choice vote distribution. The training set rides in an auxiliary query,
//	  exactly like classify.
//	model (ask + using model): a decide_model GoComputation the executor
//	  dispatches at runtime through the injected ToolResolver.
func (p *planner) planDecideBlock(b *ast.DecideBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})

	choices := exprListToStrings(b.Choices)

	if b.Mode() == "model" {
		params := map[string]any{
			"ask":        b.Ask,
			"model":      b.UsingModel,
			"choices":    choices,
			"block_name": b.Name,
		}
		if b.Confidence != nil {
			params["confidence"] = *b.Confidence
		}
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncDecideModel,
			Input:    "candidates",
			Params:   params,
			Into:     "decisions",
		})
		return plan
	}

	// Deterministic mode: reuse classify_knn, but ask it to emit the full
	// per-choice distribution.
	params := map[string]any{
		"features":          b.Features,
		"feature_names":     exprListToAttrNames(b.Features),
		"label_attr":        b.LabelAttr,
		"training_var":      "training",
		"k":                 defaultKNN,
		"choices":           choices,
		"emit_distribution": true,
	}
	tqb := p.newQueryBuilder()
	tqb.addSelector(ast.Selector{Target: "records", Conditions: b.TrainedOn.Conditions})
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:     tqb.build(),
		BindVars:  tqb.bindVars(),
		Into:      "training",
		Auxiliary: true,
	})
	if b.Confidence != nil {
		params["confidence"] = *b.Confidence
	}
	plan.Steps = append(plan.Steps, &MLComputation{
		Function: FuncClassifyKNN,
		Input:    "candidates",
		Params:   params,
		Into:     "decisions",
	})
	return plan
}

func (p *planner) planSimilarBlock(b *ast.SimilarBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	if b.VectorScope != "" {
		// HNSW-backed path: the executor asks the talon-db backend for
		// the top-K nearest neighbours of b.To inside the named scope.
		// Default K matches the existing cosine path's "no limit"
		// implicit behaviour by going high; explicit `top N` overrides.
		k := 10
		if b.TopK != nil {
			k = *b.TopK
		}
		plan.Steps = append(plan.Steps, &VectorSimilarStep{
			BlockName: b.Name,
			Input:     "candidates",
			Scope:     b.VectorScope,
			To:        b.To,
			TopK:      k,
			Within:    b.Within,
			Into:      "similar_records",
		})
		return plan
	}
	plan.Steps = append(plan.Steps, &MLComputation{
		Function: FuncSimilarityCosine,
		Input:    "candidates",
		Params: map[string]any{
			"to":       b.To,
			"within":   b.Within,
			"features": []string{attrVarName(b.To)},
		},
		Into: "similar_records",
	})
	return plan
}

func (p *planner) planRelatedBlock(b *ast.RelatedBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)
	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	plan.Steps = append(plan.Steps, &GraphSnapshot{
		CacheKey: b.Name,
		Options:  map[string]any{},
		Into:     "graph",
	})
	plan.Steps = append(plan.Steps, &MLComputation{
		Function: FuncPPRTopK,
		Input:    "candidates",
		Params: map[string]any{
			"graph_var":      "graph",
			"seed_expr":      b.To,
			"seeds_expr":     b.Seeds,
			"top_k":          ptrOrNil(b.TopK),
			"damping":        ptrOrNil(b.Damping),
			"tolerance":      ptrOrNil(b.Tol),
			"max_iterations": ptrOrNil(b.MaxIter),
			"flag_predicate": "topk",
		},
		Into: "related_records",
	})
	if b.Label != nil {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncRenderTemplate,
			Input:    "related_records",
			Params:   map[string]any{"template": b.Label.Raw},
			Into:     "results",
		})
	}
	return plan
}

// ptrOrNil returns the pointed-to value as any, or nil if the pointer is
// nil. Used to pack optional planner params without nil-typed map entries.
func ptrOrNil[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func (p *planner) planCombine(b *ast.CombineBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)

	switch {
	case b.Sequence:
		return p.planCombineACO(plan, qb, b)
	case b.Solver == "linear":
		return p.planCombineILP(plan, qb, b)
	case b.Select != nil:
		return p.planCombineGA(plan, qb, b)
	default:
		return p.planCombinePareto(plan, qb, b)
	}
}

// planCombinePareto emits the v1 plan: bind each objective attr (must be
// *AttrExpr) into the :find clause and call optimize_pareto on the rows.
func (p *planner) planCombinePareto(plan *QueryPlan, qb *queryBuilder, b *ast.CombineBlock) *QueryPlan {
	indices := make([]int, 0, len(b.Optimize))
	for _, oc := range b.Optimize {
		attr, ok := oc.Attr.(*ast.AttrExpr)
		if !ok {
			indices = append(indices, -1)
			continue
		}
		v := qb.varFor(attr.Name)
		qb.addPattern(":attr/"+sanitizeIdent(attr.Name), factstore.Var(v))
		qb.findVars = appendUniq(qb.findVars, v)
		indices = append(indices, indexOf(qb.findVars, v))
	}

	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})
	plan.Steps = append(plan.Steps, &GoComputation{
		Function: FuncOptimizePareto,
		Input:    "candidates",
		Params: map[string]any{
			"objectives":              b.Optimize,
			"return":                  b.Return,
			"block":                   b.Name,
			"find_vars":               qb.findVars,
			"objective_value_indices": indices,
		},
		Into: "frontier",
	})
	return plan
}

// planCombineGA emits the v2 plan: bind every attr referenced by objectives
// and constraints into the Datalog query, then call optimize_ga with the
// subset size and constraint specs. The GA step receives the rows along with
// a name→column-index map so it can evaluate aggregates per candidate subset.
func (p *planner) planCombineGA(plan *QueryPlan, qb *queryBuilder, b *ast.CombineBlock) *QueryPlan {
	attrIndices := map[string]int{}
	bindAttr := func(name string) {
		if _, seen := attrIndices[name]; seen {
			return
		}
		v := qb.varFor(name)
		qb.addPattern(":attr/"+sanitizeIdent(name), factstore.Var(v))
		qb.findVars = appendUniq(qb.findVars, v)
		attrIndices[name] = indexOf(qb.findVars, v)
	}

	for _, oc := range b.Optimize {
		if name, ok := refAttrName(oc.Attr); ok {
			bindAttr(name)
		}
	}
	for _, c := range b.Constraints {
		if name, ok := refAttrName(c.Left); ok {
			bindAttr(name)
		}
	}

	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})

	seed := int64(0)
	if b.Seed != nil {
		seed = *b.Seed
	}

	plan.Steps = append(plan.Steps, &GoComputation{
		Function: FuncOptimizeGA,
		Input:    "candidates",
		Params: map[string]any{
			"objectives":   b.Optimize,
			"constraints":  b.Constraints,
			"select_size":  b.Select.Size,
			"seed":         seed,
			"return":       b.Return,
			"block":        b.Name,
			"find_vars":    qb.findVars,
			"attr_indices": attrIndices,
		},
		Into: "frontier",
	})
	return plan
}

// planCombineACO emits the routing plan: bind the two coordinate attrs into
// the find clause and dispatch to optimize_aco which builds a distance
// matrix and runs ant-colony search.
func (p *planner) planCombineACO(plan *QueryPlan, qb *queryBuilder, b *ast.CombineBlock) *QueryPlan {
	xAttr, _ := b.Coordinates.X.(*ast.AttrExpr)
	yAttr, _ := b.Coordinates.Y.(*ast.AttrExpr)
	attrIndices := map[string]int{}
	if xAttr != nil {
		bindCombineAttr(qb, xAttr.Name, attrIndices)
	}
	if yAttr != nil {
		bindCombineAttr(qb, yAttr.Name, attrIndices)
	}

	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})

	seed := int64(0)
	if b.Seed != nil {
		seed = *b.Seed
	}

	plan.Steps = append(plan.Steps, &GoComputation{
		Function: FuncOptimizeACO,
		Input:    "candidates",
		Params: map[string]any{
			"x_attr":       xAttrName(xAttr),
			"y_attr":       xAttrName(yAttr),
			"seed":         seed,
			"return":       b.Return,
			"block":        b.Name,
			"find_vars":    qb.findVars,
			"attr_indices": attrIndices,
		},
		Into: "tour",
	})
	return plan
}

// planCombineILP emits the exact-solver plan: same Datalevin binding as the
// GA path, but dispatches to optimize_ilp which solves the single-objective
// 0/1 subset problem via branch-and-bound.
func (p *planner) planCombineILP(plan *QueryPlan, qb *queryBuilder, b *ast.CombineBlock) *QueryPlan {
	attrIndices := map[string]int{}
	for _, oc := range b.Optimize {
		if name, ok := refAttrName(oc.Attr); ok {
			bindCombineAttr(qb, name, attrIndices)
		}
	}
	for _, c := range b.Constraints {
		if name, ok := refAttrName(c.Left); ok {
			bindCombineAttr(qb, name, attrIndices)
		}
	}

	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})

	plan.Steps = append(plan.Steps, &GoComputation{
		Function: FuncOptimizeILP,
		Input:    "candidates",
		Params: map[string]any{
			"objectives":   b.Optimize,
			"constraints":  b.Constraints,
			"select_size":  b.Select.Size,
			"return":       b.Return,
			"block":        b.Name,
			"find_vars":    qb.findVars,
			"attr_indices": attrIndices,
		},
		Into: "frontier",
	})
	return plan
}

func bindCombineAttr(qb *queryBuilder, name string, attrIndices map[string]int) {
	if _, seen := attrIndices[name]; seen {
		return
	}
	v := qb.varFor(name)
	qb.addPattern(":attr/"+sanitizeIdent(name), factstore.Var(v))
	qb.findVars = appendUniq(qb.findVars, v)
	attrIndices[name] = indexOf(qb.findVars, v)
}

func xAttrName(a *ast.AttrExpr) string {
	if a == nil {
		return ""
	}
	return a.Name
}

// refAttrName returns the underlying attr name an objective or constraint
// expression references (either a bare *AttrExpr or an *AggregateExpr
// wrapping one). Aggregates of records (e.g. count(records)) return ("", false).
func refAttrName(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.AttrExpr:
		return x.Name, true
	case *ast.AggregateExpr:
		if a, ok := x.Arg.(*ast.AttrExpr); ok {
			return a.Name, true
		}
	}
	return "", false
}

func (p *planner) planWorkflow(b *ast.WorkflowBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}
	sorted := topoSortSteps(b.Steps)
	for _, step := range sorted {
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: "mcp_call",
			Input:    "",
			Params: map[string]any{
				"step":       step.Name,
				"depends_on": step.DependsOn,
				"mcp":        step.MCPCall,
				"when":       step.When,
			},
			Into: step.Name + "_result",
		})
	}
	return plan
}

// planStateMachine compiles a state_machine block into a two-step
// plan: a FactQuery that returns every entity matching the selector
// plus every attribute any guard or invariant references, followed
// by a StateMachineStep that drives those entities through the
// declared transitions.
//
// Walking transition guards up front means the executor never
// needs follow-up per-entity attr queries — the row from the
// FactQuery already carries every attribute the guard evaluator
// will read. That's the difference between a state_machine block
// running in O(1 query) vs O(N queries) on a thousand-entity store.
//
// Transitions fire in source order — first matching guard wins.
// State-attribute defaults to ":record/state" when unset.
func (p *planner) planStateMachine(b *ast.StateMachineBlock) *QueryPlan {
	plan := &QueryPlan{BlockName: b.Name}

	qb := p.newQueryBuilder()
	qb.addSelector(b.Selector)

	stateAttr := b.StateAttr
	if stateAttr == "" {
		stateAttr = ":record/state"
	} else if stateAttr[0] != ':' {
		stateAttr = ":attr/" + stateAttr
	}
	stateVar := qb.varFor("state")
	qb.addPattern(stateAttr, factstore.Var(stateVar))
	qb.findVars = appendUniq(qb.findVars, stateVar)
	// Index of the state column in the resulting row. findVars[0] is
	// the entity binding "?e", so the state lands at len(findVars)-1
	// after we appended stateVar.
	stateCol := len(qb.findVars) - 1

	// Collect every attribute referenced by any guard or invariant,
	// so the FactQuery returns those as additional columns and the
	// guard evaluator can read them without a follow-up query.
	attrNames := collectAttrNames(b)
	columns := make([]string, 0, len(attrNames))
	for _, name := range attrNames {
		path := ":attr/" + name
		v := qb.varFor("attr_" + name)
		qb.addPattern(path, factstore.Var(v))
		qb.findVars = appendUniq(qb.findVars, v)
		columns = append(columns, name)
	}

	plan.Steps = append(plan.Steps, &FactQuery{
		Query:    qb.build(),
		BindVars: qb.bindVars(),
		Into:     "candidates",
	})

	stateNames := make([]string, 0, len(b.States))
	for _, s := range b.States {
		stateNames = append(stateNames, s.Name)
	}
	transitions := make([]StateTransition, 0, len(b.Transitions))
	for _, t := range b.Transitions {
		transitions = append(transitions, StateTransition{From: t.From, To: t.To, When: t.When})
	}
	invariants := make([]StateInvariantSpec, 0, len(b.Invariants))
	for _, inv := range b.Invariants {
		invariants = append(invariants, StateInvariantSpec{State: inv.State, Required: inv.Required})
	}

	plan.Steps = append(plan.Steps, &StateMachineStep{
		BlockName:   b.Name,
		Input:       "candidates",
		StateAttr:   stateAttr,
		StateColumn: stateCol,
		Columns:     columns,
		Initial:     b.Initial,
		States:      stateNames,
		Transitions: transitions,
		Invariants:  invariants,
		Into:        "sm_result",
	})
	return plan
}

// collectAttrNames walks every guard and invariant condition in the
// block to find AttrExpr / IdentExpr references. The returned slice
// is deduped and stable-ordered so the FactQuery's columns line up
// across runs (tests rely on this).
func collectAttrNames(b *ast.StateMachineBlock) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, t := range b.Transitions {
		walkAttrRefs(t.When, add)
	}
	for _, inv := range b.Invariants {
		walkAttrRefs(inv.Required, add)
	}
	return out
}

// walkAttrRefs visits a condition tree and calls `add` for every
// AttrExpr.Name it finds. Conservative: covers the conditions
// state-machine guards actually use today (compare, logical, not,
// membership, string-match). Anything else is silently skipped —
// at runtime the executor would just see an empty attr map for
// those refs and the guard would fail; that's a defensive default
// matching what other planners do for unfamiliar AST shapes.
func walkAttrRefs(c ast.Condition, add func(string)) {
	switch cc := c.(type) {
	case *ast.LogicalCondition:
		walkAttrRefs(cc.Left, add)
		walkAttrRefs(cc.Right, add)
	case *ast.NotCondition:
		walkAttrRefs(cc.Inner, add)
	case *ast.CompareCondition:
		walkExprAttrs(cc.Left, add)
		walkExprAttrs(cc.Right, add)
	case *ast.MembershipCondition:
		walkExprAttrs(cc.Expr, add)
	case *ast.StringMatchCondition:
		walkExprAttrs(cc.Subject, add)
	case *ast.HasCondition:
		walkExprAttrs(cc.Subject, add)
		add(cc.Type)
	}
}

func walkExprAttrs(e ast.Expr, add func(string)) {
	switch ee := e.(type) {
	case *ast.AttrExpr:
		add(ee.Name)
	case *ast.IdentExpr:
		add(ee.Name)
	case *ast.BinaryExpr:
		walkExprAttrs(ee.Left, add)
		walkExprAttrs(ee.Right, add)
	case *ast.CallExpr:
		for _, a := range ee.Args {
			walkExprAttrs(a, add)
		}
	}
}

// topoSortSteps returns workflow steps in topological order using Kahn's algorithm.
// The validator guarantees no cycles, so this always succeeds.
func topoSortSteps(steps []ast.WorkflowStep) []ast.WorkflowStep {
	byName := map[string]ast.WorkflowStep{}
	inDegree := map[string]int{}
	for _, s := range steps {
		byName[s.Name] = s
		inDegree[s.Name] = 0
	}
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			inDegree[s.Name]++
			_ = dep // dep → s edge
		}
	}

	var queue []string
	for _, s := range steps {
		if inDegree[s.Name] == 0 {
			queue = append(queue, s.Name)
		}
	}

	// Build adjacency: dep → []dependents
	adj := map[string][]string{}
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			adj[dep] = append(adj[dep], s.Name)
		}
	}

	var sorted []ast.WorkflowStep
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		sorted = append(sorted, byName[name])
		for _, next := range adj[name] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return sorted
}

// ─── Query builder ────────────────────────────────────────────────────────────

type queryBuilder struct {
	defines          map[string]*ast.DefineBlock
	derives          map[string]*ast.DeriveBlock
	deriving         map[string]bool // predicate names currently being inlined (cycle guard)
	entityVar        string
	findVars         []string
	whereClauses     []factstore.Clause
	rules            []factstore.Rule
	goConditions     []ast.Condition
	anomalyConds     []anomalyBinding
	thresholdConds   []thresholdBinding
	correlationConds []correlationBinding
	eventSeqConds    []*ast.EventSequenceCondition
	recordSeqConds   []*ast.RecordSequenceCondition
	asOfConds        []*ast.AsOfCondition
	usedVars         map[string]int // base name → count (for dedup)
}

// pattern is a builder helper for an EAV match clause where the entity is
// bound to the builder's entity variable.
func (b *queryBuilder) pattern(attr string, value factstore.Term) *factstore.Pattern {
	return &factstore.Pattern{
		Entity:    factstore.Term{Var: b.entityVar},
		Attribute: attr,
		Value:     value,
	}
}

// addPattern appends a value-binding pattern. `value` is a variable name
// (will become Term.Var) or a literal Go value (becomes Term.Literal).
func (b *queryBuilder) addPattern(attr string, value factstore.Term) {
	b.whereClauses = append(b.whereClauses, b.pattern(attr, value))
}

// addPredicate appends a comparison or string-match predicate.
func (b *queryBuilder) addPredicate(op string, left, right factstore.Term) {
	b.whereClauses = append(b.whereClauses, &factstore.Predicate{
		Op:    op,
		Left:  left,
		Right: right,
	})
}

// anomalyBinding records an `attr X is anomaly` clause lifted out of the
// Datalog selector into an MLComputation step. The Datalog query is widened
// to also return the bound value var so the primitive can score it.
type anomalyBinding struct {
	AttrPath string       // ":attr/weekly_consumption"
	AttrName string       // "weekly_consumption" — what label templates show
	ValueVar string       // "?weekly_consumption"
	Method   string       // "zscore" (default), "grubbs"
	Window   ast.Duration // 12 weeks, 30 days…
}

// thresholdBinding records an `attr X OP learned_threshold ...` compare clause
// lifted out of the Datalog selector into an MLComputation step.
type thresholdBinding struct {
	AttrPath string
	AttrName string
	ValueVar string
	Method   string       // "p95"
	Op       string       // ">", "<", ">=", "<="
	Window   ast.Duration // recorded for explanation/audit; not enforced in phase 2
}

// correlationBinding records an `attr X correlates_with attr Y ...` clause
// lifted into an MLComputation. Unlike anomaly / threshold it binds TWO
// value vars into the :find clause so the primitive receives both series.
type correlationBinding struct {
	AttrNameX string
	ValueVarX string
	AttrNameY string
	ValueVarY string
	Method    string // "pearson"
	Op        string
	Threshold float64
	Window    ast.Duration
}

// appendAsOfSteps emits, per as-of condition, a time-travel FactQuery for
// the inner condition at now−Delta plus an intersect GoComputation that
// keeps only running candidates whose entity ID also matched the past
// query. Returns the new "last" variable name. The inner condition is
// assumed Datalog-expressible (enforced by the validator); any residual
// go-side conditions on the inner builder are dropped.
func (p *planner) appendAsOfSteps(plan *QueryPlan, conds []*ast.AsOfCondition, last string) string {
	for i, ao := range conds {
		inner := p.newQueryBuilder()
		inner.addCondition(ao.Inner)
		delta := ao.Delta
		asofVar := fmt.Sprintf("asof_%d", i)
		plan.Steps = append(plan.Steps, &FactQuery{
			Query:     inner.build(),
			BindVars:  inner.bindVars(),
			AsOfDelta: &delta,
			Into:      asofVar,
		})
		into := fmt.Sprintf("asof_intersected_%d", i)
		plan.Steps = append(plan.Steps, &GoComputation{
			Function: FuncAsOfIntersect,
			Input:    last,
			Params:   map[string]any{"with": asofVar},
			Into:     into,
		})
		last = into
	}
	return last
}

func (p *planner) newQueryBuilder() *queryBuilder {
	return &queryBuilder{
		defines:   p.defines,
		derives:   p.derives,
		deriving:  map[string]bool{},
		entityVar: "?e",
		findVars:  []string{"?e"},
		usedVars:  map[string]int{},
	}
}

func (b *queryBuilder) varFor(base string) string {
	base = sanitizeIdent(base)
	if _, seen := b.usedVars[base]; !seen {
		b.usedVars[base] = 0
		return "?" + base
	}
	b.usedVars[base]++
	return fmt.Sprintf("?%s_%d", base, b.usedVars[base])
}

func (b *queryBuilder) bindVars() map[string]any {
	return map[string]any{} // runtime injects entity_id; nothing static here
}

func (b *queryBuilder) addSelector(sel ast.Selector) {
	for _, cond := range sel.Conditions {
		b.addCondition(cond)
	}
}

func (b *queryBuilder) addCondition(cond ast.Condition) {
	if cond == nil {
		return
	}
	switch c := cond.(type) {
	case *ast.LogicalCondition:
		b.addLogical(c)
	case *ast.NotCondition:
		b.addNot(c)
	case *ast.CompareCondition:
		b.addCompare(c)
	case *ast.MembershipCondition:
		b.addMembership(c)
	case *ast.IsCondition:
		b.addIsCondition(c)
	case *ast.PredicateCallCondition:
		b.addPredicateCall(c)
	case *ast.StringMatchCondition:
		b.addStringMatch(c)
	case *ast.AnomalyCondition:
		b.addAnomalyCondition(c)
	case *ast.CorrelationCondition:
		b.addCorrelationCondition(c)
	case *ast.EventSequenceCondition:
		b.eventSeqConds = append(b.eventSeqConds, c)
	case *ast.RecordSequenceCondition:
		b.recordSeqConds = append(b.recordSeqConds, c)
	case *ast.AsOfCondition:
		b.asOfConds = append(b.asOfConds, c)
	default:
		// TemporalCondition, ChangedToCondition, HasCondition — cannot express
		// in Datalog, defer to Go.
		b.goConditions = append(b.goConditions, cond)
	}
}

func (b *queryBuilder) addLogical(c *ast.LogicalCondition) {
	if c.Op == "and" {
		b.addCondition(c.Left)
		b.addCondition(c.Right)
		return
	}
	// OR: collect sub-clauses from each side in a nested builder
	left := b.subClauses(c.Left)
	right := b.subClauses(c.Right)
	b.whereClauses = append(b.whereClauses, &factstore.Or{
		Branches: [][]factstore.Clause{left, right},
	})
}

func (b *queryBuilder) addNot(c *ast.NotCondition) {
	// Evaluate the inner in isolation first: if it is fully store-expressible,
	// negate it as a set difference (`factstore.Not`) — the fast path, correct
	// for Datalog-only bodies like `not (attr "km" > 50000)` or a derived
	// predicate whose body is all patterns/membership.
	child := &queryBuilder{
		defines:   b.defines,
		derives:   b.derives,
		deriving:  copyBoolMap(b.deriving),
		entityVar: b.entityVar,
		usedVars:  copyMap(b.usedVars),
	}
	child.addCondition(c.Inner)
	if len(child.goConditions) == 0 {
		b.whereClauses = append(b.whereClauses, &factstore.Not{Body: child.whereClauses})
		return
	}

	// Mixed body: the inner also needs a Go-side (per-row) condition, typically
	// arithmetic over two attributes. Splitting the negation — Datalog part into
	// the store's `Not`, Go part merged out as a *positive* filter — flips the
	// sign of the arithmetic and silently miscompiles. Evaluate the whole
	// negation per-row instead, inlining any derived predicate into its body so
	// the constraint evaluator (which doesn't know `pred(v)`) can run it.
	inlined, ok := b.inlineForGoEval(c.Inner)
	if ok {
		b.goConditions = append(b.goConditions, &ast.NotCondition{Inner: inlined})
		return
	}
	// Inner uses a construct the per-row evaluator can't run standalone; fall
	// back to the store-only negation (drops the Go part) — no worse than the
	// prior behaviour, and the block_eval row count keeps it observable.
	b.whereClauses = append(b.whereClauses, &factstore.Not{Body: child.whereClauses})
}

// inlineForGoEval rewrites cond into an equivalent tree with every derived
// predicate replaced by its (recursively inlined) body, combined with `and`,
// so the whole thing can be handed to the per-row constraint evaluator. It
// returns ok=false on any construct that evaluator can't run standalone —
// notably a derived-predicate reference the caller couldn't resolve — so the
// caller can fall back rather than emit a condition that always errors.
func (b *queryBuilder) inlineForGoEval(cond ast.Condition) (ast.Condition, bool) {
	switch c := cond.(type) {
	case *ast.PredicateCallCondition:
		d, ok := b.derives[c.Name]
		if !ok || b.deriving[c.Name] {
			return nil, false
		}
		b.deriving[c.Name] = true
		defer delete(b.deriving, c.Name)
		var acc ast.Condition
		for _, bc := range d.Selector.Conditions {
			sub, ok := b.inlineForGoEval(bc)
			if !ok {
				return nil, false
			}
			acc = andConds(acc, sub)
		}
		return acc, acc != nil
	case *ast.LogicalCondition:
		l, ok := b.inlineForGoEval(c.Left)
		if !ok {
			return nil, false
		}
		r, ok := b.inlineForGoEval(c.Right)
		if !ok {
			return nil, false
		}
		return &ast.LogicalCondition{Op: c.Op, Left: l, Right: r}, true
	case *ast.NotCondition:
		inner, ok := b.inlineForGoEval(c.Inner)
		if !ok {
			return nil, false
		}
		return &ast.NotCondition{Inner: inner}, true
	case *ast.CompareCondition, *ast.MembershipCondition, *ast.StringMatchCondition, *ast.TemporalCondition:
		// Directly evaluable per-row by internal/constraints.
		return cond, true
	default:
		return nil, false
	}
}

func andConds(a, b ast.Condition) ast.Condition {
	if a == nil {
		return b
	}
	return &ast.LogicalCondition{Op: "and", Left: a, Right: b}
}

func copyBoolMap(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (b *queryBuilder) subClauses(cond ast.Condition) []factstore.Clause {
	child := &queryBuilder{
		defines:   b.defines,
		derives:   b.derives,
		deriving:  b.deriving,
		entityVar: b.entityVar,
		usedVars:  copyMap(b.usedVars),
	}
	child.addCondition(cond)
	// merge any go conditions back up
	b.goConditions = append(b.goConditions, child.goConditions...)
	return child.whereClauses
}

func (b *queryBuilder) addCompare(c *ast.CompareCondition) {
	if lt, ok := c.Right.(*ast.LearnedThresholdExpr); ok {
		b.addLearnedThresholdCompare(c, lt)
		return
	}

	leftPath, leftIsField := exprToFieldPath(c.Left)
	rightPath, rightIsField := exprToFieldPath(c.Right)
	leftLitVal, leftIsLit := exprToGoLiteral(c.Left)
	rightLitVal, rightIsLit := exprToGoLiteral(c.Right)

	switch {
	case leftIsField && rightIsLit:
		if c.Op == "==" {
			// Direct value binding: pattern matches the literal value.
			b.addPattern(leftPath, factstore.Lit(rightLitVal))
		} else {
			// Bind to a variable, then constrain via predicate.
			v := b.varFor(attrVarName(c.Left))
			b.addPattern(leftPath, factstore.Var(v))
			b.findVars = appendUniq(b.findVars, v)
			b.addPredicate(c.Op, factstore.Var(v), factstore.Lit(rightLitVal))
		}

	case leftIsField && rightIsField:
		// attr1 OP attr2
		lv := b.varFor(attrVarName(c.Left))
		rv := b.varFor(attrVarName(c.Right))
		b.addPattern(leftPath, factstore.Var(lv))
		b.addPattern(rightPath, factstore.Var(rv))
		b.findVars = appendUniq(b.findVars, lv, rv)
		b.addPredicate(c.Op, factstore.Var(lv), factstore.Var(rv))

	case leftIsLit && rightIsField:
		// literal OP attr (reversed) — bind the attr, predicate on the literal.
		rv := b.varFor(attrVarName(c.Right))
		b.addPattern(rightPath, factstore.Var(rv))
		b.findVars = appendUniq(b.findVars, rv)
		b.addPredicate(c.Op, factstore.Lit(leftLitVal), factstore.Var(rv))

	default:
		// Complex expr (binary arithmetic, context refs, etc.) → Go
		b.goConditions = append(b.goConditions, c)
	}
}

func (b *queryBuilder) addMembership(c *ast.MembershipCondition) {
	path, ok := exprToFieldPath(c.Expr)
	if !ok {
		b.goConditions = append(b.goConditions, c)
		return
	}
	// `attr in category_tree("Root")` — push the recursion into the
	// FactStore as a Datalog rule. Datalevin evaluates it natively;
	// MemoryStore precomputes the rule extension via fixed-point.
	if len(c.Members) == 1 {
		if ct, isCT := c.Members[0].(*ast.CategoryTreeExpr); isCT {
			b.addCategoryTreeMembership(path, ct, c.Negated)
			return
		}
	}
	v := b.varFor(attrVarName(c.Expr))
	b.addPattern(path, factstore.Var(v))
	b.findVars = appendUniq(b.findVars, v)

	members := make([]any, 0, len(c.Members))
	for _, m := range c.Members {
		if val, ok := exprToGoLiteral(m); ok {
			members = append(members, val)
		}
	}
	op := "in"
	if c.Negated {
		op = "not_in"
	}
	b.addPredicate(op, factstore.Var(v), factstore.Lit(members))
}

// addCategoryTreeMembership emits a Datalog rule that walks the
// category hierarchy and a RuleCall clause that anchors a row when
// the entity's category attribute is `Root` or one of its
// descendants. Schema assumed (matches tln's `category` blocks):
//
//	[<entity>     :record/category <category-name>]
//	[<cat-entity> :record/type     "category"]
//	[<cat-entity> :category/name   <name>]
//	[<cat-entity> :category/parent <parent-name>]
//
// The emitted rule has two clauses (Datalog disjunction):
//
//	(category-in-tree ?c ?root) ← [(= ?c ?root)]
//	(category-in-tree ?c ?root) ← [?cent :record/type "category"]
//	                              [?cent :category/name ?c]
//	                              [?cent :category/parent ?p]
//	                              (category-in-tree ?p ?root)
//
// The membership query then adds:
//
//	[?e :record/category ?cat]
//	(category-in-tree ?cat "Root")
func (b *queryBuilder) addCategoryTreeMembership(attr string, ct *ast.CategoryTreeExpr, negated bool) {
	if negated {
		// "not in category_tree(...)" is uncommon and the rule-based
		// path doesn't compose with negation cleanly; fall back to
		// the in-process filter the original implementation used.
		b.goConditions = append(b.goConditions, &ast.MembershipCondition{
			Expr:    nil,
			Negated: true,
			Members: []ast.Expr{ct},
		})
		return
	}
	b.ensureCategoryTreeRule()
	catVar := b.varFor("category_value")
	b.addPattern(attr, factstore.Var(catVar))
	b.findVars = appendUniq(b.findVars, catVar)
	b.whereClauses = append(b.whereClauses, &factstore.RuleCall{
		Name: "category-in-tree",
		Args: []factstore.Term{factstore.Var(catVar), factstore.Lit(ct.Root)},
	})
}

// ensureCategoryTreeRule registers the recursive category-in-tree
// rule on the builder the first time category_tree(...) is used in
// the block. Subsequent invocations are no-ops so the same query
// doesn't carry duplicate rule definitions.
func (b *queryBuilder) ensureCategoryTreeRule() {
	for _, r := range b.rules {
		if r.Name == "category-in-tree" {
			return
		}
	}
	// Both rule heads start with the same `:category/name ?c` pattern
	// so ?c is bound to an actual category name in the store before
	// any equality check. Datalevin's rule projector NPEs when a base
	// rule's body is just `[(= ?c ?root)]` (free vars passed straight
	// from the call site) — anchoring ?c to a positive pattern first
	// gives the engine a concrete value to project.
	base := factstore.Rule{
		Name: "category-in-tree",
		Args: []string{"?c", "?root"},
		Body: []factstore.Clause{
			&factstore.Pattern{Entity: factstore.Var("cent"), Attribute: ":category/name", Value: factstore.Var("c")},
			&factstore.Predicate{Op: "=", Left: factstore.Var("c"), Right: factstore.Var("root")},
		},
	}
	step := factstore.Rule{
		Name: "category-in-tree",
		Args: []string{"?c", "?root"},
		Body: []factstore.Clause{
			&factstore.Pattern{Entity: factstore.Var("cent"), Attribute: ":category/name", Value: factstore.Var("c")},
			&factstore.Pattern{Entity: factstore.Var("cent"), Attribute: ":category/parent", Value: factstore.Var("p")},
			&factstore.RuleCall{Name: "category-in-tree", Args: []factstore.Term{factstore.Var("p"), factstore.Var("root")}},
		},
	}
	b.rules = append(b.rules, base, step)
}

func (b *queryBuilder) addIsCondition(c *ast.IsCondition) {
	// inline the define's conditions
	if def, ok := b.defines[c.Name]; ok {
		for _, dc := range def.Conditions {
			b.addCondition(dc)
		}
	}
	// if no define found, validator already reported the error
}

// addPredicateCall inlines a derived predicate's body conditions into the
// referencing query — the same mechanism as `is`/define, so the derivation
// reads exactly like an asserted fact (and the full condition language,
// including arithmetic, flows through the normal Datalog + Go-filter split).
// The validator rejects cycles, so inlining terminates; the `deriving` guard
// is defence-in-depth against a missed cycle.
func (b *queryBuilder) addPredicateCall(c *ast.PredicateCallCondition) {
	if b.deriving[c.Name] {
		return // cycle — validator already reported it
	}
	d, ok := b.derives[c.Name]
	if !ok {
		return // undeclared — validator already reported it
	}
	b.deriving[c.Name] = true
	defer delete(b.deriving, c.Name)
	for _, cond := range d.Selector.Conditions {
		b.addCondition(cond)
	}
}

// addLearnedThresholdCompare lifts `attr X OP learned_threshold ...` out of
// the selector. The query is widened to return the bound value var so the
// primitive can compute the percentile and per-row decision.
func (b *queryBuilder) addLearnedThresholdCompare(c *ast.CompareCondition, lt *ast.LearnedThresholdExpr) {
	path, ok := exprToFieldPath(c.Left)
	if !ok {
		b.goConditions = append(b.goConditions, c)
		return
	}
	attrName := attrVarName(c.Left)
	v := b.varFor(attrName)
	b.addPattern(path, factstore.Var(v))
	b.findVars = appendUniq(b.findVars, v)
	b.thresholdConds = append(b.thresholdConds, thresholdBinding{
		AttrPath: path,
		AttrName: attrName,
		ValueVar: v,
		Method:   lt.Method,
		Op:       c.Op,
		Window:   lt.Window,
	})
}

// addAnomalyCondition lifts `attr X is anomaly` out of the selector: it
// binds the value var into the :find clause so the resulting rows carry
// the numeric series, and records a binding the planner uses to emit an
// MLComputation step after the query.
func (b *queryBuilder) addAnomalyCondition(c *ast.AnomalyCondition) {
	path, ok := exprToFieldPath(c.Subject)
	if !ok {
		b.goConditions = append(b.goConditions, c)
		return
	}
	attrName := attrVarName(c.Subject)
	v := b.varFor(attrName)
	b.addPattern(path, factstore.Var(v))
	b.findVars = appendUniq(b.findVars, v)
	b.anomalyConds = append(b.anomalyConds, anomalyBinding{
		AttrPath: path,
		AttrName: attrName,
		ValueVar: v,
		Method:   c.Method,
		Window:   c.Window,
	})
}

// addCorrelationCondition lifts `attr X correlates_with attr Y ...` out of
// the selector, binding BOTH value vars into the :find clause so the
// primitive receives the two parallel series, and records a binding the
// planner turns into an MLComputation step.
func (b *queryBuilder) addCorrelationCondition(c *ast.CorrelationCondition) {
	pathX, okX := exprToFieldPath(c.Left)
	pathY, okY := exprToFieldPath(c.Right)
	if !okX || !okY {
		b.goConditions = append(b.goConditions, c)
		return
	}
	nameX := attrVarName(c.Left)
	nameY := attrVarName(c.Right)
	vX := b.varFor(nameX)
	vY := b.varFor(nameY)
	b.addPattern(pathX, factstore.Var(vX))
	b.addPattern(pathY, factstore.Var(vY))
	b.findVars = appendUniq(b.findVars, vX, vY)
	b.correlationConds = append(b.correlationConds, correlationBinding{
		AttrNameX: nameX,
		ValueVarX: vX,
		AttrNameY: nameY,
		ValueVarY: vY,
		Method:    c.Method,
		Op:        c.Op,
		Threshold: c.Threshold,
		Window:    c.Window,
	})
}

func (b *queryBuilder) addStringMatch(c *ast.StringMatchCondition) {
	if c.Op == "matches" || c.Op == "matches_phrase" {
		// Full-text search. When the subject resolves to a concrete
		// attribute path, scope the FTS predicate to that attribute
		// (`(fulltext $ :attr "q")` — faster on Datalevin and matches
		// the user's intent). Otherwise the search is entity-wide.
		ft := &factstore.FullText{Entity: factstore.Var("e")}
		if path, ok := exprToFieldPath(c.Subject); ok {
			ft.Attribute = path
		}
		if c.Op == "matches_phrase" {
			// Datalevin's search expression form: require the literal
			// as an exact phrase. Quotes inside the phrase need not be
			// escaped today because the lexer's STRING strips them and
			// we re-wrap.
			ft.Expr = fmt.Sprintf(`[:and {:phrase %q}]`, c.Value)
			// Backends without structured search expressions
			// (MemoryStore, talon-db) evaluate Query instead — a
			// contiguous case-insensitive substring scan, which is the
			// phrase semantics they can express. Without this the
			// clause silently matched nothing there.
			ft.Query = c.Value
		} else {
			ft.Query = c.Value
		}
		b.whereClauses = append(b.whereClauses, ft)
		return
	}
	path, ok := exprToFieldPath(c.Subject)
	if !ok {
		b.goConditions = append(b.goConditions, c)
		return
	}
	v := b.varFor(attrVarName(c.Subject))
	b.addPattern(path, factstore.Var(v))
	b.findVars = appendUniq(b.findVars, v)
	b.addPredicate(c.Op, factstore.Var(v), factstore.Lit(c.Value))
}

// build emits the structured Query the planner attaches to each FactQuery
// plan step. Backends never see Datalog text — the Datalevin client owns
// the only translator that turns this back into wire-format Datalog.
func (b *queryBuilder) build() factstore.Query {
	return factstore.Query{
		Find:  append([]string(nil), b.findVars...),
		Where: append([]factstore.Clause(nil), b.whereClauses...),
		Rules: append([]factstore.Rule(nil), b.rules...),
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// exprToFieldPath maps a tln expression to a Datalevin attribute path.
func exprToFieldPath(e ast.Expr) (path string, ok bool) {
	switch v := e.(type) {
	case *ast.IdentExpr:
		switch v.Name {
		case "type":
			return ":record/type", true
		case "status":
			return ":record/status", true
		case "category":
			return ":record/category", true
		default:
			return ":record/" + sanitizeIdent(v.Name), true
		}
	case *ast.AttrExpr:
		return ":attr/" + sanitizeIdent(v.Name), true
	}
	return "", false
}

// exprToGoLiteral extracts the underlying Go value from a literal AST node.
// Returns (value, true) when the expression is a literal; (nil, false)
// otherwise. The structured Query model carries Go values directly, so the
// translator (Datalevin) or interpreter (MemoryStore) decides on rendering.
func exprToGoLiteral(e ast.Expr) (any, bool) {
	lit, ok := e.(*ast.LiteralExpr)
	if !ok {
		return nil, false
	}
	return lit.Value, lit.Value != nil
}

// attrVarName returns a Datalevin variable base name for an expression.
func attrVarName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.AttrExpr:
		return v.Name
	case *ast.IdentExpr:
		return v.Name
	}
	return "val"
}

// exprListToAttrNames flattens a slice of AST expressions into the bare
// attribute names they reference. Used to pre-resolve ML primitive
// params at plan time so the runtime stays decoupled from the AST.
// Expressions that aren't simple attr/ident references are dropped.
func exprListToAttrNames(exprs []ast.Expr) []string {
	out := make([]string, 0, len(exprs))
	for _, e := range exprs {
		name := attrVarName(e)
		if name != "" && name != "val" {
			out = append(out, name)
		}
	}
	return out
}

// exprListToStrings extracts the string values from a list of string-literal
// expressions (a `choices [ ... ]` clause). Non-literal entries are skipped;
// the validator has already required string literals.
func exprListToStrings(exprs []ast.Expr) []string {
	out := make([]string, 0, len(exprs))
	for _, e := range exprs {
		if lit, ok := e.(*ast.LiteralExpr); ok {
			if s, ok := lit.Value.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// forecastPredicateOf extracts the comparison operator + numeric
// threshold from a `predict days_until value <op> <number>` clause. The
// forecast primitive uses these to know when to stop projecting.
// Returns (op, threshold, ok=true) on success.
func forecastPredicateOf(cond ast.Condition) (string, float64, bool) {
	cc, ok := cond.(*ast.CompareCondition)
	if !ok {
		return "", 0, false
	}
	lit, ok := cc.Right.(*ast.LiteralExpr)
	if !ok {
		return "", 0, false
	}
	switch v := lit.Value.(type) {
	case float64:
		return cc.Op, v, true
	case int:
		return cc.Op, float64(v), true
	case int64:
		return cc.Op, float64(v), true
	}
	return "", 0, false
}

func sanitizeIdent(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "_"), "-", "_")
}

func renderGoConditions(conds []ast.Condition) string {
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		parts = append(parts, fmt.Sprintf("/* %T */", c))
	}
	return strings.Join(parts, " && ")
}

// durationToSeconds converts a grammar Duration (e.g. {Value: 7,
// Unit: "days"}) to canonical seconds. Used by EventSequenceStep
// to bound its sliding window. Unknown units fall through to 0 so
// `within 7 km` (which doesn't make temporal sense) effectively
// disables the window check — validator should catch that earlier.
func durationToSeconds(d ast.Duration) float64 {
	switch d.Unit {
	case "days":
		return float64(d.Value) * 86400
	case "weeks":
		return float64(d.Value) * 7 * 86400
	case "months":
		return float64(d.Value) * 30 * 86400
	case "years":
		return float64(d.Value) * 365 * 86400
	}
	return 0
}

// buildCalculateQuery builds the aggregate query for a non-wma calculate
// clause: it filters by calc.Where and reduces to a single scalar via a
// native factstore Aggregate (count over ?e, or avg/sum over the `of attr`
// value var). The scalar is returned as a 1×1 rows table bound to calc.Name.
func (p *planner) buildCalculateQuery(calc ast.CalculateClause) factstore.Query {
	qb := p.newQueryBuilder()
	for _, c := range calc.Where {
		qb.addCondition(c)
	}
	var aggs []factstore.Aggregate
	switch calc.Method {
	case "count":
		aggs = []factstore.Aggregate{{Fn: "count", Over: factstore.Var("e"), As: calc.Name}}
	default: // "average" | "sum"
		fn := "avg"
		if calc.Method == "sum" {
			fn = "sum"
		}
		if path, ok := exprToFieldPath(calc.Value); ok {
			v := qb.varFor(attrVarName(calc.Value))
			qb.addPattern(path, factstore.Var(v))
			aggs = []factstore.Aggregate{{Fn: fn, Over: factstore.Var(v), As: calc.Name}}
		}
	}
	q := qb.build()
	q.Aggregates = aggs
	return q
}

// appendCalcSteps emits one step per calculate clause: an aggregate FactQuery
// (avg/sum/count — computed natively by the backend) or an MLComputation for
// wma (recency-weighted; its series source is deferred, shared with forecast).
// Each binds its scalar to vars[calc.Name] for `having` / label consumption.
func (p *planner) appendCalcSteps(plan *QueryPlan, calcs []ast.CalculateClause, last string) {
	for _, calc := range calcs {
		if calc.Method == "wma" {
			// wma isn't a SQL aggregate: fetch the ordered value series and
			// mark the step so the runtime reduces it via LinearWMA.
			plan.Steps = append(plan.Steps, &FactQuery{
				Query:    p.buildCalcSeriesQuery(calc),
				BindVars: map[string]any{},
				Reduce:   "wma",
				Into:     calc.Name,
			})
			continue
		}
		plan.Steps = append(plan.Steps, &FactQuery{
			Query:    p.buildCalculateQuery(calc),
			BindVars: map[string]any{},
			Into:     calc.Name,
		})
	}
}

// buildCalcSeriesQuery builds the value-series query for a wma calculate: it
// filters by calc.Where and projects [?e, ?value] so the runtime can pull the
// value column, ordered by entity id (the store returns id-sorted rows —
// oldest→newest by convention). No aggregate; the reduction is LinearWMA.
func (p *planner) buildCalcSeriesQuery(calc ast.CalculateClause) factstore.Query {
	qb := p.newQueryBuilder()
	for _, c := range calc.Where {
		qb.addCondition(c)
	}
	if path, ok := exprToFieldPath(calc.Value); ok {
		v := qb.varFor(attrVarName(calc.Value))
		qb.addPattern(path, factstore.Var(v))
		q := qb.build()
		q.Find = []string{qb.entityVar, v} // [?e, ?value]
		return q
	}
	return qb.build()
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func appendUniq(s []string, vs ...string) []string {
	seen := map[string]bool{}
	for _, x := range s {
		seen[x] = true
	}
	for _, v := range vs {
		if !seen[v] {
			s = append(s, v)
			seen[v] = true
		}
	}
	return s
}

func copyMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
