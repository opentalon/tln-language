package executor

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/constraints"
	"github.com/opentalon/tln-language/internal/factstore"
	tlnlog "github.com/opentalon/tln-language/internal/log"
	"github.com/opentalon/tln-language/internal/mlruntime"
	"github.com/opentalon/tln-language/internal/planner"
	"github.com/opentalon/tln-language/internal/template"
)

// TriggerRowVar is the reserved variable key under which a reactively-fired
// workflow receives the triggering record's attributes (namespace-stripped),
// so its step templates can interpolate {item.name}, bare {category},
// {attr.custom_attributes.x}, etc. against the record that fired the rule —
// not just the single triggering fact. Seeded by pkg/tln session; absent for
// plain (non-reactive) workflow runs, which then keep literal strings verbatim.
const TriggerRowVar = "__trigger_row"

// newDeterministicRNG returns a *rand.Rand seeded with the given
// value. Wrapped behind a constructor so a future migration to
// math/rand/v2 doesn't ripple across the package.
func newDeterministicRNG(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

// FactStore is the database abstraction layer the executor talks to. The
// canonical type lives in internal/factstore; this is the executor's
// alias so consumers don't need to import the factstore package
// transitively.
//
// Core ships only *factstore.MemoryStore (in-memory). Every other backend is a
// store plugin implementing this interface (tln-datalevin, tln-db), selected via
// config/store.tln or passed to a host's WithFactStore. See docs/factstore.md.
type FactStore = factstore.FactStore

// BlockResult is the outcome of executing one block's query plan.
type BlockResult struct {
	BlockName string
	Flagged   [][]any        // entities matched by the first FactQuery
	Vars      map[string]any // all intermediate result variables
	Steps     []StepResult   // per-step results for tracing

	// Actions holds the `do` clauses this block fired, resolved against
	// the rows that matched. Always non-nil: a block with no `do` clauses
	// (or one that matched nothing) reports an empty list, so a host never
	// has to tell nil from empty. tln executes none of them.
	Actions []FiredAction
}

// StepResult records one step's execution.
type StepResult struct {
	Type   string // "FactQuery", "GoComputation", "Filter"
	Name   string // function name or query variable
	Output any
}

// VectorSearcher is the slice of a vector-aware backend the executor
// needs to satisfy `find similar ... using vector scope "X"` steps.
// Wire backends like the talondb adapter implement this; in-memory
// stores can leave it unset — the executor reports a clear error
// instead of crashing.
type VectorSearcher interface {
	VectorSearch(ctx context.Context, scope string, query []float32, k int) ([]VectorHit, error)
}

// VectorHit mirrors the adapter-side hit shape. Kept here so the
// executor's caller doesn't need to import the talondb package just to
// see the type.
type VectorHit struct {
	ID       string
	Distance float32
}

// Executor runs compiled QueryPlans against a FactStore backend.
type Executor struct {
	Client      FactStore
	Registry    *mlruntime.Registry
	Tools       ToolResolver
	ConfirmHook ConfirmationHook

	// ApprovalHook gates `remediate approve` calls; Queue receives
	// `remediate queue` calls. Both are optional and host-supplied;
	// when nil, those modes skip the call (no approver / no queue).
	ApprovalHook ApprovalHook
	Queue        Queue

	// GraphProvider optionally supplies a *factstore.GraphSnapshot for
	// `find related` (PPR) steps. When nil, the executor falls back to
	// building a snapshot from in-scope row variables — useful for tests
	// where the dataset is seeded directly.
	GraphProvider GraphSnapshotProvider

	// VectorBackend is the talon-db (or compatible) interface used by
	// `find similar ... using vector` plan steps. When nil and a plan
	// step needs it, the executor returns ErrNoVectorBackend instead
	// of falling through to a misleading empty result.
	VectorBackend VectorSearcher

	// RandSeed optionally fixes the seed for probabilistic features
	// (recommend `suggest "X" with probability N`). When zero, the
	// executor falls back to a per-block deterministic seed derived
	// from the block name — same Run reproduces, different Runs
	// explore. Tests set this explicitly to assert exact outcomes.
	RandSeed int64

	// Now overrides the clock used to anchor time-travel reads
	// (`was <cond> N <unit> ago` resolves to now−Delta). When nil the
	// executor uses time.Now().UTC(). Tests set it for determinism.
	Now func() time.Time
}

// now returns the executor's clock, defaulting to UTC wall-clock.
func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

// probabilisticGate samples each row in `input` with probability
// `prob`, keeping only those that pass. Used by the recommend
// suggest-with-probability path so we can express ε-greedy /
// canary-rollout shaped policies directly in the language.
//
// Determinism: when Executor.RandSeed is set, the same seed plus
// block name produce identical outcomes across runs — important
// for tests and audit. When RandSeed is 0, the seed is derived
// from the block name alone, so re-running the same program is
// deterministic but two different blocks with the same probability
// don't share fate.
func (e *Executor) probabilisticGate(input any, prob float64, blockName string) any {
	rows, ok := input.([][]any)
	if !ok {
		return input
	}
	seed := e.RandSeed
	if seed == 0 {
		// FNV-1a hash of the block name — small but stable. We
		// don't need cryptographic strength; we need
		// reproducibility across Runs.
		var h uint64 = 14695981039346656037
		for i := 0; i < len(blockName); i++ {
			h ^= uint64(blockName[i])
			h *= 1099511628211
		}
		seed = int64(h)
	}
	rng := newDeterministicRNG(seed)
	kept := make([][]any, 0, len(rows))
	for _, r := range rows {
		if rng.Float64() < prob {
			kept = append(kept, r)
		}
	}
	return kept
}

// GraphSnapshotProvider builds or retrieves a factstore.GraphSnapshot for
// the executor. Implementations may cache, query a backend, etc.
type GraphSnapshotProvider interface {
	Snapshot(ctx context.Context, key string, opts map[string]any) (*factstore.GraphSnapshot, error)
}

// NewExecutor creates an executor backed by the given FactStore and
// a registry pre-populated with the default ML primitives.
func NewExecutor(client FactStore) *Executor {
	return &Executor{Client: client, Registry: mlruntime.NewRegistry()}
}

// RunAll executes all plans and returns results keyed by block name.
func (e *Executor) RunAll(ctx context.Context, plans map[string]*planner.QueryPlan) (map[string]*BlockResult, error) {
	results := make(map[string]*BlockResult, len(plans))
	for name, plan := range plans {
		result, err := e.Run(ctx, plan)
		if err != nil {
			return results, fmt.Errorf("block %q: %w", name, err)
		}
		results[name] = result
	}
	// Defeasible resolution runs after every block, before any host sees an
	// action: a strict rule in one block can defeat a rule in another, and
	// the loser's actions must not appear.
	for _, w := range resolveDefeatedActions(plans, results) {
		tlnlog.Default().WarnContext(ctx, w, "source", "defeasible")
	}
	return results, nil
}

// Run executes a single block's query plan. It emits a `block_eval`
// observability event on completion (success or failure) with the block
// name, kind, matched row count, and wall-clock duration.
func (e *Executor) Run(ctx context.Context, plan *planner.QueryPlan) (*BlockResult, error) {
	return e.RunWithPresets(ctx, plan, nil)
}

// RunWithPresets executes a single block's query plan like [Executor.Run],
// but seeds the block's variable scope with presets before the first step
// runs. This lets a caller inject synthetic step results — e.g. a reactive
// trigger exposing step("trigger").result.{entity,attr,value,prev,kind}.
//
// Keys are copied verbatim into the scope; to make a value resolvable via
// step("name").result.*, use the key "name_result" (the same convention
// the executor uses internally for real step results).
func (e *Executor) RunWithPresets(ctx context.Context, plan *planner.QueryPlan, presets map[string]any) (*BlockResult, error) {
	start := time.Now()
	result := &BlockResult{
		BlockName: plan.BlockName,
		Vars:      map[string]any{},
		Actions:   []FiredAction{},
	}
	for k, v := range presets {
		result.Vars[k] = v
	}

	for _, step := range plan.Steps {
		sr, err := e.execStep(ctx, step, result.Vars)
		if err != nil {
			tlnlog.BlockEval(ctx, plan.BlockName, "", 0, time.Since(start))
			return result, err
		}
		result.Steps = append(result.Steps, sr)
	}

	result.Flagged = flaggedRows(plan, result.Vars)
	if fired, ok := result.Vars[planner.ActionsVar].([]FiredAction); ok && fired != nil {
		result.Actions = fired
	}
	tlnlog.BlockEval(ctx, plan.BlockName, "", len(result.Flagged), time.Since(start))
	return result, nil
}

// flaggedRows derives the block's flagged set. Start with the first
// FactQuery's rows, then narrow to entities marked Value=true by any
// MLComputation step downstream.
func flaggedRows(plan *planner.QueryPlan, vars map[string]any) [][]any {
	var rows [][]any
	// Walk the plan in order so any row-narrowing step (Filter,
	// EventSequenceStep) downstream of the FactQuery is applied
	// before ML steps prune by entity ID. The last [][]any-shaped
	// `Into` becomes the canonical "flagged rows" the block
	// observed.
	for _, step := range plan.Steps {
		switch s := step.(type) {
		case *planner.FactQuery:
			// As-of FactQueries feed the intersect step, and aggregate /
			// reduced (calculate) FactQueries bind a scalar — none is the
			// candidate stream, so skip them to keep Flagged accurate.
			if s.AsOfDelta == nil && len(s.Query.Aggregates) == 0 && s.Reduce == "" && !s.Auxiliary {
				if arr, ok := vars[s.Into].([][]any); ok {
					rows = arr
				}
			}
		case *planner.GoComputation:
			if s.Function == planner.FuncAsOfIntersect {
				if arr, ok := vars[s.Into].([][]any); ok {
					rows = arr
				}
			}
		case *planner.Filter:
			if arr, ok := vars[s.Into].([][]any); ok {
				rows = arr
			}
		case *planner.EventSequenceStep:
			if arr, ok := vars[s.Into].([][]any); ok {
				rows = arr
			}
		case *planner.RecordSequenceStep:
			if arr, ok := vars[s.Into].([][]any); ok {
				rows = arr
			}
		case *planner.VectorSimilarStep:
			if arr, ok := vars[s.Into].([][]any); ok {
				rows = arr
			}
		}
	}
	if rows == nil {
		return nil
	}

	for _, step := range plan.Steps {
		ml, ok := step.(*planner.MLComputation)
		if !ok {
			continue
		}
		flagged, ok := extractFlaggedIDs(vars[ml.Into])
		if !ok {
			continue
		}
		rows = filterRowsByID(rows, flagged)
	}
	return rows
}

// extractFlaggedIDs reads the entity IDs from an ML step's output. A row
// is "flagged" when its Value is the bool true (anomaly/threshold style)
// or any non-zero float64 (ranked output like PPR top-K).
func extractFlaggedIDs(out any) (map[int]bool, bool) {
	m, ok := out.(map[string]any)
	if !ok {
		return nil, false
	}
	rs, ok := m["results"].([]mlruntime.Result)
	if !ok {
		return nil, false
	}
	ids := map[int]bool{}
	filtering := false
	for _, r := range rs {
		switch v := r.Value.(type) {
		case bool:
			filtering = true
			if v {
				ids[r.EntityID] = true
			}
		case float64:
			filtering = true
			if v != 0 {
				ids[r.EntityID] = true
			}
		default:
			// A string (classify class) or other informational value: the
			// primitive labels rather than filters, so it doesn't narrow the
			// candidate stream.
		}
	}
	if !filtering {
		return nil, false
	}
	return ids, true
}

// filterRowsByID keeps only rows whose first column (entity ID) is in keep.
func filterRowsByID(rows [][]any, keep map[int]bool) [][]any {
	out := make([][]any, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		id, ok := toInt(row[0])
		if !ok {
			continue
		}
		if keep[id] {
			out = append(out, row)
		}
	}
	return out
}

func (e *Executor) execStep(ctx context.Context, step planner.PlanStep, vars map[string]any) (StepResult, error) {
	switch s := step.(type) {
	case *planner.FactQuery:
		return e.execQuery(ctx, s, vars)
	case *planner.GoComputation:
		return e.execComputation(ctx, s, vars)
	case *planner.MLComputation:
		return e.execMLComputation(s, vars)
	case *planner.Filter:
		return e.execFilter(s, vars)
	case *planner.GraphSnapshot:
		return e.execGraphSnapshot(ctx, s, vars)
	case *planner.StateMachineStep:
		return e.execStateMachine(ctx, s, vars)
	case *planner.EventSequenceStep:
		return e.execEventSequence(ctx, s, vars)
	case *planner.RecordSequenceStep:
		return e.execRecordSequence(ctx, s, vars)
	case *planner.VectorSimilarStep:
		return e.execVectorSimilar(ctx, s, vars)
	default:
		return StepResult{}, fmt.Errorf("unknown step type: %T", step)
	}
}

// execGraphSnapshot loads or builds a graph snapshot and binds it under
// the step's Into variable. Resolution order:
//  1. If the executor has a GraphProvider, ask it.
//  2. Else, if vars already contains a *factstore.GraphSnapshot under
//     "graph" (typical in tests that seed directly), reuse it.
//  3. Else, return an empty snapshot so downstream PPR returns ErrNoGraph
//     with a clear diagnostic instead of crashing.
func (e *Executor) execGraphSnapshot(ctx context.Context, g *planner.GraphSnapshot, vars map[string]any) (StepResult, error) {
	var snap *factstore.GraphSnapshot
	if e.GraphProvider != nil {
		s, err := e.GraphProvider.Snapshot(ctx, g.CacheKey, g.Options)
		if err != nil {
			return StepResult{}, fmt.Errorf("graph snapshot %q: %w", g.CacheKey, err)
		}
		snap = s
	} else if existing, ok := vars[g.Into].(*factstore.GraphSnapshot); ok {
		snap = existing
	}
	vars[g.Into] = snap
	return StepResult{Type: "GraphSnapshot", Name: g.Into, Output: snap}, nil
}

func (e *Executor) execQuery(ctx context.Context, dq *planner.FactQuery, vars map[string]any) (StepResult, error) {
	// Skip empty queries (e.g. calculate clauses with no conditions).
	if len(dq.Query.Where) == 0 {
		vars[dq.Into] = [][]any{}
		return StepResult{Type: "FactQuery", Name: dq.Into, Output: [][]any{}}, nil
	}
	var (
		rows [][]any
		err  error
	)
	if dq.AsOfDelta != nil {
		// Time-travel read: evaluate against the store's past state. The
		// backend must implement TimeTraveler; otherwise the block can't run.
		tt, ok := e.Client.(factstore.TimeTraveler)
		if !ok {
			return StepResult{}, fmt.Errorf("query into %q: %w", dq.Into, factstore.ErrNoTimeTravel)
		}
		asOf := e.now().Add(-constraints.DurationDelta(*dq.AsOfDelta))
		rows, err = tt.QueryAsOf(ctx, dq.Query, asOf)
	} else {
		rows, err = e.Client.Query(ctx, dq.Query)
	}
	if err != nil {
		return StepResult{}, fmt.Errorf("query into %q: %w", dq.Into, err)
	}
	vars[dq.Into] = rows
	return StepResult{
		Type:   "FactQuery",
		Name:   dq.Into,
		Output: rows,
	}, nil
}

// intersectRowsByEntity keeps rows of base whose entity id (column 0)
// appears in other. Backs FuncAsOfIntersect: narrow present-day candidates
// to those that also matched a time-travel query.
func intersectRowsByEntity(base, other [][]any) [][]any {
	keep := make(map[string]bool, len(other))
	for _, r := range other {
		if len(r) > 0 {
			keep[rowEntityKey(r[0])] = true
		}
	}
	out := make([][]any, 0, len(base))
	for _, r := range base {
		if len(r) > 0 && keep[rowEntityKey(r[0])] {
			out = append(out, r)
		}
	}
	return out
}

// rowEntityKey normalizes an entity-id cell to a stable string key across
// the numeric types different backends emit (float64 from MemoryStore /
// structpb, ints elsewhere).
func rowEntityKey(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(n), 'f', -1, 64)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case int32:
		return strconv.Itoa(int(n))
	}
	return fmt.Sprint(v)
}

