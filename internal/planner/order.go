package planner

import (
	"sort"

	"github.com/opentalon/tln-language/internal/actions"
	"github.com/opentalon/tln-language/internal/ast"
)

// ExecutionOrder returns the plan names in the order RunAll runs them: a
// block runs after every other block that writes an attribute it reads, so
// an enrich gated on another enrich's result (a typed decision deciding
// whether to run a review) and a rule on either see the facts they depend
// on in the same run. Blocks with no dependency between them run in name
// order. Blocks on a dependency cycle can't be ordered; they are returned
// in name order after the rest, and also listed in cycle.
func ExecutionOrder(plans map[string]*QueryPlan) (order, cycle []string) {
	names := make([]string, 0, len(plans))
	for name := range plans {
		names = append(names, name)
	}
	sort.Strings(names)

	writers := map[string][]string{} // attr → blocks that write it
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

	var ready []string
	for _, name := range names {
		if inDegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	done := make(map[string]bool, len(names))
	for len(ready) > 0 {
		sort.Strings(ready)
		name := ready[0]
		ready = ready[1:]
		done[name] = true
		order = append(order, name)
		for _, d := range dependents[name] {
			inDegree[d]--
			if inDegree[d] == 0 {
				ready = append(ready, d)
			}
		}
	}
	for _, name := range names {
		if !done[name] {
			cycle = append(cycle, name)
		}
	}
	return append(order, cycle...), cycle
}

// enrichReads lists the attributes an enrich block reads: its selector and
// the expressions it passes to the tool.
func (p *planner) enrichReads(b *ast.EnrichBlock) []string {
	var out []string
	add := func(n string) { out = append(out, n) }
	for _, c := range b.Selector.Conditions {
		p.walkReads(c, add, map[string]bool{})
	}
	if b.Call != nil {
		for _, e := range b.Call.Args {
			walkExprAttrs(e, add)
		}
	}
	return dedupe(out)
}

// ruleReads lists the attributes a rule reads: its selector, its when
// condition and its action clauses' templates.
func (p *planner) ruleReads(b *ast.RuleBlock) []string {
	var out []string
	add := func(n string) { out = append(out, n) }
	if b.Selector != nil {
		for _, c := range b.Selector.Conditions {
			p.walkReads(c, add, map[string]bool{})
		}
	}
	p.walkReads(b.When, add, map[string]bool{})
	out = append(out, actions.ReferencedAttrs(b)...)
	return dedupe(out)
}

// walkReads is walkAttrRefs that also follows what the planner inlines: an
// `is "name"` define and a derived predicate `name(v)` read whatever their
// bodies read. seen guards against a define/derive cycle (the validator
// reports those).
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
