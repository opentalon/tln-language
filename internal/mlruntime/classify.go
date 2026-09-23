package mlruntime

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// KNNClassifier implements the classify_knn primitive: each candidate is
// assigned the majority class of its K nearest labeled training examples,
// with confidence = the winning class's vote fraction.
//
// Features are read by bare attribute name from Input.Entities (candidates)
// and Input.Training (labeled examples). Each feature column is z-normalised
// across the union of both sets so a large-magnitude attribute (e.g. "hours"
// in the thousands) doesn't dominate a small one (e.g. a 0–10 score);
// distance is euclidean over the normalised vectors. Ties in the vote break
// to the lexically smallest label; ties in distance keep training-row order —
// both deterministic, per the explainability contract (ADR-0001).
//
// Params (set by the planner from the classify block):
//
//	feature_names []string — bare attribute names forming the feature vector.
//	k             int       — neighbours to poll (default 5).
//	label_attr    string    — informational; the runtime has already read it
//	                          into each TrainingRow.Label.
//	confidence    float64   — optional; the runtime drops predictions below
//	                          this vote fraction. The primitive still emits
//	                          every prediction, with the fraction recorded in
//	                          the Explanation, so audit output stays complete.
//
// Result.Value is the predicted class string. The Explanation records the
// confidence and the K neighbours (id, label, distance) that voted, so
// `tln trace` can show exactly who outvoted whom.
type KNNClassifier struct{}

// NewKNNClassifier constructs the primitive.
func NewKNNClassifier() *KNNClassifier { return &KNNClassifier{} }

// Name returns the planner function constant this primitive serves.
func (*KNNClassifier) Name() string { return "classify_knn" }

// Compute assigns each candidate a class by majority vote of its K nearest
// labeled neighbours. See the type-level comment for the parameter contract.
func (c *KNNClassifier) Compute(_ context.Context, in Input) ([]Result, error) {
	features := readStringSlice(in.Params, "feature_names")
	if len(features) == 0 {
		return nil, fmt.Errorf("classify_knn: at least one feature is required")
	}
	if len(in.Training) == 0 {
		// No labeled examples reached the primitive (e.g. the executor path,
		// which doesn't yet materialise a training set — see ADR-0006). Emit
		// no predictions rather than erroring, so a `tln run` over a
		// classify block degrades to "unclassified" instead of aborting.
		return nil, nil
	}
	k := readIntOr(in.Params, "k", 5)
	if k <= 0 {
		k = 5
	}
	if k > len(in.Training) {
		k = len(in.Training)
	}

	// Candidate vectors, ordered by Rows so the output ordering is stable.
	candIDs := make([]int, 0, len(in.Rows))
	candVecs := make([][]float64, 0, len(in.Rows))
	for _, row := range in.Rows {
		if len(row) == 0 {
			continue
		}
		id, ok := toInt(row[0])
		if !ok {
			continue
		}
		candIDs = append(candIDs, id)
		candVecs = append(candVecs, featureVec(in.Entities[id], features))
	}

	// Training vectors, parallel to in.Training.
	trainVecs := make([][]float64, len(in.Training))
	for i, t := range in.Training {
		trainVecs[i] = featureVec(t.Attrs, features)
	}

	// Per-feature z-normalisation across candidates ∪ training.
	mean, std := columnStats(features, candVecs, trainVecs)
	for i := range candVecs {
		normalizeVec(candVecs[i], mean, std)
	}
	for i := range trainVecs {
		normalizeVec(trainVecs[i], mean, std)
	}

	// `decide` blocks ask for the full per-choice distribution (votes[c]/k),
	// not just the winning class. choices declares the categories the
	// distribution ranges over so unseen choices surface as explicit zeros.
	emitDist := readBoolOr(in.Params, "emit_distribution", false)
	choices := readStringSlice(in.Params, "choices")

	results := make([]Result, 0, len(candIDs))
	for i, id := range candIDs {
		results = append(results, classifyOne(id, candVecs[i], trainVecs, in.Training, k, emitDist, choices))
	}
	return results, nil
}

