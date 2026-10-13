# Block execution order

`RunAll` runs every block of a program once, against one fact store. Blocks
run in dependency order, not source or map order: a block runs after every
other block that writes an attribute it reads.

## Reads and writes

- **Writes** are an `enrich` block's `update attr` targets — the only block
  that asserts facts during a run.
- **Reads** are the attributes a block's selector, `when`, `having`, features,
  tool arguments and action templates reference, for every block kind
  (`rule`, `enrich`, `detect`, `classify`, `predict`, `decide`, `cluster`,
  `similar`, `related`, `combine`, `forecast`, `recommend`, state machines).
  That includes temporal (`older_than` / `newer_than`), `changed to`,
  anomaly and correlation conditions.
- Reads follow what the planner inlines: an `is "name"` define and a derived
  predicate `name(v)` read whatever their bodies read, transitively.

So an `enrich` gated on another `enrich`'s result, and a rule or `detect` gated
on either, see the facts they depend on in the same run:

```tln
enrich "Decide how a policy changes access" {
  for records where type == "code_unit"
  tool "jev" "decide" { state concat("Policy ", attr "unit.name") }
  update attr "unit.access_change" from result.chosen
}

enrich "Explain units the decider flagged" {
  for records where type == "code_unit" and attr "unit.access_change" == "widens access"
  tool "llm" "review" { unit attr "unit.name" }
  update attr "unit.llm_result" from result.verdict
}
```

## Ties and cycles

- Blocks with no dependency between them run in name order, so every run is
  the same.
- A block that reads its own write (an `enrich` refreshing the attribute it
  filters on) is not a dependency.
- Blocks that read each other's writes form a cycle and can't be ordered. The
  cycle is broken at its member with the smallest name, which runs first; the
  rest of the order still puts writers before readers, including blocks
  downstream of the cycle. The broken-at blocks are logged as a warning.
