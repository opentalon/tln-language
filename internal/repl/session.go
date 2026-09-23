// Package repl implements `tln repl` — an interactive read-eval-print loop
// that lets users assert facts, define blocks inline, and evaluate them
// against an in-memory store. See issue #21.
//
// The REPL reuses the same lex/parse/validate/plan pipeline as `tln build`
// and the testrunner's in-memory evaluator. It does not introduce a new
// FactStore implementation; instead, it synthesises a TestBlock per eval so
// the testrunner's existing in-memory path runs unchanged.
package repl

import (
	"sort"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/pkg/tln"
)

// Session holds everything the REPL accumulates across one interactive
// session: user-defined blocks, asserted facts, and context variables.
//
// Facts are stored in the same shape the parser emits for `given { record …
// attr … }` test fixtures so the REPL can hand them straight to the
// testrunner without translation.
type Session struct {
	Blocks  []ast.Block
	Facts   []ast.TestDatum
	Context map[string]string

	// watch is the reactive runtime, non-nil once a program containing
	// `on` blocks is loaded (via :load or a preloaded file). Asserting a
	// fact then fires matching on-blocks and prints the result. See
	// watch.go.
	watch *tln.Session
}

// NewSession returns an empty session.
func NewSession() *Session {
	return &Session{Context: map[string]string{}}
}

// AddBlocks appends blocks parsed from inline input, replacing any prior
// block with the same name (so re-defining a rule inline overwrites the
// older copy — matches typical REPL expectations).
func (s *Session) AddBlocks(blocks []ast.Block) {
	for _, nb := range blocks {
		name := nb.BlockName()
		replaced := false
		for i, ob := range s.Blocks {
			if ob.BlockName() == name {
				s.Blocks[i] = nb
				replaced = true
				break
			}
		}
		if !replaced {
			s.Blocks = append(s.Blocks, nb)
		}
	}
}

// AddFact records a single record or attr assertion. Multiple assertions for
// the same record ID merge their fields (matching test-fixture semantics).
func (s *Session) AddFact(d ast.TestDatum) {
	for i, ex := range s.Facts {
		if ex.ID == d.ID && ex.Kind == d.Kind && sameAttrName(ex, d) {
			for k, v := range d.Fields {
				s.Facts[i].Fields[k] = v
			}
			return
		}
	}
	s.Facts = append(s.Facts, d)
}

// sameAttrName reports whether two attr-kind data target the same attribute.
// `record` data are matched purely by ID and merged across calls.
func sameAttrName(a, b ast.TestDatum) bool {
	if a.Kind != "attr" {
		return true
	}
	for k := range a.Fields {
		if _, ok := b.Fields[k]; ok {
			return true
		}
	}
	return false
}

// ClearAll drops both blocks and facts, and tears down any reactive
// watch session.
func (s *Session) ClearAll() {
	s.Blocks = nil
	s.Facts = nil
	s.Context = map[string]string{}
	s.stopWatch()
}

// ClearFacts drops facts only — keeps the compiled blocks.
func (s *Session) ClearFacts() {
	s.Facts = nil
}

// Program returns a snapshot *ast.Program containing the session's blocks
// (no test blocks). Used by validators and planners that operate on the
// whole program.
func (s *Session) Program() *ast.Program {
	return &ast.Program{Blocks: append([]ast.Block(nil), s.Blocks...)}
}

// BlockNames returns the names of all session blocks, sorted, for display.
func (s *Session) BlockNames() []string {
	out := make([]string, 0, len(s.Blocks))
	for _, b := range s.Blocks {
		out = append(out, b.BlockName())
	}
	sort.Strings(out)
	return out
}

// BlockByName looks up a session block by its declared name.
func (s *Session) BlockByName(name string) ast.Block {
	for _, b := range s.Blocks {
		if b.BlockName() == name {
			return b
		}
	}
	return nil
}

// blockKind names the keyword used to declare a block (for `:rules` output).
func blockKind(b ast.Block) string {
	switch b.(type) {
	case *ast.DetectBlock:
		return "detect"
	case *ast.RuleBlock:
		return "rule"
	case *ast.RecommendBlock:
		return "recommend"
	case *ast.CombineBlock:
		return "combine"
	case *ast.DefineBlock:
		return "define"
	case *ast.WorkflowBlock:
		return "workflow"
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
	case *ast.RelatedBlock:
		return "find related"
	}
	return "block"
}
