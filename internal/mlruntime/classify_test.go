package mlruntime

import (
	"context"
	"testing"
)

// train builds a labeled example with two numeric features.
func train(id int, f1, f2 float64, label string) TrainingRow {
	return TrainingRow{ID: id, Label: label, Attrs: map[string]any{"f1": f1, "f2": f2}}
}

// classifyInput assembles an Input for the kNN primitive: candidate ids in
// Rows, their features in Entities, labeled examples in Training.
func classifyInput(cands map[int][2]float64, training []TrainingRow, k int) Input {
	rows := make([][]any, 0, len(cands))
	ents := map[int]map[string]any{}
	for id, xy := range cands {
		rows = append(rows, []any{id})
		ents[id] = map[string]any{"f1": xy[0], "f2": xy[1]}
	}
	return Input{
		Rows:     rows,
		Entities: ents,
		Training: training,
		Params:   map[string]any{"feature_names": []string{"f1", "f2"}, "k": k},
	}
}

func classOf(t *testing.T, results []Result, id int) (string, float64) {
	t.Helper()
	for _, r := range results {
		if r.EntityID == id {
			cls, _ := r.Value.(string)
			return cls, r.Explanation.Confidence
		}
	}
	t.Fatalf("no result for entity %d", id)
	return "", 0
}

// TestClassifyTwoClusters is the issue's golden case: two clearly separated
// labeled groups; a candidate sitting on top of each group must take that
// group's label with full confidence at k = cluster size.
func TestClassifyTwoClusters(t *testing.T) {
	training := []TrainingRow{
		train(1, 10, 10, "hot"), train(2, 11, 9, "hot"), train(3, 9, 11, "hot"),
		train(4, 0, 0, "cold"), train(5, 1, 1, "cold"), train(6, -1, 0, "cold"),
	}
	in := classifyInput(map[int][2]float64{
		100: {10, 10}, // squarely in the hot cluster
		101: {0, 0},   // squarely in the cold cluster
	}, training, 3)

	results, err := NewKNNClassifier().Compute(context.Background(), in)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if cls, conf := classOf(t, results, 100); cls != "hot" || conf != 1.0 {
		t.Errorf("entity 100: got %q conf %v, want hot 1.0", cls, conf)
	}
	if cls, conf := classOf(t, results, 101); cls != "cold" || conf != 1.0 {
		t.Errorf("entity 101: got %q conf %v, want cold 1.0", cls, conf)
	}
}

// TestClassifyBorderlineConfidence — a candidate midway between the clusters
// still gets a class, but with a split (< 1.0) vote, which is exactly the
// signal a `confidence >= N` bound uses to drop uncertain predictions.
func TestClassifyBorderlineConfidence(t *testing.T) {
	training := []TrainingRow{
		train(1, 10, 10, "hot"), train(2, 10, 10, "hot"),
		train(3, 0, 0, "cold"), train(4, 0, 0, "cold"), train(5, 0, 0, "cold"),
	}
	// Candidate closer to cold but within reach of hot; k=5 polls everyone,
	// so the 3 cold beat the 2 hot: "cold" at 0.6.
	in := classifyInput(map[int][2]float64{200: {4, 4}}, training, 5)
	results, err := NewKNNClassifier().Compute(context.Background(), in)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	cls, conf := classOf(t, results, 200)
	if cls != "cold" {
		t.Errorf("entity 200: got class %q, want cold", cls)
	}
	if conf != 0.6 {
		t.Errorf("entity 200: got confidence %v, want 0.6 (3 of 5)", conf)
	}
}

