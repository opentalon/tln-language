package parser

import (
	"testing"

	"github.com/opentalon/tln-language/internal/ast"
)

// TestParseDecideModelMode parses a model-mode decide block and checks the
// choices, ask expression, and using-model handle land on the AST.
func TestParseDecideModelMode(t *testing.T) {
	prog := mustParse(t, `
decide "email_kind" {
  for records where folder == "Inbox"
  choices ["Legitimate", "Spam", "Phishing"]
  ask concat("Subject: ", attr "subject")
  using model "jev-small"
  confidence >= 0.9
}`)
	b := block[*ast.DecideBlock](t, prog, 0)
	if b.Name != "email_kind" {
		t.Errorf("name = %q", b.Name)
	}
	if len(b.Choices) != 3 {
		t.Errorf("choices = %d, want 3", len(b.Choices))
	}
	if b.Ask == nil {
		t.Error("ask expression not parsed")
	}
	if b.UsingModel != "jev-small" {
		t.Errorf("using model = %q, want jev-small", b.UsingModel)
	}
	if b.Mode() != "model" {
		t.Errorf("mode = %q, want model", b.Mode())
	}
	if b.Confidence == nil || *b.Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9", b.Confidence)
	}
}

// TestParseDecideDeterministicMode parses the features/trained_on form and is
// order-independent (clauses may appear in any order).
func TestParseDecideDeterministicMode(t *testing.T) {
	prog := mustParse(t, `
decide "kind" {
  for records where folder == "Inbox"
  features [ attr "link_count", attr "caps_ratio" ]
  trained_on records where labeled == true
  choices ["ham", "spam"]
  label_attr "kind"
}`)
	b := block[*ast.DecideBlock](t, prog, 0)
	if len(b.Features) != 2 {
		t.Errorf("features = %d, want 2", len(b.Features))
	}
	if b.TrainedOn == nil {
		t.Error("trained_on not parsed")
	}
	if b.LabelAttr != "kind" {
		t.Errorf("label_attr = %q, want kind", b.LabelAttr)
	}
	if b.Mode() != "deterministic" {
		t.Errorf("mode = %q, want deterministic", b.Mode())
	}
}
