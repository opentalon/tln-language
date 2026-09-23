package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/opentalon/tln-language/internal/ast"
	tlnlog "github.com/opentalon/tln-language/internal/log"
	"github.com/opentalon/tln-language/internal/planner"
	"github.com/opentalon/tln-language/internal/template"
)

// execDecideModel runs a `decide "name" { ask ... using model "m" ... }` block.
// For each candidate entity it renders the `ask` state expression against the
// entity's attributes, then calls the injected ToolResolver with
// (server=model, tool="decide", args={state, choices}). The resolver — a Jev /
// laya / logits-style System-1 decision model on the host side — returns
// {chosen, confidence, probabilities}. A `confidence >=` bound drops
// low-confidence decisions here on the executor path; the deterministic mode's
// equivalent gate is applied separately on the `tln test` / `tln explain` path
// (narrowByML), since the two modes run on different evaluation paths. With no
// ToolResolver injected the step stubs out (like execMCPCall), so a plan that
// references a model still runs.
//
// Model decisions are a non-deterministic external boundary; the result is
// recorded as a fact for audit but never claims the reproducibility guarantee
// the deterministic (classify_knn) mode carries.
func (e *Executor) execDecideModel(ctx context.Context, gc *planner.GoComputation, vars map[string]any) (any, error) {
	askExpr, _ := gc.Params["ask"].(ast.Expr)
	model, _ := gc.Params["model"].(string)
	choices, _ := gc.Params["choices"].([]string)
	confBound, hasConf := gc.Params["confidence"].(float64)
	rows, _ := vars[gc.Input].([][]any)

	summary := map[string]any{
		"function":   planner.FuncDecideModel,
		"candidates": len(rows),
		"results":    []any{},
	}
	if e.Tools == nil || askExpr == nil || len(rows) == 0 {
		summary["status"] = "stub"
		return summary, nil
	}

	// Unique candidate entity ids, preserving query order.
	var ids []int
	seen := map[int]bool{}
	for _, r := range rows {
		if len(r) == 0 {
			continue
		}
		id, ok := toEntityID(r[0])
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}

	// Attributes the `ask` expression reads, fetched once for the batch.
	nameSet := map[string]bool{}
	collectExprAttrs(askExpr, func(n string) {
		if n != "" && n != "id" {
			nameSet[n] = true
		}
	})
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	attrsByID := e.fetchEntityAttrs(ctx, ids, names)

	results := make([]any, 0, len(ids))
	for _, id := range ids {
		row := attrsByID[id]
		if row == nil {
			row = map[string]any{}
		}
		row["id"] = id
		rctx := template.RenderContext{Row: row}
		state := fmt.Sprint(resolveRemediateArg(askExpr, row, rctx))

		start := time.Now()
		resp, err := e.Tools.Call(ctx, model, "decide", map[string]any{
			"state":   state,
			"choices": choices,
		})
		tlnlog.MCPCall(ctx, model, "decide", statusOf(err), time.Since(start), err)
		if err != nil {
			return summary, fmt.Errorf("decide model %q: %w", model, err)
		}
		m, ok := resp.(map[string]any)
		if !ok {
			continue
		}
		chosen, _ := m["chosen"].(string)
		conf, hasVal := toFloat(m["confidence"])
		if !hasVal {
			// The resolver omitted an explicit confidence. Fall back to the
			// chosen option's probability so a missing field doesn't silently
			// read as 0 and drop every decision under a confidence gate.
			if probs, ok := m["probabilities"].(map[string]any); ok {
				conf, hasVal = toFloat(probs[chosen])
			}
		}
		// Only gate when we actually have a confidence signal; a resolver that
		// returns neither confidence nor probabilities can't be thresholded, so
		// the decision passes through rather than being silently dropped.
		if hasConf && hasVal && conf < confBound {
			continue
		}
		results = append(results, map[string]any{
			"entity_id":     id,
			"chosen":        chosen,
			"confidence":    conf,
			"probabilities": m["probabilities"],
		})
	}
	summary["results"] = results
	summary["status"] = "ok"
	return summary, nil
}

func statusOf(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
