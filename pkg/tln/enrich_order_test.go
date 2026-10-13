package tln_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/opentalon/tln-language/pkg/tln"
)

// A typed decision decides whether a chat model reviews a unit, and a rule
// gates on the review. Block names sort reader-first ("A…" reads what
// "B…" writes, "0…" reads what "A…" writes), so name or map order alone
// would run the review before the decision exists.
const gatedReviewSrc = `
rule "0 Block an access change the review confirms" {
  for records where type == "code_unit" and attr "unit.llm_result" == "mismatch"
  do block "pr.merge"
  do comment "pr" "{attr.unit.name}: {attr.unit.llm_explanation}"
}

enrich "A Explain units the decider flagged" {
  for records where type == "code_unit" and attr "unit.access_change" == "widens access"
  stale_after 1 hour
  tool "llm" "review" {
    unit attr "unit.name"
  }
  update attr "unit.llm_result"      from result.verdict
  update attr "unit.llm_explanation" from result.explanation
}

enrich "B Decide how a policy changes access" {
  for records where type == "code_unit"
  stale_after 1 hour
  tool "jev" "decide" {
    state concat("Policy ", attr "unit.name")
  }
  update attr "unit.access_change" from result.chosen
}
`

const gatedReviewSeed = `
test "seed" {
  given {
    record 1 type "code_unit"
    attr 1 "unit.name" "app/policies/item_policy.rb"
    record 2 type "code_unit"
    attr 2 "unit.name" "app/policies/report_policy.rb"
  }
  when rule "noop"
  expect { flagged 1 }
}
`

func TestRun_EnrichGatedOnAnotherEnrichRunsAfterIt(t *testing.T) {
	// Map iteration order varies run to run; repeat so a lucky order can't pass.
	for i := 0; i < 25; i++ {
		store := tln.NewMemoryStore()
		if _, err := tln.Seed(context.Background(), store, gatedReviewSeed); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		caller := &mockCaller{handler: func(server, _ string, args map[string]any) (any, error) {
			switch server {
			case "jev":
				if args["state"] == "Policy app/policies/item_policy.rb" {
					return map[string]any{"chosen": "widens access"}, nil
				}
				return map[string]any{"chosen": "no change to access"}, nil
			case "llm":
				return map[string]any{"verdict": "mismatch", "explanation": "admins now edit any item"}, nil
			}
			return nil, nil
		}}
		res, err := tln.Run(context.Background(), gatedReviewSrc, tln.WithFactStore(store), tln.WithToolResolver(caller))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		var servers []string
		for _, c := range caller.calls {
			servers = append(servers, c.Server)
		}
		if want := []string{"jev", "jev", "llm"}; !reflect.DeepEqual(servers, want) {
			t.Fatalf("run %d: calls %v, want %v (decider on both units, then one review)", i, servers, want)
		}
		if got := caller.calls[2].Args["unit"]; got != "app/policies/item_policy.rb" {
			t.Fatalf("run %d: reviewed %v, want only the unit the decider flagged", i, got)
		}
		want := []tln.Action{
			{EntityID: 1, Rule: "0 Block an access change the review confirms", Verb: "block", Args: []any{"pr.merge"}},
			{EntityID: 1, Rule: "0 Block an access change the review confirms", Verb: "comment", Args: []any{"pr", "app/policies/item_policy.rb: admins now edit any item"}},
		}
		if !reflect.DeepEqual(res.Actions, want) {
			t.Fatalf("run %d: actions\n got %#v\nwant %#v", i, res.Actions, want)
		}
	}
}

// The gate can sit behind a define (`is "…"`) or a derived predicate: both
// are inlined at plan time, so the review still depends on the decider.
const gatedViaInlinedSrc = `
define "widens_access" {
  attr "unit.access_change" == "widens access"
}

derive needs_explaining(u) {
  for records where type == "code_unit" and is "widens_access"
}

rule "0 Block an access change the review confirms" {
  for records where type == "code_unit" and attr "unit.llm_result" == "mismatch"
  do block "pr.merge"
}

enrich "A Explain units the decider flagged" {
  for records where needs_explaining(u)
  stale_after 1 hour
  tool "llm" "review" {
    unit attr "unit.name"
  }
  update attr "unit.llm_result" from result.verdict
}

enrich "B Decide how a policy changes access" {
  for records where type == "code_unit"
  stale_after 1 hour
  tool "jev" "decide" {
    state concat("Policy ", attr "unit.name")
  }
  update attr "unit.access_change" from result.chosen
}
`

func TestRun_EnrichGatedThroughDefineAndDeriveRunsAfterIt(t *testing.T) {
	for i := 0; i < 25; i++ {
		store := tln.NewMemoryStore()
		if _, err := tln.Seed(context.Background(), store, gatedReviewSeed); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		caller := &mockCaller{handler: func(server, _ string, args map[string]any) (any, error) {
			if server == "jev" {
				if args["state"] == "Policy app/policies/item_policy.rb" {
					return map[string]any{"chosen": "widens access"}, nil
				}
				return map[string]any{"chosen": "no change to access"}, nil
			}
			return map[string]any{"verdict": "mismatch"}, nil
		}}
		res, err := tln.Run(context.Background(), gatedViaInlinedSrc, tln.WithFactStore(store), tln.WithToolResolver(caller))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		var servers []string
		for _, c := range caller.calls {
			servers = append(servers, c.Server)
		}
		if want := []string{"jev", "jev", "llm"}; !reflect.DeepEqual(servers, want) {
			t.Fatalf("run %d: calls %v, want %v", i, servers, want)
		}
		want := []tln.Action{{EntityID: 1, Rule: "0 Block an access change the review confirms", Verb: "block", Args: []any{"pr.merge"}}}
		if !reflect.DeepEqual(res.Actions, want) {
			t.Fatalf("run %d: actions\n got %#v\nwant %#v", i, res.Actions, want)
		}
	}
}
