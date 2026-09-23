package validator

import (
	"fmt"
	"strings"
	"time"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/diagnostic"
)

type validator struct {
	file    string
	prog    *ast.Program
	diags   diagnostic.List
	defines map[string]*ast.DefineBlock
	blocks  map[string]ast.Block // all named blocks
	seen    map[string]ast.Pos   // first occurrence, for duplicate detection
}

func Validate(file string, prog *ast.Program) diagnostic.List {
	v := &validator{
		file:    file,
		prog:    prog,
		defines: map[string]*ast.DefineBlock{},
		blocks:  map[string]ast.Block{},
		seen:    map[string]ast.Pos{},
	}
	v.collect()
	v.checkDuplicates()
	v.checkCompleteness()
	v.checkReferences()
	v.checkOverrides()
	v.checkTemplates()
	v.checkScore()
	v.checkTypes()
	v.checkCycles()
	v.checkWorkflows()
	v.checkAsOf()
	v.checkCorrelation()
	v.checkCalculate()
	v.checkThresholds()
	v.checkDerives()
	return v.diags
}

// checkDerives validates derived-predicate blocks: every `pred(X)` reference
// must resolve to a declared `derive`, and the derive dependency graph must be
// acyclic. v1 is arity-1 and non-recursive; a cycle (which for arity-1 has no
// base case) is rejected — this also subsumes negation-through-recursion.
func (v *validator) checkDerives() {
	derives := map[string]*ast.DeriveBlock{}
	var order []string
	for _, b := range v.prog.Blocks {
		if d, ok := b.(*ast.DeriveBlock); ok {
			derives[d.Name] = d
			order = append(order, d.Name)
		}
	}

	// Every predicate-call reference must resolve to a declared derive.
	for _, b := range v.prog.Blocks {
		pos := blockPos(b)
		walkBlockConditions(b, func(c ast.Condition) {
			pc, ok := c.(*ast.PredicateCallCondition)
			if ok {
				if _, declared := derives[pc.Name]; !declared {
					v.errAt(pos, fmt.Sprintf("reference to undeclared derived predicate %q", pc.Name), suggest(pc.Name, order))
				}
			}
		})
	}

	// Build the *signed* derive→derive dependency graph and reject any cycle.
	// An edge name→pred is negative when the reference sits under an odd number
	// of `not`s (`not overdue(v)`); the parity distinguishes a plain recursive
	// cycle from negation-through-recursion, which needs well-founded semantics
	// to have a meaning at all (see #170) and is rejected here with a distinct
	// message.
	deps := map[string][]deriveEdge{}
	for _, name := range order {
		for _, c := range derives[name].Selector.Conditions {
			walkCondPolarity(c, false, func(cc ast.Condition, negated bool) {
				pc, ok := cc.(*ast.PredicateCallCondition)
				if !ok {
					return
				}
				if _, isDerive := derives[pc.Name]; !isDerive {
					return
				}
				// A predicate reachable both positively and negatively keeps the
				// negative edge — the cycle it may close is the unsafe one.
				if e, ok := seen2(deps[name], pc.Name); ok {
					if negated && !e.negative {
						markNegative(deps[name], pc.Name)
					}
					return
				}
				deps[name] = append(deps[name], deriveEdge{to: pc.Name, negative: negated})
			})
		}
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	// path/edgeNeg reconstruct the DFS-tree cycle so its polarity is known:
	// parent[m] is the node we entered m from, edgeNeg[m] the polarity of that
	// tree edge. A cycle is non-stratifiable iff any edge on it is negative.
	parent := map[string]string{}
	edgeNeg := map[string]bool{}
	var dfs func(n string) bool
	dfs = func(n string) bool {
		color[n] = gray
		for _, e := range deps[n] {
			m := e.to
			if color[m] == gray {
				if cycleHasNegative(n, m, e.negative, parent, edgeNeg) {
					v.errAt(derives[n].Pos,
						fmt.Sprintf("negation through recursion via %q is not stratifiable — a predicate cannot depend on its own negation (well-founded semantics required; see issue #170)", m), "")
				} else {
					v.errAt(derives[n].Pos,
						fmt.Sprintf("recursive derive cycle through %q — not supported in v1 (arity-1 recursion has no base case)", m), "")
				}
				return true
			}
			if color[m] == white {
				parent[m] = n
				edgeNeg[m] = e.negative
				if dfs(m) {
					return true
				}
			}
		}
		color[n] = black
		return false
	}
	for _, name := range order {
		if color[name] == white && dfs(name) {
			return
		}
	}
}

// deriveEdge is one edge of the signed derive dependency graph.
type deriveEdge struct {
	to       string
	negative bool
}

func seen2(edges []deriveEdge, to string) (deriveEdge, bool) {
	for _, e := range edges {
		if e.to == to {
			return e, true
		}
	}
	return deriveEdge{}, false
}

func markNegative(edges []deriveEdge, to string) {
	for i := range edges {
		if edges[i].to == to {
			edges[i].negative = true
		}
	}
}

// cycleHasNegative reports whether the cycle closed by the back-edge n→m
// (polarity closingNeg) contains any negative edge. It walks the DFS tree
// from n up to m via parent pointers, OR-ing each tree edge's polarity.
func cycleHasNegative(n, m string, closingNeg bool, parent map[string]string, edgeNeg map[string]bool) bool {
	if closingNeg {
		return true
	}
	for cur := n; cur != m; cur = parent[cur] {
		if edgeNeg[cur] {
			return true
		}
		if _, ok := parent[cur]; !ok {
			break
		}
	}
	return false
}

// checkThresholds validates cached threshold blocks and their references:
// valid_until must parse as a date; an expired threshold warns (the stale
// value is still used — the host's discovery job is expected to refresh it);
// and every `threshold "name"` reference must resolve to a declared block.
func (v *validator) checkThresholds() {
	declared := map[string]bool{}
	for _, b := range v.prog.Blocks {
		t, ok := b.(*ast.ThresholdBlock)
		if !ok {
			continue
		}
		declared[t.Name] = true
		if t.ValidUntil == "" {
			continue
		}
		exp, err := parseThresholdDate(t.ValidUntil)
		switch {
		case err != nil:
			v.errAt(t.Pos, fmt.Sprintf("threshold %q: valid_until %q is not a date (want YYYY-MM-DD or RFC 3339)", t.Name, t.ValidUntil), "")
		case exp.Before(time.Now()):
			v.diags.AddWarning(v.file, t.Pos.Line, t.Pos.Col,
				fmt.Sprintf("threshold %q expired on %s — its stale value is still used; the host discovery job should refresh it", t.Name, t.ValidUntil), "")
		}
	}

	for _, b := range v.prog.Blocks {
		pos := blockPos(b)
		walkBlockConditions(b, func(c ast.Condition) {
			walkCondThresholdRefs(c, func(name string) {
				if !declared[name] {
					v.errAt(pos, fmt.Sprintf("reference to undeclared threshold %q", name), suggest(name, names(declared)))
				}
			})
		})
	}
}

// parseThresholdDate accepts a bare date (YYYY-MM-DD) or a full RFC 3339
// timestamp — both forms appear in host-generated threshold blocks.
func parseThresholdDate(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func walkCondThresholdRefs(c ast.Condition, fn func(string)) {
	switch cc := c.(type) {
	case *ast.CompareCondition:
		walkExprThresholdRefs(cc.Left, fn)
		walkExprThresholdRefs(cc.Right, fn)
	case *ast.MembershipCondition:
		walkExprThresholdRefs(cc.Expr, fn)
		for _, m := range cc.Members {
			walkExprThresholdRefs(m, fn)
		}
	case *ast.StringMatchCondition:
		walkExprThresholdRefs(cc.Subject, fn)
	case *ast.TemporalCondition:
		walkExprThresholdRefs(cc.Subject, fn)
	case *ast.IsCondition:
		walkExprThresholdRefs(cc.Subject, fn)
	case *ast.AnomalyCondition:
		walkExprThresholdRefs(cc.Subject, fn)
	case *ast.CorrelationCondition:
		walkExprThresholdRefs(cc.Left, fn)
		walkExprThresholdRefs(cc.Right, fn)
	case *ast.ChangedToCondition:
		walkExprThresholdRefs(cc.Value, fn)
	}
}

func walkExprThresholdRefs(e ast.Expr, fn func(string)) {
	switch ex := e.(type) {
	case *ast.ThresholdRefExpr:
		fn(ex.Name)
	case *ast.BinaryExpr:
		walkExprThresholdRefs(ex.Left, fn)
		walkExprThresholdRefs(ex.Right, fn)
	case *ast.UnaryExpr:
		walkExprThresholdRefs(ex.Operand, fn)
	case *ast.ListExpr:
		for _, el := range ex.Elements {
			walkExprThresholdRefs(el, fn)
		}
	case *ast.CallExpr:
		for _, a := range ex.Args {
			walkExprThresholdRefs(a, fn)
		}
	}
}

// checkScore range-checks `confidence N` provenance annotations to [0, 1].
// The ML filter form (`confidence >= N`) is enforced separately by the
// ConfidenceClause grammar and runtime; here we only see the bare-NUMBER
// annotation form populated by parseScoreAnnotation.
func (v *validator) checkScore() {
	check := func(pos ast.Pos, name string, score *float64) {
		if score == nil {
			return
		}
		if *score < 0 || *score > 1 {
			v.errAt(pos,
				fmt.Sprintf("%s: confidence annotation %.3f is outside [0, 1]", name, *score),
				"confidence annotations are probabilities — provenance generators emit values in that range")
		}
	}
	for _, b := range v.prog.Blocks {
		switch bb := b.(type) {
		case *ast.DetectBlock:
			check(bb.Pos, "detect "+bb.Name, bb.Score)
		case *ast.RuleBlock:
			check(bb.Pos, "rule "+bb.Name, bb.Score)
		}
	}
}

// checkTemplates flags unknown function names inside `{...}` template
// interpolations. The renderer leaves unknowns as their literal source
// so users see what went wrong; this surface gives compile-time
// diagnostics for the same gap before the rule ever fires.
func (v *validator) checkTemplates() {
	for _, b := range v.prog.Blocks {
		pos := blockPos(b)
		for _, tmpl := range collectTemplates(b) {
			if tmpl == nil {
				continue
			}
			for _, n := range tmpl.Nodes {
				fn, ok := n.(*ast.FuncNode)
				if !ok {
					continue
				}
				if _, known := ast.KnownTemplateFunctions[fn.Fn]; !known {
					v.errAt(pos, fmt.Sprintf("template uses unknown function %q", fn.Fn),
						"supported: count, total, sum, avg, min, max, days_until, days_since")
				}
			}
		}
	}
}

// collectTemplates returns every Template embedded in a block, so the
// validator can walk them uniformly. Returns []*ast.Template (never
// dereferences nil).
func collectTemplates(b ast.Block) []*ast.Template {
	switch bb := b.(type) {
	case *ast.DetectBlock:
		return []*ast.Template{bb.Label}
	case *ast.RecommendBlock:
		return []*ast.Template{bb.Suggest}
	case *ast.RuleBlock:
		return []*ast.Template{bb.Reason}
	case *ast.PredictBlock:
		return []*ast.Template{bb.Label}
	case *ast.ForecastBlock:
		return []*ast.Template{bb.Label}
	case *ast.ClusterBlock:
		return []*ast.Template{bb.Label}
	case *ast.ClassifyBlock:
		return []*ast.Template{bb.Label}
	case *ast.DecideBlock:
		return []*ast.Template{bb.Label}
	case *ast.SimilarBlock:
		return []*ast.Template{bb.Label}
	case *ast.RelatedBlock:
		return []*ast.Template{bb.Label}
	case *ast.CombineBlock:
		return []*ast.Template{bb.Label}
	}
	return nil
}

// checkOverrides verifies `overrides "name"` clauses on rules: the named rule
// must exist, and you cannot override a strict rule.
func (v *validator) checkOverrides() {
	rules := map[string]*ast.RuleBlock{}
	for _, b := range v.prog.Blocks {
		if r, ok := b.(*ast.RuleBlock); ok {
			rules[r.Name] = r
		}
	}
	ruleNames := make([]string, 0, len(rules))
	for n := range rules {
		ruleNames = append(ruleNames, n)
	}
	for _, b := range v.prog.Blocks {
		r, ok := b.(*ast.RuleBlock)
		if !ok {
			continue
		}
		for _, target := range r.Overrides {
			t, exists := rules[target]
			if !exists {
				v.errAt(r.Pos,
					fmt.Sprintf("rule %q overrides unknown rule %q", r.Name, target),
					suggest(target, ruleNames))
				continue
			}
			if t.Strict {
				v.errAt(r.Pos,
					fmt.Sprintf("rule %q cannot override strict rule %q", r.Name, target),
					"strict rules cannot be defeated")
			}
		}
	}
}

// ─── Collection ───────────────────────────────────────────────────────────────

func (v *validator) collect() {
	for _, b := range v.prog.Blocks {
		name := b.BlockName()
		if def, ok := b.(*ast.DefineBlock); ok {
			v.defines[name] = def
		}
		v.blocks[name] = b
	}
}

// ─── Duplicate names ──────────────────────────────────────────────────────────

func (v *validator) checkDuplicates() {
	for _, b := range v.prog.Blocks {
		// Reactive on-blocks are dispatched at runtime by their trigger anchor +
		// `when` guard, not by name, and are never referenced by name. So several
		// `on assert item { when ... }` branches legitimately share the
		// auto-generated "on assert item" label — exempt them from the
		// unique-name rule (workflow/detect/recommend blocks still need it, as
		// they ARE referenced by name).
		if _, isOn := b.(*ast.OnBlock); isOn {
			continue
		}
		name := b.BlockName()
		pos := blockPos(b)
		if prev, ok := v.seen[name]; ok {
			v.errAt(pos, fmt.Sprintf("duplicate block name %q (first defined at line %d)", name, prev.Line), "")
		} else {
			v.seen[name] = pos
		}
	}
}

// ─── Completeness ─────────────────────────────────────────────────────────────

func (v *validator) checkCompleteness() {
	for _, b := range v.prog.Blocks {
		switch bb := b.(type) {
		case *ast.DetectBlock:
			if bb.Flag == nil {
				v.errAt(bb.Pos, fmt.Sprintf("detect %q requires a 'flag' clause", bb.Name), "add: flag matching items")
			}
			if bb.Recommend != nil && bb.Recommend.Suggest == nil {
				v.errAt(bb.Recommend.Pos, fmt.Sprintf("nested recommend %q requires a 'suggest' clause", bb.Recommend.Name), "")
			}
			v.checkRemediate(bb.Name, bb.Remediate)
			v.checkDetectTune(bb)
		case *ast.RuleBlock:
			if bb.Block == nil && bb.Requires == nil && bb.Allow == nil && len(bb.Do) == 0 {
				v.errAt(bb.Pos, fmt.Sprintf("rule %q requires a 'block', 'allow', 'requires', or 'do' clause", bb.Name), "")
			}
		case *ast.RecommendBlock:
			if bb.Suggest == nil {
				v.errAt(bb.Pos, fmt.Sprintf("recommend %q requires a 'suggest' clause", bb.Name), "")
			}
			v.checkRemediate(bb.Name, bb.Remediate)
		case *ast.EnrichBlock:
			if bb.Call == nil {
				v.errAt(bb.Pos, fmt.Sprintf("enrich %q requires an 'mcp' call", bb.Name), "")
			} else if bb.Call.Server == "" || bb.Call.Tool == "" {
				v.errAt(bb.Pos, fmt.Sprintf("enrich %q: mcp call requires a server and tool name", bb.Name), "")
			}
			if len(bb.Updates) == 0 {
				v.errAt(bb.Pos, fmt.Sprintf("enrich %q requires at least one 'update attr ... from result...' clause", bb.Name), "")
			}
			if bb.StaleAfter.Value <= 0 {
				v.errAt(bb.Pos, fmt.Sprintf("enrich %q requires a positive 'stale_after' duration", bb.Name), "")
			}
		case *ast.CollectBlock:
			if bb.Schedule == "" {
				v.errAt(bb.Pos, fmt.Sprintf("collect %q requires a 'schedule'", bb.Name), "")
			}
			if bb.Call == nil {
				v.errAt(bb.Pos, fmt.Sprintf("collect %q requires an 'mcp' call", bb.Name), "")
			} else if bb.Call.Server == "" || bb.Call.Tool == "" {
				v.errAt(bb.Pos, fmt.Sprintf("collect %q: mcp call requires a server and tool name", bb.Name), "")
			}
			if bb.StoreAs == "" {
				v.errAt(bb.Pos, fmt.Sprintf("collect %q requires a 'store results as <type>' clause", bb.Name), "")
			}
		case *ast.ConnectorBlock:
			if bb.Name == "" {
				v.errAt(bb.Pos, "connector requires a name", "")
			}
			if bb.Plugin == "" {
				v.errAt(bb.Pos, fmt.Sprintf("connector %q requires a plugin (`via <plugin>`)", bb.Name), "")
			}
		case *ast.PredictBlock:
			// A `using model "..."` block drives prediction from the model's
			// inline fitted tree, so the inline training clauses aren't needed.
			if bb.UsingModel != "" {
				if bb.TrainedOn != nil {
					v.errAt(bb.Pos, fmt.Sprintf("predict %q: `using model` and `trained_on` are mutually exclusive", bb.Name), "")
				}
			} else {
				if len(bb.Features) == 0 {
					v.errAt(bb.Pos, fmt.Sprintf("predict %q requires a 'features' clause (or `using model \"...\"`)", bb.Name), "")
				}
				if bb.TrainedOn == nil {
					v.errAt(bb.Pos, fmt.Sprintf("predict %q requires a 'trained_on records where ...' clause (or `using model \"...\"`)", bb.Name), "")
				}
				if bb.LabelAttr == "" {
					v.errAt(bb.Pos, fmt.Sprintf("predict %q requires a 'label_attr \"<name>\"' clause naming the target column on training rows", bb.Name), "")
				}
			}
			if bb.Confidence != nil && (*bb.Confidence < 0 || *bb.Confidence > 1) {
				v.errAt(bb.Pos, fmt.Sprintf("predict %q: confidence must be in [0, 1], got %v", bb.Name, *bb.Confidence), "")
			}
		case *ast.ClassifyBlock:
			// A `using model "..."` block draws features, labels, and the
			// training set from the referenced model, so the inline clauses
			// are not required (and are mutually exclusive with trained_on).
			if bb.UsingModel != "" {
				if bb.TrainedOn != nil {
					v.errAt(bb.Pos, fmt.Sprintf("classify %q: `using model` and `trained_on` are mutually exclusive", bb.Name), "")
				}
			} else {
				if len(bb.Features) == 0 {
					v.errAt(bb.Pos, fmt.Sprintf("classify %q requires a 'features' clause (or `using model \"...\"`)", bb.Name), "")
				}
				if bb.TrainedOn == nil {
					v.errAt(bb.Pos, fmt.Sprintf("classify %q requires a 'trained_on records where ...' clause (or `using model \"...\"`)", bb.Name), "")
				}
				if bb.LabelAttr == "" {
					v.errAt(bb.Pos, fmt.Sprintf("classify %q requires a 'label_attr \"<name>\"' clause naming the class column on training rows", bb.Name), "")
				}
			}
			if bb.Confidence != nil && (*bb.Confidence < 0 || *bb.Confidence > 1) {
				v.errAt(bb.Pos, fmt.Sprintf("classify %q: confidence must be in [0, 1], got %v", bb.Name, *bb.Confidence), "")
			}
		case *ast.DecideBlock:
			// choices are required and must be string literals.
			if len(bb.Choices) < 2 {
				v.errAt(bb.Pos, fmt.Sprintf("decide %q requires a 'choices [ ... ]' clause with at least two options", bb.Name), "")
			}
			for _, c := range bb.Choices {
				lit, ok := c.(*ast.LiteralExpr)
				if !ok {
					continue // non-literal (e.g. attr): let evaluation handle it
				}
				if _, ok := lit.Value.(string); !ok {
					v.errAt(bb.Pos, fmt.Sprintf("decide %q: choices must be string literals", bb.Name), "")
				}
			}
			// Exactly one mode. model = ask + using model; deterministic =
			// features + trained_on. The two must not be mixed, and each must
			// be complete.
			hasModel := bb.UsingModel != "" || bb.Ask != nil
			hasDet := len(bb.Features) > 0 || bb.TrainedOn != nil
			switch {
			case hasModel && hasDet:
				v.errAt(bb.Pos, fmt.Sprintf("decide %q: model mode (`ask`/`using model`) and deterministic mode (`features`/`trained_on`) are mutually exclusive", bb.Name), "")
			case hasModel:
				if bb.Ask == nil {
					v.errAt(bb.Pos, fmt.Sprintf("decide %q: model mode requires an 'ask <expr>' clause", bb.Name), "")
				}
				if bb.UsingModel == "" {
					v.errAt(bb.Pos, fmt.Sprintf("decide %q: model mode requires a 'using model \"<name>\"' clause", bb.Name), "")
				}
			case hasDet:
				if len(bb.Features) == 0 {
					v.errAt(bb.Pos, fmt.Sprintf("decide %q: deterministic mode requires a 'features [ ... ]' clause", bb.Name), "")
				}
				if bb.TrainedOn == nil {
					v.errAt(bb.Pos, fmt.Sprintf("decide %q: deterministic mode requires a 'trained_on records where ...' clause", bb.Name), "")
				}
				if bb.LabelAttr == "" {
					v.errAt(bb.Pos, fmt.Sprintf("decide %q: deterministic mode requires a 'label_attr \"<name>\"' clause naming the class column on training rows", bb.Name), "")
				}
			default:
				v.errAt(bb.Pos, fmt.Sprintf("decide %q needs a mode: either `ask ... using model \"...\"` or `features [...] trained_on records where ...`", bb.Name), "")
			}
			if bb.Confidence != nil && (*bb.Confidence < 0 || *bb.Confidence > 1) {
				v.errAt(bb.Pos, fmt.Sprintf("decide %q: confidence must be in [0, 1], got %v", bb.Name, *bb.Confidence), "")
			}
		case *ast.ForecastBlock:
			if bb.Series.Attr == nil {
				v.errAt(bb.Pos, fmt.Sprintf("forecast %q requires a 'series' clause", bb.Name), "")
			}
		case *ast.CombineBlock:
			v.checkCombine(bb)
		case *ast.RelatedBlock:
			if bb.To == nil && len(bb.Seeds) == 0 {
				v.errAt(bb.Pos, fmt.Sprintf("find related %q requires a 'to' or 'seeds [...]' clause", bb.Name), "")
			}
			if bb.TopK != nil && *bb.TopK <= 0 {
				v.errAt(bb.Pos, fmt.Sprintf("find related %q: top_k must be > 0", bb.Name), "")
			}
			if bb.Damping != nil && (*bb.Damping < 0 || *bb.Damping >= 1) {
				v.errAt(bb.Pos, fmt.Sprintf("find related %q: damping must be in [0, 1)", bb.Name), "")
			}
			if bb.Tol != nil && *bb.Tol <= 0 {
				v.errAt(bb.Pos, fmt.Sprintf("find related %q: tolerance must be > 0", bb.Name), "")
			}
			if bb.MaxIter != nil && *bb.MaxIter <= 0 {
				v.errAt(bb.Pos, fmt.Sprintf("find related %q: max_iterations must be > 0", bb.Name), "")
			}
		}
	}
}

// checkCombine validates combine-block specific constraints.
//
//   - At least one optimize clause.
//   - No duplicate (direction, objective-key) pairs.
//   - v1 ranking mode (no `select`): objectives must be `attr "name"`.
//   - v2 subset mode (with `select K`): objectives may also be aggregates
//     (`total(attr "x")`, `count(records)`, `avg(attr "y")`). Subject_to
//     constraints must have an aggregate LHS and a numeric literal RHS.
//   - `select K` requires K >= 1.
func (v *validator) checkCombine(bb *ast.CombineBlock) {
	// ACO sequence mode validates separately — no minimize/maximize required;
	// the implicit objective is total euclidean distance along the sequence.
	if bb.Sequence {
		v.checkCombineSequence(bb)
		return
	}
	if len(bb.Optimize) == 0 {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q requires at least one 'minimize' or 'maximize' clause", bb.Name), "")
		return
	}
	subsetMode := bb.Select != nil
	if subsetMode && bb.Select.Size < 1 {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: select size must be >= 1, got %d", bb.Name, bb.Select.Size), "")
	}

	// ILP solver requires single-objective subset selection with linear
	// aggregate constraints. Multi-objective + ILP is intentionally rejected
	// for v2.1 — exact multi-objective Pareto is exponential and the user
	// should explicitly choose GA in that case.
	if bb.Solver == "linear" {
		if !subsetMode {
			v.errAt(bb.Pos, fmt.Sprintf("combine %q: solver linear requires `select K from records`", bb.Name), "")
		}
		if len(bb.Optimize) > 1 {
			v.errAt(bb.Pos, fmt.Sprintf("combine %q: solver linear supports a single objective (got %d) — remove the extra clause or drop `solver linear` to use the GA backend", bb.Name, len(bb.Optimize)), "")
		}
	}

	seen := map[string]bool{}
	for _, oc := range bb.Optimize {
		key, ok := combineObjectiveKey(oc.Attr, subsetMode)
		if !ok {
			if subsetMode {
				v.errAt(bb.Pos, fmt.Sprintf("combine %q: objective must be `attr \"name\"` or `total/count/avg(...)`", bb.Name), "")
			} else {
				v.errAt(bb.Pos, fmt.Sprintf("combine %q: objective must be `attr \"name\"` (use `select K from records` to enable aggregates)", bb.Name), "")
			}
			continue
		}
		fullKey := oc.Direction + ":" + key
		if seen[fullKey] {
			v.errAt(bb.Pos, fmt.Sprintf("combine %q: duplicate objective %s %s", bb.Name, oc.Direction, key), "")
		}
		seen[fullKey] = true
	}

	if !subsetMode && len(bb.Constraints) > 0 {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: `subject_to` requires `select K from records`", bb.Name), "")
	}
	for _, c := range bb.Constraints {
		if _, ok := c.Left.(*ast.AggregateExpr); !ok {
			v.errAt(c.Pos, fmt.Sprintf("combine %q: subject_to LHS must be an aggregate (total/count/avg)", bb.Name), "")
		}
		if lit, ok := c.Right.(*ast.LiteralExpr); !ok {
			v.errAt(c.Pos, fmt.Sprintf("combine %q: subject_to RHS must be a numeric literal", bb.Name), "")
		} else if _, ok := lit.Value.(float64); !ok {
			v.errAt(c.Pos, fmt.Sprintf("combine %q: subject_to RHS must be numeric, got %T", bb.Name, lit.Value), "")
		}
	}
}

// checkDetectTune validates `tune against test "..."` clauses on detect blocks.
// The referenced test must exist; the block must contain a tunable ML
// primitive (today: anomaly only — extensible).
// checkRemediate validates a remediate clause's structural well-formedness:
// at least one call, each with a non-empty server and tool. (There is no
// MCP-server registry to check names against — same as workflow steps.)
func (v *validator) checkRemediate(blockName string, r *ast.RemediateClause) {
	if r == nil {
		return
	}
	if len(r.Body) == 0 {
		v.errAt(r.Pos, fmt.Sprintf("%q: remediate block requires at least one mcp call", blockName), "")
		return
	}
	calls := 0
	v.walkActions(blockName, r.Pos, r.Body, &calls)
	if calls == 0 {
		v.errAt(r.Pos, fmt.Sprintf("%q: remediate block requires at least one mcp call", blockName), "")
	}
}

// walkActions validates an imperative action body: every leaf MCP call needs
// a server and tool, for-each needs a loop variable, and while must carry a
// positive iteration cap. It tallies the reachable MCP calls so an all
// control-flow body with no effect is rejected.
func (v *validator) walkActions(blockName string, pos ast.Pos, actions []ast.Action, calls *int) {
	for _, a := range actions {
		switch act := a.(type) {
		case *ast.MCPAction:
			*calls++
			if act.Call.Server == "" || act.Call.Tool == "" {
				v.errAt(pos, fmt.Sprintf("%q: remediate mcp call requires a server and tool name", blockName), "")
			}
		case *ast.IfAction:
			v.walkActions(blockName, act.Pos, act.Then, calls)
			v.walkActions(blockName, act.Pos, act.Else, calls)
		case *ast.ForEachAction:
			if act.Variable == "" {
				v.errAt(act.Pos, fmt.Sprintf("%q: for each requires a loop variable", blockName), "")
			}
			v.walkActions(blockName, act.Pos, act.Body, calls)
		case *ast.WhileAction:
			if act.MaxIter <= 0 {
				v.errAt(act.Pos, fmt.Sprintf("%q: while loop needs a positive iteration bound", blockName), "")
			}
			v.walkActions(blockName, act.Pos, act.Body, calls)
		}
	}
}

func (v *validator) checkDetectTune(bb *ast.DetectBlock) {
	if bb.Tune == nil {
		return
	}
	if bb.Tune.AgainstTest == "" {
		v.errAt(bb.Pos, fmt.Sprintf("detect %q: tune clause requires a test name", bb.Name), "")
		return
	}
	// Only enforce existence when the program actually contains test blocks
	// (i.e. we were given a .tln.test file). During `tln build` the rule
	// file is validated in isolation, so the labeled fixture isn't visible
	// yet; testrunner picks up the same check at run time.
	if v.programHasTests() && !v.testExists(bb.Tune.AgainstTest) {
		v.errAt(bb.Pos, fmt.Sprintf("detect %q: tune references unknown test %q", bb.Name, bb.Tune.AgainstTest), "")
	}
	if !detectHasTunablePrimitive(bb) {
		v.errAt(bb.Pos, fmt.Sprintf("detect %q: tune clause requires a tunable ML primitive (today: anomaly)", bb.Name), "")
	}
}

// testExists reports whether any TestBlock in the program (typically from
// a .tln.test file merged in by the CLI) matches the given name.
func (v *validator) testExists(name string) bool {
	for _, b := range v.prog.Blocks {
		if tb, ok := b.(*ast.TestBlock); ok && tb.Name == name {
			return true
		}
	}
	return false
}

// programHasTests reports whether the program contains any TestBlock.
// Used to gate validator checks that depend on labeled fixtures being
// visible (which is only true once `tln test` merges the .tln.test).
func (v *validator) programHasTests() bool {
	for _, b := range v.prog.Blocks {
		if _, ok := b.(*ast.TestBlock); ok {
			return true
		}
	}
	return false
}

// detectHasTunablePrimitive reports whether a detect block contains a
// primitive whose parameters ABC tuning currently supports. v1 tunes
// `anomaly_zscore` (threshold) and `learned_threshold` (percentile).
func detectHasTunablePrimitive(bb *ast.DetectBlock) bool {
	if bb.Anomaly != nil {
		return true
	}
	for _, cond := range bb.Selector.Conditions {
		if hasTunableCondition(cond) {
			return true
		}
	}
	return false
}

// checkCalculate validates `calculate` clauses: the average / sum / wma
// methods aggregate a value column and so require an `of attr "X"`; count
// does not.
func (v *validator) checkCalculate() {
	for _, b := range v.prog.Blocks {
		var calcs []ast.CalculateClause
		switch bb := b.(type) {
		case *ast.DetectBlock:
			calcs = bb.Calculate
		case *ast.RecommendBlock:
			calcs = bb.Calculate
		}
		for _, c := range calcs {
			switch c.Method {
			case "average", "sum", "wma":
				if c.Value == nil {
					v.errAt(blockPos(b),
						fmt.Sprintf("calculate %q: %s requires a value column — add `of attr \"...\"`", c.Name, methodLabel(c.Method)),
						"e.g. calculate rate from records of attr \"usage\" average")
				}
			}
		}
	}
}

func methodLabel(m string) string {
	if m == "wma" {
		return "weighted_moving_average"
	}
	return m
}

// checkCorrelation validates `correlates_with` conditions: the Pearson
// coefficient r is always in [-1, 1], so a threshold outside that range can
// never be satisfied (or is always satisfied) — almost certainly a mistake.
func (v *validator) checkCorrelation() {
	for _, b := range v.prog.Blocks {
		walkBlockConditions(b, func(c ast.Condition) {
			cc, ok := c.(*ast.CorrelationCondition)
			if !ok {
				return
			}
			if cc.Threshold < -1 || cc.Threshold > 1 {
				v.errAt(blockPos(b),
					fmt.Sprintf("correlates_with threshold %g is outside [-1, 1]; a Pearson correlation can never satisfy it", cc.Threshold),
					"use a threshold between -1 and 1")
			}
		})
	}
}

// hasTunableCondition walks the selector tree looking for primitive-tunable
// shapes: anomaly compared_to OR learned_threshold comparisons.
func hasTunableCondition(cond ast.Condition) bool {
	switch c := cond.(type) {
	case *ast.AnomalyCondition:
		return true
	case *ast.CompareCondition:
		if _, ok := c.Right.(*ast.LearnedThresholdExpr); ok {
			return true
		}
	case *ast.LogicalCondition:
		return hasTunableCondition(c.Left) || hasTunableCondition(c.Right)
	case *ast.NotCondition:
		return hasTunableCondition(c.Inner)
	}
	return false
}

// checkCombineSequence validates the ACO sequence mode: requires a
// `coordinates` clause naming two attr exprs; rejects `select`, `subject_to`,
// `minimize`/`maximize`, and `solver linear` since none apply to routing.
func (v *validator) checkCombineSequence(bb *ast.CombineBlock) {
	if bb.Coordinates == nil {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: sequence mode requires `coordinates attr \"X\", attr \"Y\"`", bb.Name), "")
		return
	}
	if _, ok := bb.Coordinates.X.(*ast.AttrExpr); !ok {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: coordinates X must be `attr \"name\"`", bb.Name), "")
	}
	if _, ok := bb.Coordinates.Y.(*ast.AttrExpr); !ok {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: coordinates Y must be `attr \"name\"`", bb.Name), "")
	}
	if len(bb.Optimize) > 0 {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: sequence mode optimizes total distance implicitly — remove `minimize`/`maximize` (or drop `sequence` to use GA)", bb.Name), "")
	}
	if bb.Select != nil {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: sequence mode visits every candidate — `select K` is not applicable", bb.Name), "")
	}
	if len(bb.Constraints) > 0 {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: sequence mode does not yet support `subject_to`", bb.Name), "")
	}
	if bb.Solver != "" {
		v.errAt(bb.Pos, fmt.Sprintf("combine %q: sequence mode uses ACO; `solver` is not applicable", bb.Name), "")
	}
}

// combineObjectiveKey produces a stable identifier for an objective expression
// for duplicate detection, and reports whether the shape is allowed.
func combineObjectiveKey(e ast.Expr, allowAggregate bool) (string, bool) {
	switch x := e.(type) {
	case *ast.AttrExpr:
		return "attr:" + x.Name, true
	case *ast.AggregateExpr:
		if !allowAggregate {
			return "", false
		}
		if attr, ok := x.Arg.(*ast.AttrExpr); ok {
			return x.Fn + ":attr:" + attr.Name, true
		}
		if x.Arg == nil && x.Fn == "count" {
			return "count:records", true
		}
		return "", false
	}
	return "", false
}

// ─── Reference resolution ─────────────────────────────────────────────────────

func (v *validator) checkReferences() {
	defineNames := names(v.defines)
	blockNames := allNames(v.blocks)

	for _, b := range v.prog.Blocks {
		pos := blockPos(b)
		walkBlockConditions(b, func(cond ast.Condition) {
			switch c := cond.(type) {
			case *ast.IsCondition:
				if c.Name != "" {
					if _, ok := v.defines[c.Name]; !ok {
						v.errAt(pos,
							fmt.Sprintf("undefined define reference %q", c.Name),
							suggest(c.Name, defineNames))
					}
				}
			case *ast.BlockMatchesCondition:
				if _, ok := v.blocks[c.Name]; !ok {
					v.errAt(pos,
						fmt.Sprintf("undefined block reference %q", c.Name),
						suggest(c.Name, blockNames))
				}
			}
		})

		// on-block bodies reference other blocks by name via
		// `recommend "X"` / `detect "X"` / `workflow "X"`; the target
		// must be defined.
		if on, ok := b.(*ast.OnBlock); ok {
			for _, act := range on.Actions {
				ref, ok := act.(*ast.BlockRefAction)
				if !ok {
					continue
				}
				if _, ok := v.blocks[ref.Name]; !ok {
					v.errAt(pos,
						fmt.Sprintf("%s references undefined block %q", on.Name, ref.Name),
						suggest(ref.Name, blockNames))
				}
			}
		}
	}
}

// ─── Type checking ────────────────────────────────────────────────────────────

func (v *validator) checkTypes() {
	for _, b := range v.prog.Blocks {
		pos := blockPos(b)
		walkBlockConditions(b, func(cond ast.Condition) {
			c, ok := cond.(*ast.CompareCondition)
			if !ok {
				return
			}
			switch c.Op {
			case ">", "<", ">=", "<=":
				if isStringLiteral(c.Right) {
					v.warnAt(pos,
						fmt.Sprintf("numeric operator %q used with string value", c.Op),
						"use == or != for string comparisons")
				}
			}
		})
	}
}

// ─── Cycle detection ──────────────────────────────────────────────────────────

func (v *validator) checkCycles() {
	type dfsState int
	const (
		unvisited dfsState = iota
		inStack
		done
	)
	states := make(map[string]dfsState, len(v.defines))

	var dfs func(name string, path []string)
	dfs = func(name string, path []string) {
		if states[name] == inStack {
			for i, p := range path {
				if p == name {
					chain := append(append([]string{}, path[i:]...), name)
					def := v.defines[name]
					v.errAt(def.Pos,
						fmt.Sprintf("circular dependency: %s", strings.Join(chain, " → ")), "")
					return
				}
			}
		}
		if states[name] != unvisited {
			return
		}
		states[name] = inStack
		if def, ok := v.defines[name]; ok {
			for _, cond := range def.Conditions {
				for _, ref := range collectIsRefs(cond) {
					dfs(ref, append(path, name))
				}
			}
		}
		states[name] = done
	}

	for name := range v.defines {
		if states[name] == unvisited {
			dfs(name, nil)
		}
	}
}

// ─── Workflow validation ──────────────────────────────────────────────────────

func (v *validator) checkWorkflows() {
	for _, b := range v.prog.Blocks {
		wf, ok := b.(*ast.WorkflowBlock)
		if !ok {
			continue
		}

		stepNames := map[string]bool{}
		for _, step := range wf.Steps {
			// Each step must have an MCP call.
			if step.MCPCall == nil {
				v.errAt(wf.Pos, fmt.Sprintf("step %q in workflow %q has no mcp call", step.Name, wf.Name), "")
			}
			// Step names must be unique within the workflow.
			if stepNames[step.Name] {
				v.errAt(wf.Pos, fmt.Sprintf("duplicate step name %q in workflow %q", step.Name, wf.Name), "")
			}
			stepNames[step.Name] = true
		}

		// Validate depends_on references.
		for _, step := range wf.Steps {
			for _, dep := range step.DependsOn {
				if !stepNames[dep] {
					v.errAt(wf.Pos,
						fmt.Sprintf("step %q depends on undefined step %q in workflow %q", step.Name, dep, wf.Name),
						suggest(dep, names(stepNames)))
				}
			}
		}

		// Validate `when` guard step references: a guard may only read a step
		// the guarded step declares in depends_on, so the referenced result is
		// guaranteed to have run before the guard is evaluated.
		for _, step := range wf.Steps {
			if step.When == nil {
				continue
			}
			deps := map[string]bool{}
			for _, d := range step.DependsOn {
				deps[d] = true
			}
			for _, ref := range stepRefsInCondition(step.When) {
				switch {
				case !stepNames[ref]:
					v.errAt(wf.Pos,
						fmt.Sprintf("step %q when-guard references undefined step %q in workflow %q",
							step.Name, ref, wf.Name),
						suggest(ref, names(stepNames)))
				case ref != step.Name && !deps[ref]:
					v.errAt(wf.Pos,
						fmt.Sprintf("step %q when-guard reads step %q but does not depend on it — "+
							"add depends_on %q in workflow %q", step.Name, ref, ref, wf.Name), "")
				}
			}
		}

		// Cycle detection via DFS.
		type dfsState int
		const (
			unvisited dfsState = iota
			inStack
			done
		)
		states := make(map[string]dfsState, len(wf.Steps))
		adj := map[string][]string{}
		for _, step := range wf.Steps {
			for _, dep := range step.DependsOn {
				adj[dep] = append(adj[dep], step.Name)
			}
		}

		var dfs func(name string, path []string)
		dfs = func(name string, path []string) {
			if states[name] == inStack {
				for i, p := range path {
					if p == name {
						chain := append(append([]string{}, path[i:]...), name)
						v.errAt(wf.Pos,
							fmt.Sprintf("circular dependency in workflow %q: %s", wf.Name, strings.Join(chain, " → ")), "")
						return
					}
				}
			}
			if states[name] != unvisited {
				return
			}
			states[name] = inStack
			for _, next := range adj[name] {
				dfs(next, append(path, name))
			}
			states[name] = done
		}

		for _, step := range wf.Steps {
			if states[step.Name] == unvisited {
				dfs(step.Name, nil)
			}
		}
	}
}

// stepRefsInCondition returns the step names referenced by step("name").…
// operands anywhere in a condition, so workflow validation can confirm a
// guard only reads steps it depends on. Best-effort over the condition/expr
// shapes a guard realistically uses; unrecognised nodes contribute nothing.
func stepRefsInCondition(c ast.Condition) []string {
	var out []string
	var walkExpr func(ast.Expr)
	walkExpr = func(e ast.Expr) {
		switch ee := e.(type) {
		case *ast.StepResultExpr:
			out = append(out, ee.StepName)
		case *ast.MapExpr:
			walkExpr(ee.Source)
		case *ast.FindExpr:
			walkExpr(ee.Source)
		case *ast.CallExpr:
			for _, a := range ee.Args {
				walkExpr(a)
			}
		case *ast.BinaryExpr:
			walkExpr(ee.Left)
			walkExpr(ee.Right)
		case *ast.UnaryExpr:
			walkExpr(ee.Operand)
		case *ast.AggregateExpr:
			if ee.Arg != nil {
				walkExpr(ee.Arg)
			}
		}
	}
	var walkCond func(ast.Condition)
	walkCond = func(c ast.Condition) {
		switch cc := c.(type) {
		case *ast.CompareCondition:
			walkExpr(cc.Left)
			walkExpr(cc.Right)
		case *ast.LogicalCondition:
			walkCond(cc.Left)
			walkCond(cc.Right)
		case *ast.NotCondition:
			walkCond(cc.Inner)
		case *ast.MembershipCondition:
			walkExpr(cc.Expr)
			for _, m := range cc.Members {
				walkExpr(m)
			}
		case *ast.StringMatchCondition:
			walkExpr(cc.Subject)
		case *ast.IsCondition:
			walkExpr(cc.Subject)
		case *ast.HasCondition:
			walkExpr(cc.Subject)
		}
	}
	walkCond(c)
	return out
}

// checkAsOf enforces the v1 restrictions on `was ( <cond> ) N <unit> ago`:
// it must be a top-level `and` conjunct of a detect or rule selector.
// Nested inside `or`/`not` — or used in a block that doesn't run selector
// candidates — the planner would silently drop it, so those are rejected.
func (v *validator) checkAsOf() {
	for _, b := range v.prog.Blocks {
		allowed := map[*ast.AsOfCondition]bool{}
		switch bb := b.(type) {
		case *ast.DetectBlock:
			markTopLevelAsOf(bb.Selector.Conditions, allowed)
		case *ast.RuleBlock:
			if bb.Selector != nil {
				markTopLevelAsOf(bb.Selector.Conditions, allowed)
			}
		}
		walkBlockConditions(b, func(c ast.Condition) {
			if ao, ok := c.(*ast.AsOfCondition); ok && !allowed[ao] {
				v.errAt(blockPos(b),
					"`was ... ago` must be a top-level `and` condition of a detect or rule selector",
					"move it out of any or/not grouping and join it with `and`")
			}
		})
	}
}

// markTopLevelAsOf records every AsOfCondition reachable from the given
// selector conditions through only `and` nodes.
func markTopLevelAsOf(conds []ast.Condition, allowed map[*ast.AsOfCondition]bool) {
	var flatten func(ast.Condition)
	flatten = func(c ast.Condition) {
		switch cc := c.(type) {
		case *ast.LogicalCondition:
			if cc.Op == "and" {
				flatten(cc.Left)
				flatten(cc.Right)
			}
		case *ast.AsOfCondition:
			allowed[cc] = true
		}
	}
	for _, c := range conds {
		flatten(c)
	}
}

// ─── AST walkers ──────────────────────────────────────────────────────────────

func walkBlockConditions(b ast.Block, fn func(ast.Condition)) {
	switch bb := b.(type) {
	case *ast.DetectBlock:
		walkSelector(bb.Selector, fn)
		if bb.Recommend != nil {
			walkCond(bb.Recommend.When, fn)
		}
	case *ast.RuleBlock:
		if bb.Selector != nil {
			walkSelector(*bb.Selector, fn)
		}
		walkCond(bb.When, fn)
	case *ast.RecommendBlock:
		walkCond(bb.When, fn)
		for _, calc := range bb.Calculate {
			for _, c := range calc.Where {
				walkCond(c, fn)
			}
		}
	case *ast.DefineBlock:
		for _, c := range bb.Conditions {
			walkCond(c, fn)
		}
		if bb.ForEach != nil {
			for _, c := range bb.ForEach.Body {
				walkCond(c, fn)
			}
		}
	case *ast.PredictBlock:
		walkSelector(bb.Selector, fn)
		if bb.TrainedOn != nil {
			for _, c := range bb.TrainedOn.Conditions {
				walkCond(c, fn)
			}
		}
	case *ast.ClassifyBlock:
		walkSelector(bb.Selector, fn)
		if bb.TrainedOn != nil {
			for _, c := range bb.TrainedOn.Conditions {
				walkCond(c, fn)
			}
		}
	case *ast.DecideBlock:
		walkSelector(bb.Selector, fn)
		if bb.TrainedOn != nil {
			for _, c := range bb.TrainedOn.Conditions {
				walkCond(c, fn)
			}
		}
	case *ast.ForecastBlock:
		walkSelector(bb.Selector, fn)
		walkCond(bb.When, fn)
	case *ast.RelatedBlock:
		walkSelector(bb.Selector, fn)
	case *ast.DeriveBlock:
		walkSelector(bb.Selector, fn)
	}
}

func walkSelector(sel ast.Selector, fn func(ast.Condition)) {
	for _, c := range sel.Conditions {
		walkCond(c, fn)
	}
}

func walkCond(cond ast.Condition, fn func(ast.Condition)) {
	if cond == nil {
		return
	}
	fn(cond)
	switch c := cond.(type) {
	case *ast.LogicalCondition:
		walkCond(c.Left, fn)
		walkCond(c.Right, fn)
	case *ast.NotCondition:
		walkCond(c.Inner, fn)
	}
}

// walkCondPolarity is walkCond that threads negation parity: fn receives each
// condition together with whether it sits under an odd number of `not`s.
// `not not p` is positive again — parity, not mere presence, is what makes a
// derive cycle unsafe.
func walkCondPolarity(cond ast.Condition, negated bool, fn func(ast.Condition, bool)) {
	if cond == nil {
		return
	}
	fn(cond, negated)
	switch c := cond.(type) {
	case *ast.LogicalCondition:
		walkCondPolarity(c.Left, negated, fn)
		walkCondPolarity(c.Right, negated, fn)
	case *ast.NotCondition:
		walkCondPolarity(c.Inner, !negated, fn)
	}
}

func collectIsRefs(cond ast.Condition) []string {
	var refs []string
	walkCond(cond, func(c ast.Condition) {
		if ic, ok := c.(*ast.IsCondition); ok && ic.Name != "" {
			refs = append(refs, ic.Name)
		}
	})
	return refs
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func blockPos(b ast.Block) ast.Pos {
	switch bb := b.(type) {
	case *ast.DetectBlock:
		return bb.Pos
	case *ast.RuleBlock:
		return bb.Pos
	case *ast.RecommendBlock:
		return bb.Pos
	case *ast.CombineBlock:
		return bb.Pos
	case *ast.DefineBlock:
		return bb.Pos
	case *ast.WorkflowBlock:
		return bb.Pos
	case *ast.PredictBlock:
		return bb.Pos
	case *ast.ForecastBlock:
		return bb.Pos
	case *ast.ClusterBlock:
		return bb.Pos
	case *ast.ClassifyBlock:
		return bb.Pos
	case *ast.DecideBlock:
		return bb.Pos
	case *ast.SimilarBlock:
		return bb.Pos
	case *ast.RelatedBlock:
		return bb.Pos
	case *ast.OnBlock:
		return bb.Pos
	case *ast.ConstraintBlock:
		return bb.Pos
	case *ast.EnrichBlock:
		return bb.Pos
	case *ast.CollectBlock:
		return bb.Pos
	case *ast.ThresholdBlock:
		return bb.Pos
	case *ast.DeriveBlock:
		return bb.Pos
	}
	return ast.Pos{}
}

func isStringLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.LiteralExpr)
	if !ok {
		return false
	}
	_, isStr := lit.Value.(string)
	return isStr
}

func names[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func allNames(m map[string]ast.Block) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (v *validator) errAt(pos ast.Pos, msg, hint string) {
	v.diags.AddError(v.file, pos.Line, pos.Col, msg, hint)
}

func (v *validator) warnAt(pos ast.Pos, msg, hint string) {
	v.diags.AddWarning(v.file, pos.Line, pos.Col, msg, hint)
}

// suggest returns "did you mean X?" when Levenshtein distance ≤ 2.
func suggest(name string, candidates []string) string {
	best, bestDist := "", 999
	for _, c := range candidates {
		if d := levenshtein(name, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	if bestDist <= 2 && best != "" {
		return fmt.Sprintf("did you mean %q?", best)
	}
	return ""
}

func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	row := make([]int, lb+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= la; i++ {
		prev := row[0]
		row[0] = i
		for j := 1; j <= lb; j++ {
			old := row[j]
			if a[i-1] == b[j-1] {
				row[j] = prev
			} else {
				row[j] = 1 + min3(prev, row[j], row[j-1])
			}
			prev = old
		}
	}
	return row[lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
