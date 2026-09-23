package executor

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/factstore"
	"github.com/opentalon/tln-language/internal/planner"
)

// decideModelStep builds the GoComputation the planner emits for a model-mode
// `decide` block.
func decideModelStep(model string, choices []string, ask ast.Expr, conf *float64) *planner.GoComputation {
	params := map[string]any{
		"ask":        ask,
		"model":      model,
		"choices":    choices,
		"block_name": "triage",
	}
	if conf != nil {
		params["confidence"] = *conf
	}
	return &planner.GoComputation{
		Function: planner.FuncDecideModel,
		Input:    "candidates",
		Params:   params,
		Into:     "decisions",
	}
}

func decideStore(t *testing.T, id int, subject string) *factstore.MemoryStore {
	t.Helper()
	store := factstore.NewMemoryStore()
	must(t, store.Assert(context.Background(), []factstore.Fact{
		{RecordID: strconv.Itoa(id), Attribute: ":attr/subject", Value: subject},
	}))
	return store
}

// TestDecideModelViaToolResolver: a model-mode decide renders the `ask` state
// from the entity's attributes, calls the injected ToolResolver as
// (server=model, tool="decide", args={state, choices}), and records the
// returned {chosen, confidence, probabilities}.
func TestDecideModelViaToolResolver(t *testing.T) {
	store := decideStore(t, 1, "You won a prize")
	mock := &mockMCP{handler: func(_, _ string, _ map[string]any) (any, error) {
		return map[string]any{
			"chosen":     "Spam",
			"confidence": 0.92,
			"probabilities": map[string]any{
				"Legitimate": 0.05, "Spam": 0.92, "Phishing": 0.03,
			},
		}, nil
	}}
	e := &Executor{Client: store, Tools: mock}
	gc := decideModelStep("jev-small",
		[]string{"Legitimate", "Spam", "Phishing"},
		&ast.AttrExpr{Name: "subject"}, nil)
	vars := map[string]any{"candidates": [][]any{{float64(1)}}}

	out, err := e.execDecideModel(context.Background(), gc, vars)
	if err != nil {
		t.Fatalf("execDecideModel: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("want 1 resolver call, got %d", len(mock.calls))
	}
	c := mock.calls[0]
	if c.Server != "jev-small" || c.Tool != "decide" {
		t.Errorf("resolver got (%q, %q), want (jev-small, decide)", c.Server, c.Tool)
	}
	if c.Args["state"] != "You won a prize" {
		t.Errorf("state = %#v, want rendered from attr subject", c.Args["state"])
	}
	if choices, ok := c.Args["choices"].([]string); !ok || len(choices) != 3 {
		t.Errorf("choices = %#v, want the 3 declared options", c.Args["choices"])
	}

	results := decideResults(t, out)
	if len(results) != 1 {
		t.Fatalf("want 1 decision, got %d", len(results))
	}
	if results[0]["chosen"] != "Spam" {
		t.Errorf("chosen = %#v, want Spam", results[0]["chosen"])
	}
}

// TestDecideModelConfidenceGate: a `confidence >=` bound drops decisions the
// model isn't sure enough about.
func TestDecideModelConfidenceGate(t *testing.T) {
	store := decideStore(t, 1, "meeting notes")
	mock := &mockMCP{handler: func(_, _ string, _ map[string]any) (any, error) {
		return map[string]any{"chosen": "Legitimate", "confidence": 0.4}, nil
	}}
	e := &Executor{Client: store, Tools: mock}
	bound := 0.9
	gc := decideModelStep("jev-small", []string{"Legitimate", "Spam"},
		&ast.AttrExpr{Name: "subject"}, &bound)
	vars := map[string]any{"candidates": [][]any{{float64(1)}}}

	out, err := e.execDecideModel(context.Background(), gc, vars)
	if err != nil {
		t.Fatalf("execDecideModel: %v", err)
	}
	if got := len(decideResults(t, out)); got != 0 {
		t.Errorf("low-confidence decision survived the gate: %d results", got)
	}
}

// TestDecideModelStub: with no ToolResolver injected the step stubs out
// instead of erroring, so a plan referencing a model still runs.
func TestDecideModelStub(t *testing.T) {
	store := decideStore(t, 1, "hi")
	e := &Executor{Client: store} // no Tools
	gc := decideModelStep("jev-small", []string{"a", "b"},
		&ast.AttrExpr{Name: "subject"}, nil)
	vars := map[string]any{"candidates": [][]any{{float64(1)}}}

	got, err := e.execDecideModel(context.Background(), gc, vars)
	if err != nil {
		t.Fatalf("execDecideModel: %v", err)
	}
	out, _ := got.(map[string]any)
	if out["status"] != "stub" {
		t.Errorf("status = %#v, want stub when no resolver", out["status"])
	}
}

// TestDecideModelResolverError surfaces a resolver failure rather than
// swallowing it.
func TestDecideModelResolverError(t *testing.T) {
	store := decideStore(t, 1, "hi")
	mock := &mockMCP{handler: func(_, _ string, _ map[string]any) (any, error) {
		return nil, errors.New("model unreachable")
	}}
	e := &Executor{Client: store, Tools: mock}
	gc := decideModelStep("jev-small", []string{"a", "b"},
		&ast.AttrExpr{Name: "subject"}, nil)
	vars := map[string]any{"candidates": [][]any{{float64(1)}}}

	if _, err := e.execDecideModel(context.Background(), gc, vars); err == nil {
		t.Fatal("expected an error when the resolver fails")
	}
}

func decideResults(t *testing.T, result any) []map[string]any {
	t.Helper()
	out, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("decide result missing or wrong type: %#v", result)
	}
	raw, _ := out["results"].([]any)
	res := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			res = append(res, m)
		}
	}
	return res
}
