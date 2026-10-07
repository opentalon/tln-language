// Package actions resolves the `do` clauses of a rule into the action
// payload the engine hands back to its host. tln never executes an
// action: it decides which ones fire, resolves their arguments against the
// matched row, and returns them as data.
//
// The package is shared by the runtime (internal/executor) and the
// .tln.test runner (internal/testrunner) so that `did` / `did_not`
// assertions and what a host actually receives cannot drift apart.
package actions

import (
	"sort"
	"strings"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/defeasible"
	"github.com/opentalon/tln-language/internal/template"
)

// Fired is one `do` action resolved against one matched row.
type Fired struct {
	EntityID int
	Rule     string
	Verb     string
	Args     []any
}

// Row is one matched record as the firing code needs it.
//
// Attrs holds the row's `attr "x"` values under bare names — only what was
// asserted in the :attr/ namespace, so an attribute the row does not carry
// is absent from the map rather than present-and-empty. Ctx is the render
// context for string-literal arguments, which are templates.
type Row struct {
	ID    int
	Attrs map[string]any
	Ctx   template.RenderContext
}

// Fire resolves a rule's `do` clauses for every matched row: actions in
// source order per row, rows in the order given. A rule with no `do`
// clauses fires nothing.
//
// Arguments resolve per row: `attr "x"` reads the row's attribute, a string
// literal is rendered as a template so `{attr.x}` interpolates, and numbers
// and booleans pass through. An `attr` the row does not carry resolves to
// nil, which keeps a missing fact distinguishable from an empty string in
// the action payload even though the condition layer is two-valued.
func Fire(rule *ast.RuleBlock, rows []Row) []Fired {
	if rule == nil || len(rule.Do) == 0 {
		return nil
	}
	var out []Fired
	for _, row := range rows {
		for _, do := range rule.Do {
			fired := Fired{EntityID: row.ID, Rule: rule.Name, Verb: do.Verb}
			for _, arg := range do.Args {
				fired.Args = append(fired.Args, ResolveArg(arg, row))
			}
			out = append(out, fired)
		}
	}
	return out
}

// ResolveArg evaluates one `do` argument against a row.
func ResolveArg(e ast.Expr, row Row) any {
	switch v := e.(type) {
	case *ast.AttrExpr:
		if val, ok := row.Attrs[v.Name]; ok {
			return val
		}
		return nil
	case *ast.LiteralExpr:
		if s, ok := v.Value.(string); ok {
			return template.Render(ast.ParseTemplate(s), row.Ctx)
		}
		return v.Value
	case *ast.IdentExpr:
		return v.Name
	}
	return nil
}

// ReferencedAttrs lists the bare attribute names a rule's `do` clauses read
// — via `attr "x"` arguments and via {item.x} / {attr.x} / {x} refs inside
// string-literal arguments. The runtime uses it to fetch exactly the
// attributes it needs before firing.
func ReferencedAttrs(rule *ast.RuleBlock) []string {
	if rule == nil {
		return nil
	}
	set := map[string]bool{}
	add := func(name string) {
		if name != "" && name != "id" {
			set[name] = true
		}
	}
	for _, do := range rule.Do {
		for _, arg := range do.Args {
			switch v := arg.(type) {
			case *ast.AttrExpr:
				add(v.Name)
			case *ast.LiteralExpr:
				s, ok := v.Value.(string)
				if !ok || !strings.Contains(s, "{") {
					continue
				}
				for _, node := range ast.ParseTemplate(s).Nodes {
					ref, ok := node.(*ast.RefNode)
					if !ok {
						continue
					}
					add(templateRefAttr(ref.Path))
				}
			}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	return names
}

// templateRefAttr maps a template ref path to the bare attribute name to
// fetch: "item.name"→"name", "attr.km"→"km", "name"→"name". Paths scoped to
// context return "" so they're skipped.
func templateRefAttr(path string) string {
	if strings.HasPrefix(path, "context.") {
		return ""
	}
	for _, prefix := range []string{"attr.", "item."} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			return rest
		}
	}
	return path
}

// Resolve drops the actions of rules that lost defeasible resolution for
// the row they fired on. matched maps entity id → every rule that matched
// that entity, which is what defeat is defined over: a rule only defeats a
// rule that also matched the same target.
//
// Only rules linked by an `overrides` edge to another rule matching the
// same row are put through resolution. Two rules matching one row is the
// normal case and not a conflict — a ruleset where `label` and `assign`
// both match a PR must fire both, and a priority annotation is not a claim
// that the lower-priority rule should be silenced. An `overrides` clause is
// the author declaring the conflict; priority then decides who wins inside
// that declared group.
//
// Rules with no `do` clauses still participate — an `overrides` clause on a
// silent rule is how a policy suppresses another rule's actions.
func Resolve(fired []Fired, matched map[int][]*ast.RuleBlock) (kept []Fired, warnings []string) {
	kept = make([]Fired, 0, len(fired))
	winners := map[int]map[string]bool{}
	seenWarn := map[string]bool{}
	for id, rules := range matched {
		group := conflicting(rules)
		if len(group) < 2 {
			continue // nothing to resolve against
		}
		w, warns := defeasible.Resolve(group)
		names := make(map[string]bool, len(w))
		for _, r := range w {
			names[r.Name] = true
		}
		// Rules outside the conflict group are unaffected, so seed them
		// as winners rather than letting the filter drop them.
		for _, r := range rules {
			if !inGroup(group, r) {
				names[r.Name] = true
			}
		}
		winners[id] = names
		for _, warn := range warns {
			if seenWarn[warn] {
				continue
			}
			seenWarn[warn] = true
			warnings = append(warnings, warn)
		}
	}
	for _, f := range fired {
		if names, ok := winners[f.EntityID]; ok && !names[f.Rule] {
			continue
		}
		kept = append(kept, f)
	}
	// matched is a map, so warning discovery order isn't stable; sort so two
	// runs over the same facts produce the same list.
	sort.Strings(warnings)
	return kept, warnings
}

// conflicting returns the rules that an `overrides` edge connects to
// another rule in the same matched set — either end of the edge. Rules with
// no edge to anything here aren't in conflict with anyone and are left out
// of resolution entirely.
func conflicting(rules []*ast.RuleBlock) []*ast.RuleBlock {
	byName := make(map[string]*ast.RuleBlock, len(rules))
	for _, r := range rules {
		byName[r.Name] = r
	}
	in := map[string]bool{}
	for _, r := range rules {
		for _, target := range r.Overrides {
			if _, ok := byName[target]; !ok {
				continue // overriding a rule that didn't match is a no-op
			}
			in[r.Name] = true
			in[target] = true
		}
	}
	out := make([]*ast.RuleBlock, 0, len(in))
	for _, r := range rules {
		if in[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

func inGroup(group []*ast.RuleBlock, r *ast.RuleBlock) bool {
	for _, g := range group {
		if g.Name == r.Name {
			return true
		}
	}
	return false
}