func (e *Executor) execComputation(ctx context.Context, gc *planner.GoComputation, vars map[string]any) (StepResult, error) {
	input := vars[gc.Input]

	switch gc.Function {
	case planner.FuncRenderTemplate:
		out := map[string]any{
			"input":    input,
			"template": gc.Params["template"],
		}
		// Probabilistic gating + optional Bayesian update from
		// feedback. When `feedback_window_days` is set, we treat
		// the declared `probability` as a Beta prior and shift
		// it toward observed accept rate within the window. Then
		// sample at the posterior rate. Fired suggestions are
		// stamped with a trace ID so the host can later attribute
		// user actions back to which suggestion prompted them.
		if prob, ok := gc.Params["probability"].(float64); ok && prob > 0 && prob < 1 {
			blockName, _ := gc.Params["block_name"].(string)
			window, _ := gc.Params["feedback_window_days"].(int)
			effective := prob
			if window > 0 {
				if updated, err := e.adjustWithFeedback(ctx, blockName, prob, window); err == nil {
					effective = updated
				}
				// On error we silently fall back to the prior —
				// missing feedback infra shouldn't break the
				// recommend path; it just means no learning yet.
			}
			out["probability"] = effective
			out["prior_probability"] = prob
			gated := e.probabilisticGate(input, effective, blockName)
			out["input"] = gated
			if window > 0 {
				// Mint trace IDs for kept rows so the host can
				// attribute feedback later. Best-effort: errors
				// don't fail the recommend.
				if rows, ok := gated.([][]any); ok && len(rows) > 0 {
					ids, _ := e.mintTraces(ctx, blockName, rows)
					out["trace_ids"] = ids
				}
			}
		}
		vars[gc.Into] = out
	case "resolve_block_matches":
		vars[gc.Into] = input
	case "mcp_call":
		result, err := e.execMCPCall(ctx, gc, vars)
		if err != nil {
			return StepResult{}, err
		}
		vars[gc.Into] = result
	case planner.FuncFireActions:
		vars[gc.Into] = e.execFireActions(ctx, gc, vars)
	case planner.FuncRemediateMCP:
		result, err := e.execRemediate(ctx, gc, vars)
		if err != nil {
			return StepResult{}, err
		}
		vars[gc.Into] = result
	case planner.FuncEnrichMCP:
		result, err := e.execEnrich(ctx, gc, vars)
		if err != nil {
			return StepResult{}, err
		}
		vars[gc.Into] = result
	case planner.FuncOptimizePareto:
		result, err := e.execOptimizePareto(ctx, gc, vars)
		if err != nil {
			return StepResult{}, err
		}
		vars[gc.Into] = result
	case planner.FuncOptimizeGA:
		result, err := e.execOptimizeGA(ctx, gc, vars)
		if err != nil {
			return StepResult{}, err
		}
		vars[gc.Into] = result
	case planner.FuncOptimizeACO:
		result, err := e.execOptimizeACO(ctx, gc, vars)
		if err != nil {
			return StepResult{}, err
		}
		vars[gc.Into] = result
	case planner.FuncOptimizeILP:
		result, err := e.execOptimizeILP(ctx, gc, vars)
		if err != nil {
			return StepResult{}, err
		}
		vars[gc.Into] = result
	case planner.FuncAsOfIntersect:
		base, _ := input.([][]any)
		withVar, _ := gc.Params["with"].(string)
		other, _ := vars[withVar].([][]any)
		vars[gc.Into] = intersectRowsByEntity(base, other)
	default:
		vars[gc.Into] = map[string]any{
			"function": gc.Function,
			"input":    input,
			"params":   gc.Params,
			"status":   "stub",
		}
	}

	return StepResult{
		Type:   "GoComputation",
		Name:   gc.Function,
		Output: vars[gc.Into],
	}, nil
}

