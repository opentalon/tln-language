# Typed decisions (`decide`)

A `decide` block turns a record into a **typed decision**: given a fixed set of
`choices`, it produces a **calibrated probability distribution** over them, the
argmax `chosen` option, and a `confidence` — so code can act only when the
decision is sure enough. It's the tln surface for the "System 1 decision model"
pattern (classify an email as `Legitimate` / `Spam` / `Phishing`, route a ticket
to `p0` / `p1` / `p2`, score a claim), and it comes in two modes.

- **Deterministic mode** — no model, no LLM. Reuses tln's own kNN classifier and
  exposes the *full* per-choice vote distribution (`votes/k`), not just the
  winner. Fully reproducible and auditable, like every tln ML primitive.
- **Model mode** — delegates to an external decision model (e.g. a
  [Jev / laya](#using-an-external-model-jev--laya) System-1 model) behind the
  injected `ToolResolver`. The model understands raw text and returns the
  distribution; tln gates and records it. A non-deterministic external boundary.

Both modes yield the same shape — `{chosen, confidence, probabilities}` — so
downstream `when` guards, labels, and tool args read them the same way.

## Deterministic mode

```tln
decide "email_kind" {
  for records where folder == "Inbox"
  choices ["Legitimate", "Spam", "Phishing"]
  features [attr "link_count", attr "caps_ratio", attr "sender_age_days"]
  trained_on records where labeled == true
  label_attr "kind"
  confidence >= 0.7
}
```

| Clause | Meaning |
|---|---|
| `for records where …` | the **candidates** to decide on |
| `choices [ … ]` | the fixed set of options the distribution ranges over |
| `features [ … ]` | the numeric attributes distance is measured on |
| `trained_on records where …` | the **labeled examples** the vote draws from |
| `label_attr "…"` | which attribute on those examples holds the class |
| `confidence >= N` | *(optional)* drop decisions whose top probability is below `N` |

The distribution is the kNN vote restricted to the declared `choices`:

- Every declared choice is present; one nobody votes for is an explicit `0.0`.
- Votes for labels **outside** `choices` (stray training classes) are dropped
  and the remaining mass renormalised, so the distribution always sums to 1 over
  exactly `choices`.
- The basis is `votes/k`, so `probabilities[chosen]` equals `confidence`, and
  the `confidence >=` gate is a threshold on the chosen option's probability.
- The winner is the argmax over declared choices, lexically-smallest tie-break —
  deterministic, per [ADR-0001](design/0001-ml-runtime-strategy.md).

Example: a candidate whose 3 nearest labeled neighbours are `Spam, Spam,
Legitimate` yields `{Spam: 0.67, Legitimate: 0.33, Phishing: 0.0}`, chosen
`Spam`, confidence `0.67` — dropped by a `confidence >= 0.7` gate.

## Model mode

When the input is raw natural-language text with no labeled features, hand the
state to a model instead:

```tln
decide "email_kind" {
  for records where folder == "Inbox"
  choices ["Legitimate", "Spam", "Phishing"]
  ask concat("Subject: ", attr "subject", "\n\n", attr "body")
  using model "jev-small"
  confidence >= 0.9
}
```

| Clause | Meaning |
|---|---|
| `ask <expr>` | the **state** string handed to the model (rendered per entity) |
| `using model "…"` | the resolver handle — a `ToolResolver` server name |

Per candidate, tln renders `ask` against the entity's attributes and calls the
resolver as `(server = model, tool = "decide", args = {state, choices})`. The
resolver returns `{chosen, confidence, probabilities}`; tln applies the
`confidence >=` gate and records the decision. With no `ToolResolver` injected
the step stubs out (like an MCP call), so a plan still runs.

`ask`/`using model` and `features`/`trained_on` are **mutually exclusive** — a
block is either deterministic or model-backed, and the validator enforces it.

### Using an external model: Jev / laya

Model mode is how tln consumes a "System 1" decision model — a small, calibrated
classifier that reads a prompt + choices and returns probabilities:

- **[laya](https://huggingface.co/convaiinnovations/laya-typed-decisions)** — an
  open-weights ModernBERT typed-decision model (choice / score / noul).
- **[Jev](https://typesafe.ai/)** — a hosted System-1 decision model.
- **Any small LLM** — read the logits over the choice tokens and softmax them,
  as in [Jev in 25 lines](https://www.nobodywho.ai/posts/jev-in-25-lines/).

Wrap any of them as a `ToolResolver` that answers the `"decide"` tool with
`{chosen, confidence, probabilities}`; bind it to a name with a `connector`:

```tln
connector "jev-small" via mcp {
  endpoint env "JEV_URL"
  model "Qwen3-0.6B"
}
```

tln orchestrates and gates the decision; it never embeds the model, key, or
transport. The model stays entirely on the host side of the boundary.

## Downstream

The decision is recorded per entity. `{chosen}` interpolates into the block's
`label`, and in a workflow `step(...).chosen` / `.confidence` /
`.probabilities.Spam` read the result through the usual step-result navigation
— so you can guard an action on it:

```tln
when step("classify").chosen == "Phishing" and step("classify").confidence >= 0.9
```

## Explainability & determinism

Deterministic mode carries the same first-class explanation contract as
`classify`: the `k` neighbours that voted plus the full distribution, so `tln
explain` shows exactly how the decision was reached, reproducibly.

Model mode is a **non-deterministic external boundary**: the decision depends on
the host's model, so it's recorded as an auditable fact but never claims the
reproducibility guarantee deterministic mode carries. See
[ADR-0014](design/0014-decide-typed-decisions.md).

## Limitations

- Like `classify`, deterministic mode's full evaluation (materialising the
  training set) runs on the `tln test` / `tln explain` path; the `tln run`
  executor path doesn't yet materialise multi-attribute training sets (shared
  work tracked in [ADR-0006](design/0006-classify-knn.md)). Model mode runs on
  the executor path via the injected `ToolResolver`.
- `k` is fixed at 5 (inherited from the kNN primitive).
- Non-numeric features contribute 0 to the vector — deterministic mode decides
  on numeric signal; use model mode for raw text.
