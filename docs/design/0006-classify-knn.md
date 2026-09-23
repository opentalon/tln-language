# ADR-0006: `classify_knn` Primitive + Supervised-Block Plumbing

## Status

Proposed — implements [opentalon/tln-language#70](https://github.com/opentalon/tln-language/issues/70), gates parent [#11](https://github.com/opentalon/tln-language/issues/11).

## Context

The `classify` block parses, plans, and routes to an `MLComputation` step,
but the runtime is a no-op: both dispatch paths fall through when the
registry has no primitive (`internal/executor/executor.go:707`,
`internal/testrunner/testrunner.go:897`). Issue #70 asks for the kNN
primitive that fills that gap.

Tracing the block end-to-end against the current tree (`d37ed59`) shows
the issue's "what's already in place" section is **stale on the one point
that makes this hard**, and omits three real gaps. This ADR records the
actual state and the design decisions the build depends on — chiefly *how a
supervised block names its training set and label column*, which no clause
expresses today.

### Verified state

**In place ✅**

- `classify "N" { features [...] confidence >= x label ... priority ... }`
  parses — `parseClassifyBlock` (`internal/parser/parser.go:1233`).
- `planClassifyBlock` emits
  `MLComputation{Function: "classify_knn", Params: {features, feature_names}}`
  into `"classifications"` (`internal/planner/planner.go:731`).
- `FuncClassifyKNN = "classify_knn"` is a registered plan constant
  (`internal/planner/planner.go:23`, listed in the ML-function set at `:241`).
- `mlruntime.Input.Entities` carries the per-candidate attribute map
  (`internal/mlruntime/explanation.go:53`). `CosineSimilarity`
  (`internal/mlruntime/similar.go`) is a direct template — same cosine
  math, same `Entities`-self-serve feature-vector pattern.

**Missing / contradicted ❌**

1. **`trained_on` does not exist for `classify`.** The issue claims it is
   "passed through as an unresolved `*ast.TrainedOnClause` in Params."
   It is not: `ast.ClassifyBlock` (`internal/ast/ast.go:204`) has **no
   `TrainedOn` field**, `parseClassifyBlock` has **no `trained_on` case**
   (it would error), and `planClassifyBlock` puts **only**
   `features`/`feature_names` in Params. `TrainedOn` exists for `predict`
   (`PredictBlock`, `PredictClause`), which is the pattern to copy — but it
   has never been extended to `classify`.

2. **No way to name the label column.** kNN votes on `t.label`. No clause
   tells the block *which attribute* holds a training row's class. This is
   unspecified in the issue and blocks items 1–4 below.

3. **Training-data fetch is misattributed to the planner.** The planner
   emits *static* plans; it never executes queries. Materialising the
   training set requires the **runtime** to run a second FactQuery and
   inject rows — and `mlruntime.Input` has **no channel** for a second
   entity set (Rows/Schema/Params/Entities all describe the *candidate*
   set).

4. **The executor path doesn't pass `Entities` at all.**
   `execMLComputation` (`internal/executor/executor.go:704`) builds
   `Input{Rows, Schema, Params}` with no `Entities`; only the testrunner's
   `narrowByML` populates it (`testrunner.go:949`). Multi-attribute
   primitives therefore only function under `tln test` today. kNN
   inherits this.

5. **Class→label and confidence-filter are unwired.** A kNN result is a
   class **string** (non-bool). `narrowByML` treats non-bool `Value` as
   "information, not filtering" and keeps every row
   (`testrunner.go:968-975`). So `confidence >= N` won't drop
   low-confidence predictions, and `RenderContext` (`internal/template/
   render.go:37`) has a `Calc` channel for calculate-vars but **no channel
   for an ML-predicted value**, so `label "{class}"` can't resolve.

## Decision

Ship kNN as a **supervised-block vertical slice**, in the order below.
Each layer is independently testable.

### 1. Grammar: `trained_on` + label column

**Decision — extend the `classify` body with:**

```
classify "Failure mode" {
  for records where type == "incident"
  features [attr "vibration", attr "temp", attr "hours"]
  trained_on records where type == "incident" and attr "resolved" == true
  label_attr "root_cause"          // the class column on training rows
  confidence >= 0.6
  label "likely {class}"           // {class} resolves to the prediction
}
```

- `trained_on records where <cond>` — reuse `parseTrainedOnClause`
  (`internal/parser/parser.go:1596`) verbatim; add a `TrainedOn
  *TrainedOnClause` field to `ast.ClassifyBlock`, a `case
  lexer.TokenTrainedOn` to `parseClassifyBlock`, and a validator arm.
- `label_attr "<name>"` — a new one-token clause naming the class column.
  Chosen over the alternatives:

  | Option | Why not |
  |---|---|
  | infer label from `trained_on` | no signal in a selector says "this attr is the target" |
  | `predict class from attr "x"` | overloads `predict`, which already means the DT block/clause; confusing |
  | reuse `label` | `label` is the output *template*; conflating it with the class *column* is a footgun |

  `label_attr` is unambiguous, one token, and mirrors `state_attr`
  (`internal/parser/parser.go:1079`) which already sets that precedent for
  "name the attribute that carries X."

- `{class}` in the `label`/`suggest` template — a reserved ref resolved
  from the prediction (see item 5).

**New lexer token:** `label_attr` (one keyword; `trained_on` already
tokenises).

### 2. Planner: dependent training FactQuery

`planClassifyBlock` emits a **second** `FactQuery` before the
`MLComputation`, built from the `trained_on` selector, into `"training"`,
and threads the label column + the training var name through Params:

```go
plan.Steps = append(plan.Steps, &FactQuery{Query: trainQuery, Into: "training"})
plan.Steps = append(plan.Steps, &MLComputation{
    Function: FuncClassifyKNN,
    Input:    "candidates",
    Params: map[string]any{
        "features": b.Features, "feature_names": names,
        "label_attr": b.LabelAttr, "training_var": "training", "k": 5,
    },
    Into: "classifications",
})
```

This is the new plan shape the issue correctly flags — the other three ML
primitives shipped in #X (`forecast`, `cluster_dbscan`,
`similarity_cosine`) operate purely on the matched-row set, so none needed
a query that feeds a later step. `k` defaults to 5.