func (e *Executor) execMCPCall(ctx context.Context, gc *planner.GoComputation, vars map[string]any) (any, error) {
	mcpCall, ok := gc.Params["mcp"].(*ast.MCPCall)
	if !ok {
		return map[string]any{"status": "stub", "step": gc.Params["step"]}, nil
	}

	if e.Tools == nil {
		return map[string]any{"status": "stub", "step": gc.Params["step"]}, nil
	}

	stepName, _ := gc.Params["step"].(string)

	// Optional `when` guard: skip this call when the condition doesn't hold.
	// Evaluated before the confirmation hook so we never prompt for a call the
	// guard will drop. A skipped step's result is a {status:"skipped"} marker,
	// so a downstream step depending on it reads nil for its fields.
	if cond, ok := gc.Params["when"].(ast.Condition); ok && cond != nil {
		pass, err := e.stepGuardPasses(cond, vars)
		if err != nil {
			return nil, fmt.Errorf("step %q: when guard: %w", stepName, err)
		}
		if !pass {
			tlnlog.MCPCall(ctx, mcpCall.Server, mcpCall.Tool, "skipped", 0, nil)
			return map[string]any{"status": "skipped", "reason": "when_guard"}, nil
		}
	}

	// Confirmation hook
	if e.ConfirmHook != nil {
		proceed, err := e.ConfirmHook(ctx, stepName, mcpCall.Server, mcpCall.Tool)
		if err != nil {
			return nil, fmt.Errorf("step %q: confirmation: %w", stepName, err)
		}
		if !proceed {
			tlnlog.MCPCall(ctx, mcpCall.Server, mcpCall.Tool, "skipped", 0, nil)
			return map[string]any{"status": "skipped", "reason": "confirmation_denied"}, nil
		}
	}

	// Resolve MCP args
	args := resolveMCPArgs(mcpCall.Args, vars)

	// Check for collect_all
	collectAll := false
	if ca, ok := args["collect_all"]; ok {
		if b, ok := ca.(bool); ok && b {
			collectAll = true
		}
		delete(args, "collect_all")
	}

	mcpStart := time.Now()
	if !collectAll {
		// Single call goes through the shared on_error policy path
		// (retry / log / skip / fail); dispatchMCP handles logging.
		res, skipped, err := e.dispatchMCP(ctx, mcpCall.Server, mcpCall.Tool, args, mcpCall.OnError, nil)
		if err != nil {
			return nil, err
		}
		if skipped {
			return map[string]any{"status": "skipped", "reason": "on_error"}, nil
		}
		return res, nil
	}

	// Auto-paginate: call repeatedly until has_more is false
	res, err := e.collectAll(ctx, mcpCall.Server, mcpCall.Tool, args)
	status := "ok"
	if err != nil {
		status = "error"
	}
	tlnlog.MCPCall(ctx, mcpCall.Server, mcpCall.Tool, status, time.Since(mcpStart), err)
	return res, err
}