// classifyOne runs the vote for a single candidate. When emitDist is set it
// also attaches a calibrated probability distribution over choices (votes/k,
// renormalised so it sums to 1 over exactly the declared choices).
func classifyOne(id int, cand []float64, trainVecs [][]float64, training []TrainingRow, k int, emitDist bool, choices []string) Result {
	type nbr struct {
		idx  int
		dist float64
	}
	nbrs := make([]nbr, len(trainVecs))
	for i, tv := range trainVecs {
		nbrs[i] = nbr{idx: i, dist: euclidean(cand, tv)}
	}
	// Stable sort keeps training-row order for equal distances (deterministic).
	sort.SliceStable(nbrs, func(a, b int) bool { return nbrs[a].dist < nbrs[b].dist })

	votes := map[string]int{}
	neighbours := make([]map[string]any, 0, k)
	for i := 0; i < k; i++ {
		t := training[nbrs[i].idx]
		votes[t.Label]++
		neighbours = append(neighbours, map[string]any{
			"id": t.ID, "label": t.Label, "distance": nbrs[i].dist,
		})
	}
	winner, winVotes := majorityVote(votes)
	confidence := float64(winVotes) / float64(k)
	var dist map[string]float64
	if emitDist {
		// decide mode: the decision ranges over exactly the declared choices.
		// Derive the distribution, the chosen option, and the confidence from
		// the votes restricted to those choices, so the winner is always a
		// declared choice and confidence == probabilities[chosen].
		dist, winner, confidence = distribution(votes, choices)
	}
	inputs := map[string]any{
		"class":       winner,
		"k":           k,
		"k_neighbors": neighbours,
	}
	if emitDist {
		inputs["probabilities"] = dist
		inputs["chosen"] = winner
	}
	return Result{
		EntityID: id,
		Value:    winner,
		Explanation: Explanation{
			Primitive:  "classify_knn",
			EntityID:   id,
			Confidence: confidence,
			Inputs:     inputs,
		},
	}
}

// distribution builds a calibrated probability distribution over the declared
// choices from the k-neighbour vote counts, and returns it with the argmax
// choice and that choice's probability. Every declared choice is present
// (unseen ones are explicit 0.0); votes for labels outside `choices` (stray
// training classes) are dropped and the remaining mass is renormalised so the
// distribution sums to 1 over exactly `choices`. The winner is the argmax over
// declared choices (lexically-smallest tie-break, matching majorityVote), so
// the chosen option is always a declared choice and prob == probs[chosen].
// With no votes on any declared choice (or an empty choice set) the
// lexically-smallest choice is chosen with probability 0.
func distribution(votes map[string]int, choices []string) (map[string]float64, string, float64) {
	// Dedupe the declared choices so a repeated choice string can't
	// double-count its votes into the renormalisation denominator.
	probs := make(map[string]float64, len(choices))
	uniq := make([]string, 0, len(choices))
	for _, c := range choices {
		if _, seen := probs[c]; seen {
			continue
		}
		probs[c] = 0
		uniq = append(uniq, c)
	}
	sorted := append([]string(nil), uniq...)
	sort.Strings(sorted)
	kept := 0
	for _, c := range uniq {
		kept += votes[c]
	}
	if kept == 0 {
		if len(sorted) > 0 {
			return probs, sorted[0], 0
		}
		return probs, "", 0
	}
	chosen, best := "", -1.0
	for _, c := range sorted {
		p := float64(votes[c]) / float64(kept)
		probs[c] = p
		if p > best {
			chosen, best = c, p
		}
	}
	return probs, chosen, best
}

// featureVec projects an entity's attribute map onto the feature axes.
// Missing or non-numeric attributes contribute 0, matching the cosine
// primitive's convention so the vector length stays well-defined.
func featureVec(attrs map[string]any, features []string) []float64 {
	v := make([]float64, len(features))
	for i, name := range features {
		if val, ok := attrs[name]; ok {
			if f, ok := toFloat(val); ok {
				v[i] = f
			}
		}
	}
	return v
}

// columnStats returns per-feature mean and (population) standard deviation
// across the union of candidate and training vectors. A zero-variance column
// gets std=1 so normalisation leaves it at 0 instead of dividing by zero.
func columnStats(features []string, groups ...[][]float64) (mean, std []float64) {
	n := len(features)
	mean = make([]float64, n)
	std = make([]float64, n)
	count := 0
	for _, g := range groups {
		for _, v := range g {
			for i := 0; i < n && i < len(v); i++ {
				mean[i] += v[i]
			}
			count++
		}
	}
	if count == 0 {
		for i := range std {
			std[i] = 1
		}
		return mean, std
	}
	for i := range mean {
		mean[i] /= float64(count)
	}
	for _, g := range groups {
		for _, v := range g {
			for i := 0; i < n && i < len(v); i++ {
				d := v[i] - mean[i]
				std[i] += d * d
			}
		}
	}
	for i := range std {
		std[i] = sqrtOr1(std[i] / float64(count))
	}
	return mean, std
}

func normalizeVec(v, mean, std []float64) {
	for i := range v {
		if i < len(mean) {
			v[i] = (v[i] - mean[i]) / std[i]
		}
	}
}

// majorityVote returns the winning label and its count. Ties break to the
// lexically smallest label so the result is deterministic regardless of map
// iteration order.
func majorityVote(votes map[string]int) (string, int) {
	labels := make([]string, 0, len(votes))
	for l := range votes {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	best, bestN := "", 0
	for _, l := range labels {
		if votes[l] > bestN {
			best, bestN = l, votes[l]
		}
	}
	return best, bestN
}

// sqrtOr1 returns √x, or 1 for a non-positive variance so a zero-variance
// feature column normalises to 0 rather than dividing by zero.
func sqrtOr1(x float64) float64 {
	if x <= 0 {
		return 1
	}
	return math.Sqrt(x)
}