### 3. Runtime: `Input.Training`

Add one field:

```go
type Input struct {
    Rows     [][]any
    Schema   map[string]int
    Params   map[string]any
    Entities map[int]map[string]any
    Training []TrainingRow   // NEW: labeled examples for supervised primitives
}
type TrainingRow struct { ID int; Attrs map[string]any; Label string }
```

Both dispatch paths materialise it:

- **testrunner** — `narrowByML` (`testrunner.go:896`) already has the
  in-memory `entities` map; when `Params["training_var"]` is set, resolve
  the training IDs (the second FactQuery's result already lives in `vars`),
  flatten each via `entityAttrsFlattened`, read `label_attr` → `Label`.
- **executor** — `execMLComputation` (`executor.go:704`) stores FactQuery
  results as `[][]any` projections with no entity-attribute map, so it can't
  yet feed `Entities`/`Training` to *any* multi-attribute primitive
  (cosine / DBSCAN are equally test-only there). Rather than special-case
  classify, that materialisation is deferred as shared follow-up work
  (gap #4). To avoid regressing `tln run`, the primitive degrades to "no
  predictions" when no training reaches it, and the executor treats a
  string-valued (labeling) result as non-filtering so candidate rows pass
  through unchanged. **kNN is delivered and verified through the testrunner**
  (`tln test` / `tln explain`) — the same bar `forecast`, `wma`, and the
  other primitives are held to.

### 4. The primitive — `internal/mlruntime/classify.go`

```
normalise each feature column to unit variance across training ∪ candidates
for each candidate c:
    dists = [(euclidean(c.vec, t.vec), t.label) for t in training]
    take K smallest, majority-vote labels (deterministic tiebreak: lexical)
    confidence = votes(winner) / K
    Result{ EntityID: c.id, Value: winner,
            Explanation: { confidence, k_neighbors: [{id,label,dist}...] } }
```

Register `NewKNNClassifier()` in `NewRegistry()`
(`internal/mlruntime/registry.go:13`). ~120 LoC incl. normalisation +
voting, reusing the existing `euclidean()` helper (`cluster.go:166`).
Euclidean (not cosine) is the standard kNN metric and, on z-normalised
features, keeps the golden fixtures intuitive — a candidate is classified by
whichever labeled cluster it is numerically nearest.

### 5. Surfacing the class + confidence filter

- **Class → label**: add a `Class map[int]string` (entity→prediction)
  channel to `template.RenderContext` and resolve the reserved ref
  `{class}` from it. Populated in `renderContextFor`
  (`internal/testrunner/decisions.go:446`) from the classify results,
  mirroring how `Calc` is threaded.
- **`confidence >= N`**: in `narrowByML`, when the step function is
  `classify_knn` and a confidence bound is present, drop rows whose
  `Explanation.Confidence < N` (today non-bool results are kept
  unconditionally at `testrunner.go:968`). Keep the class string as the
  Value for surviving rows.

### 6. Tests + example

- `internal/mlruntime/classify_test.go` — golden two-cluster fixtures: two
  clearly separated labeled groups, candidates that land unambiguously in
  each; plus K-boundary, tie, and empty-training cases.
- `internal/planner/classify_test.go` — assert the two-FactQuery shape.
- `internal/testrunner` end-to-end — `classify` flags/labels a candidate by
  its neighbours; `confidence >=` drops a borderline one.
- `examples/…​.tln(.test)` — a runnable classify example.
- Round-trips for free through `internal/gen` once the AST fields land
  (add a `label_attr`/`trained_on` snippet to `print_test.go`).

## Consequences

**Bought**

- `classify` becomes a real supervised primitive, explainable per the
  ADR-0001 promise (per-neighbour vote breakdown in the Explanation).
- The `Input.Training` channel + dependent-FactQuery shape are reusable by
  any future supervised primitive (a real `predict` DT trainer, naive
  Bayes, …), not one-off scaffolding.
- Closing gap #4 (executor `Entities`/`Training`) lifts cosine/DBSCAN out
  of test-only too — a latent-correctness win beyond classify.

**Paid**

- Grammar surface grows by `label_attr` + `trained_on`-in-classify. Both
  reuse existing patterns, so lexer/parser/validator/printer cost is
  mechanical.
- Runtime plumbing touches **both** dispatch paths; the executor path needs
  net-new `Entities`/`Training` assembly. This is the bulk of the risk and
  the reason the issue's "just write a primitive" framing undercounts.
- `{class}` is a new reserved template ref — must be validator-flagged so a
  `{class}` in a non-classify block is a clear error, not a silent blank.

## Out of scope (follow-ups)

- **Executor entity/training materialisation (gap #4)** — shared with
  cosine / DBSCAN; lifts classify from testrunner-only to `tln run`.
- **Validator flag for `{class}` outside classify** — template refs aren't
  validated today (unknown refs render as literal `{...}`); adding ref
  validation is its own change. `{class}` simply renders empty elsewhere.
- Weighted kNN (distance-weighted votes) and configurable `k` syntax —
  `k` is a fixed default of 5 here.
- Non-euclidean metrics (cosine / manhattan).
- Model persistence / incremental training (ADR-0001 §Future Backend Swap).
- Multi-label / probability-vector output — single winning class only.
  *(Update: the `decide` block, [ADR-0014](0014-decide-typed-decisions.md),
  extends `classify_knn` with `choices` + `emit_distribution` params to emit the
  full per-choice distribution — the probability-vector output noted here.)*