func (e *Executor) collectAll(ctx context.Context, server, tool string, args map[string]any) (any, error) {
	var allItems []any
	page := 1
	for {
		args["page"] = page
		result, err := e.Tools.Call(ctx, server, tool, args)
		if err != nil {
			return nil, err
		}
		m, ok := result.(map[string]any)
		if !ok {
			return result, nil
		}
		if items, ok := m["items"].([]any); ok {
			allItems = append(allItems, items...)
		}
		hasMore, _ := m["has_more"].(bool)
		if !hasMore {
			break
		}
		page++
	}
	return map[string]any{"items": allItems}, nil
}

// resolveMCPArgs evaluates each MCP argument expression against step results.
func resolveMCPArgs(exprArgs map[string]ast.Expr, vars map[string]any) map[string]any {
	args := map[string]any{}
	for k, expr := range exprArgs {
		args[k] = resolveExprValue(expr, vars)
	}
	return args
}

func resolveExprValue(expr ast.Expr, vars map[string]any) any {
	switch e := expr.(type) {
	case *ast.LiteralExpr:
		// A string literal carrying `{…}` refs interpolates against the
		// triggering record's row when one is present (reactive fire). With no
		// row (plain workflow run) it passes through verbatim, as before.
		if s, ok := e.Value.(string); ok && strings.IndexByte(s, '{') >= 0 {
			if row, ok := vars[TriggerRowVar].(map[string]any); ok && len(row) > 0 {
				return template.Render(ast.ParseTemplate(s), template.RenderContext{Row: template.Row(row)})
			}
		}
		return e.Value
	case *ast.StepResultExpr:
		return resolveStepField(vars, e.StepName, e.Field)
	case *ast.MapExpr:
		src := resolveExprValue(e.Source, vars)
		return resolveMap(src, e.Field)
	case *ast.FindExpr:
		// First element of the array whose fields satisfy the predicate, then the
		// trailing field navigated on it — e.g.
		// step("cats").ticket_categories.find(name == "Defect").id.
		arr, ok := resolveExprValue(e.Source, vars).([]any)
		if !ok {
			return nil
		}
		for _, el := range arr {
			m, ok := el.(map[string]any)
			if !ok {
				continue
			}
			if match, err := constraints.EvalCondition(e.Cond, m); err == nil && match {
				return navPath(m, e.Field)
			}
		}
		return nil
	case *ast.ListExpr:
		// A list literal MCP arg — e.g. `assignee_ids [2383]` or
		// `item_ids [step("find").result.0.id]`. Each element resolves the same
		// way any arg value does, so ids, step results, and interpolated strings
		// all compose inside a list. Complements resolveMap's array (`.map(id)`).
		out := make([]any, 0, len(e.Elements))
		for _, el := range e.Elements {
			out = append(out, resolveExprValue(el, vars))
		}
		return out
	case *ast.ContextExpr:
		return resolveStepField(vars, "context", e.Field)
	case *ast.IdentExpr:
		return e.Name
	default:
		// Any other expression — function calls (concat, days_until, …), the
		// today / now() clock, arithmetic (today + N days) — evaluates through
		// the shared expression evaluator, the same one `when` guards use, so the
		// date and string toolkits work in tool-call args, not only in guards.
		// The scope mirrors stepGuardPasses: the triggering row (if any) plus the
		// step-result scope under StepScopeKey.
		rec := map[string]any{}
		if row, ok := vars[TriggerRowVar].(map[string]any); ok {
			for k, v := range row {
				rec[k] = v
			}
		}
		rec[constraints.StepScopeKey] = vars
		if v, err := constraints.EvalExpr(expr, rec, time.Now().UTC()); err == nil {
			return v
		}
		return nil
	}
}

