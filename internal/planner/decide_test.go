package planner

import "testing"

// TestPlanDecideDeterministic: the features/trained_on form reuses the
// classify_knn primitive but carries the choices and emit_distribution flag so
// the primitive attaches the full per-choice distribution.
func TestPlanDecideDeterministic(t *testing.T) {
	plan := planBlock(t, `
decide "kind" {
  for records where type == "email"
  choices ["ham", "spam"]
  features [attr "link_count", attr "caps_ratio"]
  trained_on records where labeled == true
  label_attr "kind"
  confidence >= 0.7
}`, "kind")

	var facts []*FactQuery
	for _, s := range plan.Steps {
		if fq, ok := s.(*FactQuery); ok {
			facts = append(facts, fq)
		}
	}
	if len(facts) != 2 {
		t.Fatalf("want 2 FactQuery steps (candidates + training), got %d", len(facts))
	}
	if !facts[1].Auxiliary || facts[1].Into != "training" {
		t.Errorf("training query: Auxiliary=%v Into=%q", facts[1].Auxiliary, facts[1].Into)
	}

	ml := findMLStep(plan, FuncClassifyKNN)
	if ml == nil {
		t.Fatal("expected MLComputation with classify_knn")
	}
	if ml.Params["emit_distribution"] != true {
		t.Errorf("emit_distribution param: got %v, want true", ml.Params["emit_distribution"])
	}
	choices, _ := ml.Params["choices"].([]string)
	if len(choices) != 2 || choices[0] != "ham" || choices[1] != "spam" {
		t.Errorf("choices param: got %v, want [ham spam]", ml.Params["choices"])
	}
	if conf, _ := ml.Params["confidence"].(float64); conf != 0.7 {
		t.Errorf("confidence param: got %v, want 0.7", ml.Params["confidence"])
	}
}

// TestPlanDecideModel: the ask/using-model form emits a decide_model
// GoComputation carrying the model handle, choices, and confidence bound.
func TestPlanDecideModel(t *testing.T) {
	plan := planBlock(t, `
decide "triage" {
  for records where folder == "Inbox"
  choices ["Legitimate", "Spam", "Phishing"]
  ask concat("Subject: ", attr "subject")
  using model "jev-small"
  confidence >= 0.9
}`, "triage")

	var gc *GoComputation
	for _, s := range plan.Steps {
		if g, ok := s.(*GoComputation); ok && g.Function == FuncDecideModel {
			gc = g
		}
	}
	if gc == nil {
		t.Fatal("expected a decide_model GoComputation")
	}
	if gc.Params["model"] != "jev-small" {
		t.Errorf("model param: got %v, want jev-small", gc.Params["model"])
	}
	if gc.Params["ask"] == nil {
		t.Error("ask param missing")
	}
	choices, _ := gc.Params["choices"].([]string)
	if len(choices) != 3 {
		t.Errorf("choices param: got %v, want 3 options", gc.Params["choices"])
	}
	if conf, _ := gc.Params["confidence"].(float64); conf != 0.9 {
		t.Errorf("confidence param: got %v, want 0.9", gc.Params["confidence"])
	}
}