// TestClassifyFeatureScaling proves the per-feature normalisation: without it,
// f2 (values in the thousands) would swamp f1 and every candidate would be
// classified by f2 alone. Here the true signal is in f1; f2 is constant noise
// at a huge magnitude.
func TestClassifyFeatureScaling(t *testing.T) {
	training := []TrainingRow{
		train(1, 10, 5000, "high"), train(2, 11, 5000, "high"),
		train(3, 0, 5000, "low"), train(4, 1, 5000, "low"),
	}
	in := classifyInput(map[int][2]float64{300: {10, 5000}}, training, 2)
	results, err := NewKNNClassifier().Compute(context.Background(), in)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if cls, _ := classOf(t, results, 300); cls != "high" {
		t.Errorf("entity 300: got %q, want high (f1 signal must survive f2's scale)", cls)
	}
}

// TestClassifyTieBreaksLexically — an even split resolves to the lexically
// smaller label, deterministically, regardless of map iteration order.
func TestClassifyTieBreaks(t *testing.T) {
	training := []TrainingRow{
		train(1, 0, 0, "zebra"), train(2, 0, 0, "apple"),
	}
	in := classifyInput(map[int][2]float64{400: {0, 0}}, training, 2)
	results, err := NewKNNClassifier().Compute(context.Background(), in)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if cls, _ := classOf(t, results, 400); cls != "apple" {
		t.Errorf("tie: got %q, want apple (lexical tiebreak)", cls)
	}
}

// TestClassifyEmptyTraining degrades to no predictions (not an error) so a
// `tln run` over a classify block whose training set is empty doesn't abort.
func TestClassifyEmptyTraining(t *testing.T) {
	in := classifyInput(map[int][2]float64{500: {1, 1}}, nil, 3)
	results, err := NewKNNClassifier().Compute(context.Background(), in)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("empty training: got %d results, want 0", len(results))
	}
}

// TestClassifyNoFeatures is a misconfiguration — the primitive rejects it.
func TestClassifyNoFeatures(t *testing.T) {
	_, err := NewKNNClassifier().Compute(context.Background(), Input{
		Rows:     [][]any{{1}},
		Training: []TrainingRow{train(1, 1, 1, "x")},
		Params:   map[string]any{},
	})
	if err == nil {
		t.Fatal("expected an error when no features are configured")
	}
}

// distOf pulls the emitted probability distribution + chosen option for an
// entity out of a Result's explanation. Present only when the block set
// emit_distribution (i.e. a `decide` block).
func distOf(t *testing.T, results []Result, id int) (map[string]float64, string) {
	t.Helper()
	for _, r := range results {
		if r.EntityID == id {
			probs, _ := r.Explanation.Inputs["probabilities"].(map[string]float64)
			chosen, _ := r.Explanation.Inputs["chosen"].(string)
			return probs, chosen
		}
	}
	t.Fatalf("no result for entity %d", id)
	return nil, ""
}

// TestDecideDistribution is the `decide` deterministic mode: emit_distribution
// turns the kNN vote into a calibrated distribution over the declared choices.
// A candidate with a 2-hot / 1-cold neighbourhood must report {hot: 2/3, cold:
// 1/3}, chosen "hot", and confidence == probabilities[chosen]. A choice nobody
// votes for is present as an explicit 0.
func TestDecideDistribution(t *testing.T) {
	training := []TrainingRow{
		train(1, 10, 10, "hot"), train(2, 11, 9, "hot"),
		train(3, 0, 0, "cold"),
	}
	in := classifyInput(map[int][2]float64{100: {10, 10}}, training, 3)
	in.Params["emit_distribution"] = true
	in.Params["choices"] = []string{"hot", "cold", "warm"} // warm is never voted

	results, err := NewKNNClassifier().Compute(context.Background(), in)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	probs, chosen := distOf(t, results, 100)
	if chosen != "hot" {
		t.Errorf("chosen = %q, want hot", chosen)
	}
	if !almostEq(probs["hot"], 2.0/3.0) || !almostEq(probs["cold"], 1.0/3.0) {
		t.Errorf("distribution = %v, want hot≈0.667 cold≈0.333", probs)
	}
	if got, ok := probs["warm"]; !ok || got != 0 {
		t.Errorf("unseen choice 'warm' = %v (present=%v), want explicit 0", got, ok)
	}
	// sums to 1 over exactly the declared choices.
	sum := probs["hot"] + probs["cold"] + probs["warm"]
	if !almostEq(sum, 1) {
		t.Errorf("distribution sum = %v, want 1", sum)
	}
	// confidence couples to the chosen option's probability.
	_, conf := classOf(t, results, 100)
	if !almostEq(conf, probs[chosen]) {
		t.Errorf("confidence %v != probabilities[chosen] %v", conf, probs[chosen])
	}
}