// stepGuardPasses evaluates a workflow step's optional `when` guard. The guard
// sees the triggering record's fields (when the workflow was fired reactively,
// exposed under TriggerRowVar) plus any prior step's result via
// step("name").field — the full variable scope is injected under
// constraints.StepScopeKey so the evaluator resolves those operands. With no
// trigger row and no matching step result, an operand resolves to nil.
func (e *Executor) stepGuardPasses(cond ast.Condition, vars map[string]any) (bool, error) {
	rec := map[string]any{}
	if row, ok := vars[TriggerRowVar].(map[string]any); ok {
		for k, v := range row {
			rec[k] = v
		}
	}
	rec[constraints.StepScopeKey] = vars
	return constraints.EvalCondition(cond, rec)
}

// resolveStepField navigates step("name").result.field by traversing the
// stored step result using dot-separated field paths.
func resolveStepField(vars map[string]any, stepName, field string) any {
	return navPath(vars[stepName+"_result"], field)
}

// navPath walks a dot path over nested maps/lists — a numeric segment
// indexes into a list (step("find").result.0.id), off-the-end yields nil.
// Shared by step-result and find navigation.
func navPath(cur any, path string) any {
	if cur == nil {
		return nil
	}
	if path == "" {
		return cur
	}
	for _, part := range strings.Split(path, ".") {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[part]
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(c) {
				return nil
			}
			cur = c[idx]
		default:
			return nil
		}
	}
	return cur
}

