# Actions — `do` clauses

A rule's verdict clauses (`block` / `allow` / `requires`) say whether something
is permitted. A **`do` clause** says what should happen as a result:

```tln
rule "Require human review for critical paths" {
  for records where type == "pr"
    and attr "pr.changed_files" contains "internal/auth/"
  requires "review.senior_engineer"
  do require "review.senior_engineer"
  do assign "pr" attr "user.owner"
  do comment "pr" "Touches critical code owned by {attr.user.owner}"
  reason "touches a critical path"
  priority HIGH
}
```

**tln does not execute actions and does not know what any verb means.** The
engine decides *which* actions fire, resolves their arguments against the
matched row, and hands them back as data. Performing them is the host's job.

That split is the point: an action is decided independently of whether it was
carried out, so the same rule is testable with no host at all — see "Testing"
below.

> **Status.** The engine emits actions at runtime — see "Getting the actions
> out" below. `tln explain` and `tln why` still do not report them:
> `explain.Decision` has no action field, so a decision trace does not record
> actions.

## Syntax

```
DoClause = "do" VERB { Expr }
```

A rule may carry any number of `do` clauses; they fire in source order, once per
matched row. A rule needs at least one outcome — a verdict clause, a `do`
clause, or both — and the validator rejects a rule with neither.

### Verbs are the host's vocabulary

The verb is a bare name and tln does not check it against a list. A review bot
understands `approve` / `block` / `comment`; a fleet system understands
`dispatch` / `order_part`. Both are the same construct. The host validates the
names it accepts and should reject unknown ones loudly — an unknown verb that
reaches a host is a ruleset bug, and silently dropping it is the one outcome
worth ruling out.

Two syntactic notes:

- **The verb must not be quoted.** `do "approve" "pr"` is a parse error. The
  lexer hands back a string's contents, so a quoted verb would otherwise be
  indistinguishable from the bare form.
- **Keywords are allowed in verb position.** `do block "pr.merge"` is an action
  named `block`, not the `block` verdict clause. The token straight after `do`
  is always the verb.

### Arguments

Arguments are ordinary expressions, resolved per matched row:

| Argument | Resolves to |
|---|---|
| `attr "user.owner"` | that row's `user.owner` value, or nothing if unset |
| `"pr"` | the literal string |
| `"Owned by {attr.user.owner}"` | the string with template refs interpolated |
| `42`, `true` | the literal |

Anything else — arithmetic (`attr "n" + 1`), negation (`-1`), a list literal —
is a parse error, since the resolver would hand the host an empty argument with
no diagnostic.

An `attr` the row does not carry resolves to nothing rather than an empty
string, so a host can tell a missing fact from a blank one. This matters more
than it looks: the condition layer is two-valued, so a rule can fire on a row
whose other facts never got extracted, and the action payload is the only place
that distinction survives.

tln is not newline-sensitive, so an argument list ends at the next clause
keyword (`do`, `reason`, `priority`, `block`, `allow`, `requires`, `overrides`,
`logger.*`, …) or the end of the rule body.

## Getting the actions out

A host embedding the engine reads them off the run result:

```go
res, err := tln.Run(ctx, src, tln.WithFactStore(store))
for _, a := range res.Actions {
    // a.EntityID — the row it fired for
    // a.Rule     — the rule it came from
    // a.Verb     — the host's vocabulary, unvalidated by tln
    // a.Args     — resolved arguments, positional
    host.Perform(a)
}
```

`res.Actions` is every block's actions in one list; `res.Blocks[name].Actions`
is one block's. Both are always non-nil — a ruleset with no `do` clauses, or one
that matched no rows, yields an empty list, so a caller never has to tell nil
from empty.

An `attr` argument naming a fact the row does not carry is present in `Args` as
`nil`. It is not dropped: dropping it would silently shift every positional
argument after it.

The ordering is fixed, so the same facts and ruleset produce the same list on
every run: blocks by name, rows in flagged-set order within a block, and a
rule's `do` clauses in source order within a row.

Firing lives in `internal/actions` and is shared by the runtime and the
`.tln.test` runner, so a `did` assertion that passes is an assertion about
what the host will actually receive.

### Defeated rules fire nothing

Resolution runs before any action reaches the host. When a rule that matched a
row is defeated by another rule matching the *same* row — via an `overrides`
edge, resolved by [defeasible priority](defeasible.md) — its actions are absent
from the result:

```tln
rule "Tenant approve" {
  for records where type == "pr" and attr "risk" == "low"
  do approve "pr"
}

strict rule "Block secrets" {
  for records where type == "pr" and attr "secrets" == true
  overrides "Tenant approve"
  do block "pr"
}
```

A low-risk PR carrying secrets emits only `block`. A low-risk PR without them
still emits `approve` — defeat is decided per row, not per ruleset.

Only rules linked by an `overrides` edge are put through resolution. Two rules
matching one row is the normal case, not a conflict: `label` and `assign` both
matching a PR fire both, and a `priority` annotation alone never silences
another rule. Priority decides the winner *within* a conflict the author
declared with `overrides`.

## Testing

`.tln.test` asserts on the actions a rule produced, with no host involved:

```tln
test "critical path tags the owner" {
  given {
    record 1 type "pr"
    attr 1 "pr.changed_files" ["internal/auth/session.go"]
    attr 1 "user.owner" "@alice"

    record 2 type "pr"
    attr 2 "pr.changed_files" ["README.md"]
    attr 2 "user.owner" "@bob"
  }

  when rule "Require human review for critical paths"

  expect {
    flagged 1
    did 1 require "review.senior_engineer"
    did 1 assign "pr" "@alice"
    did 1 comment "pr" contains "@alice"
    did_not 2 require "review.senior_engineer"
  }
}
```

| Form | Meaning |
|---|---|
| `did <id> <verb>` | the verb fired for that row, arguments unchecked |
| `did <id> <verb> <arg> …` | positional match on the leading arguments |
| `did <id> <verb> contains "x"` | that argument contains the substring |
| `did_not <id> <verb> …` | no matching action fired |

Fewer matchers than arguments is a prefix match, so asserting a verb and its
target does not force you to restate an interpolated comment body. More matchers
than the action carries never matches, so a typo'd extra argument fails rather
than passing quietly. A failure reports what the row actually did:

```
expected entity 1 to assign "pr" "@carol", but it did not — fired:
  require "review.senior_engineer"; assign "pr" "@alice"; comment "pr" "Owned by @alice"
```

`did_not` is what catches an over-firing rule, and it is the assertion worth
writing first: a rule that fires on rows it shouldn't is invisible in a suite
that only checks the rows it should.

### List-valued attributes

`given` takes list literals, which is what the string predicates quantify over
(`contains` means "any element contains"):

```tln
attr 1 "pr.changed_files" ["internal/auth/a.go", "README.md"]
attr 2 "pr.changed_files" []
```

An empty list is a value and is distinct from an unset attribute: it matches no
predicate, while an unset attribute additionally makes an enclosing `not`
succeed.

## Worked example

`examples/pr_review.tln` is a full deterministic GitHub PR review policy
built on `do` actions, with `examples/pr_review.tln.test` covering the
cases where rules must *not* fire.
