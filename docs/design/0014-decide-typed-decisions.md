# ADR-0014: `decide` — typed decisions with calibrated distributions

## Status

Proposed.

## Context

"System 1 decision models" (Typesafe's Jev, the open
[`laya-typed-decisions`](https://huggingface.co/convaiinnovations/laya-typed-decisions)
model, and the "any small LLM's logits" recipe from
[Jev in 25 lines](https://www.nobodywho.ai/posts/jev-in-25-lines/)) all share
one primitive: given a `state` and a fixed set of `choices`, return a
**calibrated probability distribution** over them — argmax `chosen` plus a
`confidence` code can threshold on.

tln already computes exactly this shape internally. `classify_knn` votes the
`k` nearest labeled neighbours and reports the winner plus a confidence
(`winVotes/k`) — but discards the per-class breakdown. And the workflow layer
can already call an external decision model as a `tool` and read
`step(...).confidence`. What was missing is a first-class surface that (a)
exposes the full distribution with a `confidence >=` gate, and (b) offers both
an LLM-free deterministic path and a model-backed path under one block.

## Decision

Add a `decide "name" { ... }` block with two mutually-exclusive modes:

- **Deterministic** (`features` + `trained_on` + `label_attr`): reuse the
  `classify_knn` primitive with two new params, `choices` and
  `emit_distribution`, so it attaches the full per-choice distribution to the
  explanation. No new primitive, no new registry entry.
- **Model** (`ask` + `using model`): a `decide_model` GoComputation the executor
  dispatches through the **existing `ToolResolver`** — `using model "m"`
  desugars to `Tools.Call(ctx, "m", "decide", {state, choices})`. No new
  injection interface.

Mode exclusivity is enforced in the validator (the grammar stays permissive, a
flat clause loop like `classify`).

### Distribution basis: `votes/k` over declared choices

The distribution is the vote count restricted to `choices`, renormalised:

- Every declared choice is present; unseen ones are explicit `0.0`.
- Votes for labels outside `choices` (stray training classes) are dropped and
  the remaining mass renormalised, so the distribution always sums to 1 over
  exactly `choices`.
- The winner is the argmax over declared choices; `confidence` is that choice's
  probability. Hence **`probabilities[chosen] == confidence`**, and the
  `confidence >=` gate is a threshold on the chosen option's mass.

Rejected alternative: an all-training-label prior (probability = fraction of the
whole training set). It decouples `confidence` from the distribution and breaks
sum-to-1 over `choices`, so it was not adopted.

The winner's lexically-smallest tie-break and the neighbour-order determinism
are inherited unchanged from `classify_knn` ([ADR-0001](0001-ml-runtime-strategy.md)),
so deterministic `decide` is fully reproducible.

### `using model` is a runtime resolver, not a plan-time lookup

This is the key semantic divergence from `classify`. `classify`'s `using model`
resolves a tln `model` block's fitted examples **at plan time** (`p.models.Resolve`).
`decide`'s `using model` is the **name of a runtime `ToolResolver`** — the host
plugs in a Jev / laya / logits model behind it. The planner must **not** route
`decide`'s model through the plan-time resolver, and the validator must not
require the model to exist at compile time.

### Two evaluation paths (inherited)

Deterministic mode reuses `classify_knn`, so it inherits the existing
bifurcation: the training set is materialised on the `tln test` / `tln explain`
path (`narrowByML`), not yet on the `tln run` executor path
(`execMLComputation`) — the same pending work `classify`, `find similar`, and
`cluster` share (see [ADR-0006](0006-classify-knn.md)). Model mode runs on the
executor path, since it needs the injected `ToolResolver`.

## Consequences

- One new block, two modes; the deterministic path is pure reuse of an existing
  primitive, the model path pure reuse of an existing injection point.
- Model decisions are a non-deterministic external boundary: recorded as an
  auditable fact, but trace/explain must never claim the reproducibility
  guarantee deterministic mode carries.
- Closing the `execMLComputation` training-set gap (ADR-0006) would let
  deterministic `decide` yield distributions under `tln run` too — a shared
  follow-up, not required here.