// resolveMap extracts a field from each element of an array.
// e.g. items.map(id) → [item1["id"], item2["id"], ...]
func resolveMap(src any, field string) any {
	arr, ok := src.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(arr))
	for _, elem := range arr {
		m, ok := elem.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m[field])
	}
	return out
}

// execMLComputation dispatches an MLComputation step to the registry.
// Primitives without a registered implementation fall back to a structured
// stub so downstream steps (render_template, filters) can still run.
func (e *Executor) execMLComputation(ml *planner.MLComputation, vars map[string]any) (StepResult, error) {
	input := vars[ml.Input]

	if e.Registry != nil && e.Registry.Has(ml.Function) {
		prim, _ := e.Registry.Get(ml.Function)
		rows, _ := input.([][]any)
		params := resolveMLParams(ml.Params, vars, rows)
		results, err := prim.Compute(context.Background(), mlruntime.Input{
			Rows:   rows,
			Schema: schemaFromParams(ml.Params),
			Params: params,
		})
		if err != nil {
			return StepResult{}, fmt.Errorf("ml %s: %w", ml.Function, err)
		}
		vars[ml.Into] = map[string]any{
			"function": ml.Function,
			"results":  results,
		}
		return StepResult{
			Type:   "MLComputation",
			Name:   ml.Function,
			Output: vars[ml.Into],
		}, nil
	}

	vars[ml.Into] = map[string]any{
		"function":     ml.Function,
		"input":        input,
		"params":       ml.Params,
		"status":       "stub",
		"explanations": []any{},
	}
	return StepResult{
		Type:   "MLComputation",
		Name:   ml.Function,
		Output: vars[ml.Into],
	}, nil
}

