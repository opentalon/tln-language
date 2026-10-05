package tln_test

import (
	"reflect"
	"testing"

	"github.com/opentalon/tln-language/pkg/tln"
)

func TestToolReferences_WorkflowSteps(t *testing.T) {
	// Two steps, two distinct tools on the same server.
	src := `
workflow "certs" {
  step "skills" {
    tool "timly-api" "list_person_skills" { expiry_from "x" }
  }
  step "notify" depends_on "skills" {
    tool "timly-api" "notify_user" { to "me" }
  }
}`
	got, err := tln.ToolReferences(src)
	if err != nil {
		t.Fatalf("ToolReferences: unexpected error: %v", err)
	}
	want := []tln.ToolRef{
		{Server: "timly-api", Tool: "list_person_skills"},
		{Server: "timly-api", Tool: "notify_user"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %+v, want %+v", got, want)
	}
}

func TestToolReferences_DeduplicatedAndSorted(t *testing.T) {
	// Same tool called twice plus an out-of-order server: result is distinct
	// and sorted by (server, tool).
	src := `
workflow "w" {
  step "a" { tool "svc-b" "do" { } }
  step "b" { tool "svc-a" "zed" { } }
  step "c" { tool "svc-a" "act" { } }
  step "d" { tool "svc-b" "do" { } }
}`
	got, err := tln.ToolReferences(src)
	if err != nil {
		t.Fatalf("ToolReferences: unexpected error: %v", err)
	}
	want := []tln.ToolRef{
		{Server: "svc-a", Tool: "act"},
		{Server: "svc-a", Tool: "zed"},
		{Server: "svc-b", Tool: "do"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %+v, want %+v", got, want)
	}
}

func TestToolReferences_ReactiveRemediateDeepWalk_TestBlockExcluded(t *testing.T) {
	// The tool lives in a detect→remediate block (not a workflow step), proving
	// the walk descends into reactive blocks. The test block's `mock tool` stub
	// is NOT a real call and must be excluded from the manifest.
	src := `
detect "Defective without ticket" {
  for records where status == "defective"
  flag matching items
  remediate {
    tool "inventory" "create-ticket" {
      title "Auto: {item.name} is defective"
      priority "high"
    }
  }
}
test "mock is not a real call" {
  given { record 1 type "item" attr 1 "status" "defective" }
  mock tool "inventory" "create-ticket" { }
  when detect "Defective without ticket"
  expect { flagged 1 }
}`
	got, err := tln.ToolReferences(src)
	if err != nil {
		t.Fatalf("ToolReferences: unexpected error: %v", err)
	}
	want := []tln.ToolRef{{Server: "inventory", Tool: "create-ticket"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %+v, want %+v (test-block mock must be excluded)", got, want)
	}
}

func TestToolReferences_NoTools(t *testing.T) {
	got, err := tln.ToolReferences(`workflow "empty" { }`)
	if err != nil {
		t.Fatalf("ToolReferences: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no tools, got %+v", got)
	}
}

func TestToolReferences_CompileErrorPropagates(t *testing.T) {
	_, err := tln.ToolReferences(`workflow "broken" { step "s" {`)
	if err == nil {
		t.Fatal("expected a compile error for invalid source")
	}
}
