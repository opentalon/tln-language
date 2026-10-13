package planner

import (
	"sort"

	"github.com/opentalon/tln-language/internal/actions"
	"github.com/opentalon/tln-language/internal/ast"
)

func ExecutionOrder(plans map[string]*QueryPlan) (order, cycle []string) {
	names := make([]string, 0, len(plans))
	for name := range plans {
		names = append(names, name)
	}
	sort.Strings(names)

	writers := map[string][]string{}
	for _, name := range names {
		for _, a := range dedupe(plans[name].Writes) {
			writers[a] = append(writers[a], name)
		}
	}

	inDegree := make(map[string]int, len(names))
	dependents := map[string][]string{}
	for _, name := range names {
		deps := map[string]bool{}
		for _, a := range plans[name].Reads {
			for _, w := range writers[a] {
				if w != name {
					deps[w] = true
				}
			}
		}
		inDegree[name] = len(deps)
		for w := range deps {
			dependents[w] = append(dependents[w], name)
		}
	}

	done := make(map[string]bool, len(names))
	var ready []string
	for _, name := range names {
		if inDegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	for len(order) < len(names) {
		if len(ready) == 0 {
			for _, name := range names {
				if !done[name] && onCycle(name, dependents, done) {
					ready = append(ready, name)
					cycle = append(cycle, name)
					break
				}
			}
		}
		sort.Strings(ready)
		name := ready[0]
		ready = ready[1:]
		if done[name] {
			continue
		}
		done[name] = true
		order = append(order, name)
		for _, d := range dependents[name] {
			inDegree[d]--
			if inDegree[d] == 0 && !done[d] {
				ready = append(ready, d)
			}
		}
	}
	return order, cycle
}

func onCycle(start string, dependents map[string][]string, done map[string]bool) bool {
	seen := map[string]bool{}
	stack := append([]string(nil), dependents[start]...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == start {
			return true
		}
		if seen[n] || done[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, dependents[n]...)
	}
	return false
}

func (p *planner) blockReads(b ast.Block) []string {
	var out []string
	add := func(n string) { out = append(out, n) }
	cond := func(c ast.Condition) { p.walkReads(c, add, map[string]bool{}) }
	sel := func(s ast.Selector) {
		for _, c := range s.Conditions {
			cond(c)
		}
	}
	exprs := func(es []ast.Expr) {
		for _, e := range es {
			walkExprAttrs(e, add)
		}
	}
	switch bb := b.(type) {
	case *ast.RuleBlock:
		if bb.Selector != nil {
			sel(*bb.Selector)
		}
		cond(bb.When)
		out = append(out, actions.ReferencedAttrs(bb)...)
	case *ast.EnrichBlock:
		sel(bb.Selector)
		if bb.Call != nil {
			for _, e := range bb.Call.Args {
				walkExprAttrs(e, add)
			}
		}
	case *ast.DetectBlock:
		sel(bb.Selector)
		for _, c := range bb.Having {
			cond(c)
		}
	case *ast.ClassifyBlock:
		sel(bb.Selector)
		exprs(bb.Features)
	case *ast.PredictBlock:
		sel(bb.Selector)
		exprs(bb.Features)
	case *ast.DecideBlock:
		sel(bb.Selector)
		exprs(bb.Features)
		walkExprAttrs(bb.Ask, add)
	case *ast.ClusterBlock:
		sel(bb.Selector)
		exprs(bb.ByAttrs)
	case *ast.SimilarBlock:
		sel(bb.Selector)
		walkExprAttrs(bb.To, add)
	case *ast.RelatedBlock:
		sel(bb.Selector)
		walkExprAttrs(bb.To, add)
		exprs(bb.Seeds)
	case *ast.CombineBlock:
		sel(bb.Selector)
	case *ast.ForecastBlock:
		sel(bb.Selector)
		cond(bb.When)
	case *ast.RecommendBlock:
		cond(bb.When)
	case *ast.StateMachineBlock:
		out = append(out, collectAttrNames(bb)...)
	}
	return dedupe(out)
}

func (p *planner) walkReads(c ast.Condition, add func(string), seen map[string]bool) {
	switch cc := c.(type) {
	case *ast.LogicalCondition:
		p.walkReads(cc.Left, add, seen)
		p.walkReads(cc.Right, add, seen)
	case *ast.NotCondition:
		p.walkReads(cc.Inner, add, seen)
	case *ast.AsOfCondition:
		p.walkReads(cc.Inner, add, seen)
	case *ast.IsCondition:
		key := "define:" + cc.Name
		if def, ok := p.defines[cc.Name]; ok && !seen[key] {
			seen[key] = true
			for _, dc := range def.Conditions {
				p.walkReads(dc, add, seen)
			}
		}
	case *ast.PredicateCallCondition:
		key := "derive:" + cc.Name
		if d, ok := p.derives[cc.Name]; ok && !seen[key] {
			seen[key] = true
			for _, dc := range d.Selector.Conditions {
				p.walkReads(dc, add, seen)
			}
		}
	default:
		walkAttrRefs(c, add)
	}
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