func (e *Executor) execFilter(f *planner.Filter, vars map[string]any) (StepResult, error) {
	// Filters contain Go predicate expressions that need a proper evaluator.
	// For now, pass through the input unfiltered.
	vars[f.Into] = vars[f.Input]
	return StepResult{
		Type:   "Filter",
		Name:   f.Condition,
		Output: vars[f.Into],
	}, nil
}

// Seed reads given blocks from a parsed .tln.test program and asserts
// every fact through the FactStore's Assert method. Schema inference (for
// backends that need it, e.g. Datalevin) is the backend's responsibility.
func (e *Executor) Seed(ctx context.Context, prog *ast.Program) (int, error) {
	// Flatten given { record N k v; attr N "k" v } into a Fact list.
	// Dedup by entity ID so the return value reports distinct entities.
	entityIDs := map[int]bool{}
	var facts []factstore.Fact
	for _, b := range prog.Blocks {
		tb, ok := b.(*ast.TestBlock)
		if !ok {
			continue
		}
		for _, d := range tb.Given {
			entityIDs[d.ID] = true
			id := strconv.Itoa(d.ID)
			for k, v := range d.Fields {
				facts = append(facts, factstore.Fact{
					RecordID:  id,
					Attribute: fieldNamespace(d.Kind, k),
					Value:     v,
				})
			}
		}
	}
	if len(facts) == 0 {
		return 0, nil
	}
	if err := e.Client.Assert(ctx, facts); err != nil {
		return 0, fmt.Errorf("seed: %w", err)
	}
	return len(entityIDs), nil
}