// TestDecideStrayLabelRenormalised: a training row whose label is outside the
// declared choices (a stray class) must be dropped from the distribution, and
// the remaining mass renormalised so it still sums to 1 over the choices.
func TestDecideStrayLabelRenormalised(t *testing.T) {
	training := []TrainingRow{
		train(1, 10, 10, "hot"), train(2, 11, 9, "hot"),
		train(3, 10, 10, "unknown"), // stray: not a declared choice
	}
	in := classifyInput(map[int][2]float64{100: {10, 10}}, training, 3)
	in.Params["emit_distribution"] = true
	in.Params["choices"] = []string{"hot", "cold"}

	results, err := NewKNNClassifier().Compute(context.Background(), in)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	probs, chosen := distOf(t, results, 100)
	if chosen != "hot" {
		t.Errorf("chosen = %q, want hot", chosen)
	}
	if _, ok := probs["unknown"]; ok {
		t.Errorf("stray label 'unknown' leaked into distribution: %v", probs)
	}
	// hot got 2 of the 2 declared-choice votes → renormalised to 1.0.
	if !almostEq(probs["hot"], 1) || !almostEq(probs["cold"], 0) {
		t.Errorf("distribution = %v, want hot=1 cold=0 after renormalisation", probs)
	}
}

// TestDecideDistributionDeterminism: the same input yields byte-identical
// distributions across runs — decide's deterministic mode inherits the kNN
// reproducibility contract (ADR-0001).
func TestDecideDistributionDeterminism(t *testing.T) {
	build := func() (map[string]float64, string) {
		training := []TrainingRow{
			train(1, 10, 10, "a"), train(2, 0, 0, "b"), train(3, 10, 10, "a"),
		}
		in := classifyInput(map[int][2]float64{100: {9, 9}}, training, 3)
		in.Params["emit_distribution"] = true
		in.Params["choices"] = []string{"a", "b"}
		results, err := NewKNNClassifier().Compute(context.Background(), in)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		return distOf(t, results, 100)
	}
	p1, c1 := build()
	p2, c2 := build()
	if c1 != c2 || p1["a"] != p2["a"] || p1["b"] != p2["b"] {
		t.Errorf("non-deterministic: run1 {%v %q} run2 {%v %q}", p1, c1, p2, c2)
	}
}

// TestDecideDuplicateChoicesDeduped: a repeated choice string must not
// double-count into the renormalisation denominator (defensive — the validator
// also rejects duplicates). votes hot=2, cold=1, k=3 with choices
// ["hot","hot","cold"] must still sum to 1, not 0.6.
func TestDecideDuplicateChoicesDeduped(t *testing.T) {
	training := []TrainingRow{
		train(1, 10, 10, "hot"), train(2, 11, 9, "hot"),
		train(3, 0, 0, "cold"),
	}
	in := classifyInput(map[int][2]float64{100: {10, 10}}, training, 3)
	in.Params["emit_distribution"] = true
	in.Params["choices"] = []string{"hot", "hot", "cold"}

	results, err := NewKNNClassifier().Compute(context.Background(), in)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	probs, _ := distOf(t, results, 100)
	sum := probs["hot"] + probs["cold"]
	if !almostEq(sum, 1) {
		t.Errorf("distribution sum = %v (probs %v), want 1 after dedupe", sum, probs)
	}
	if !almostEq(probs["hot"], 2.0/3.0) {
		t.Errorf("probs[hot] = %v, want 0.667", probs["hot"])
	}
}

func almostEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