// ResolveNames looks up :attr/name for all entities via a structured query.
func (e *Executor) ResolveNames(ctx context.Context, _ []int) (map[int]string, error) {
	q := factstore.Query{
		Find: []string{"?e", "?name"},
		Where: []factstore.Clause{
			&factstore.Pattern{
				Entity:    factstore.Var("e"),
				Attribute: ":attr/name",
				Value:     factstore.Var("name"),
			},
		},
	}
	rows, err := e.Client.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	names := map[int]string{}
	for _, row := range rows {
		if len(row) >= 2 {
			if eid, ok := toInt(row[0]); ok {
				if name, ok := row[1].(string); ok {
					names[eid] = name
				}
			}
		}
	}
	return names, nil
}

// resolveMLParams produces the params map handed to the primitive. It
// resolves PPR-specific helper fields (graph_var, seed_expr, seeds_expr)
// against `vars` and `rows`, and copies through all other fields unchanged.
func resolveMLParams(params map[string]any, vars map[string]any, rows [][]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		switch k {
		case "graph_var":
			if name, ok := v.(string); ok {
				if g, ok := vars[name].(*factstore.GraphSnapshot); ok {
					out["graph"] = g
				}
			}
		case "seed_expr":
			if e, ok := v.(ast.Expr); ok && e != nil {
				if seeds := resolveSeeds(e, rows, vars); len(seeds) > 0 {
					out["seeds"] = appendSeeds(out["seeds"], seeds)
				}
			}
		case "seeds_expr":
			if exprs, ok := v.([]ast.Expr); ok {
				for _, e := range exprs {
					if seeds := resolveSeeds(e, rows, vars); len(seeds) > 0 {
						out["seeds"] = appendSeeds(out["seeds"], seeds)
					}
				}
			}
		default:
			if v != nil {
				out[k] = v
			}
		}
	}
	return out
}

// resolveSeeds turns a seed AST expression into one or more entity IDs.
// Literals and idents resolve directly; attr "id" expressions seed from
// the first column of every candidate row (matching tln's row layout).
func resolveSeeds(e ast.Expr, rows [][]any, vars map[string]any) []string {
	switch v := e.(type) {
	case *ast.LiteralExpr:
		return []string{seedString(v.Value)}
	case *ast.IdentExpr:
		return []string{v.Name}
	case *ast.AttrExpr:
		// `to attr "id"` — broadcast across every row's entity ID column.
		seeds := make([]string, 0, len(rows))
		for _, row := range rows {
			if len(row) > 0 {
				seeds = append(seeds, seedString(row[0]))
			}
		}
		return seeds
	case *ast.ListExpr:
		out := []string{}
		for _, elem := range v.Elements {
			out = append(out, resolveSeeds(elem, rows, vars)...)
		}
		return out
	}
	return nil
}

func appendSeeds(existing any, more []string) []string {
	current, _ := existing.([]string)
	return append(current, more...)
}

func seedString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprintf("%v", v)
}

// schemaFromParams extracts column indices the planner stuffed into Params
// (value_index, entity_id_index) so primitives can read the right cells.
func schemaFromParams(params map[string]any) map[string]int {
	if params == nil {
		return nil
	}
	schema := map[string]int{}
	if idx, ok := intParam(params, "value_index"); ok && idx >= 0 {
		schema["value"] = idx
	}
	if idx, ok := intParam(params, "value_index_x"); ok && idx >= 0 {
		schema["value_x"] = idx
	}
	if idx, ok := intParam(params, "value_index_y"); ok && idx >= 0 {
		schema["value_y"] = idx
	}
	if idx, ok := intParam(params, "entity_id_index"); ok && idx >= 0 {
		schema["entity_id"] = idx
	}
	if len(schema) == 0 {
		return nil
	}
	return schema
}

func intParam(params map[string]any, key string) (int, bool) {
	switch v := params[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func fieldNamespace(kind, key string) string {
	if kind == "attr" {
		return ":attr/" + key
	}
	switch key {
	case "type":
		return ":record/type"
	case "status":
		return ":record/status"
	case "category":
		return ":record/category"
	default:
		return ":record/" + key
	}
}
