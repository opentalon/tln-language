package actions

import (
	"reflect"
	"sort"
	"testing"

	"github.com/opentalon/tln-language/internal/ast"
)

func TestReferencedAttrs_DottedTemplateRefs(t *testing.T) {
	rule := &ast.RuleBlock{
		Name: "Deps",
		Do: []*ast.DoAction{
			{Verb: "comment", Args: []ast.Expr{
				&ast.LiteralExpr{Value: "pr"},
				&ast.LiteralExpr{Value: "{attr.pr.new_dependencies} {item.pr.draft} {pr.lines_changed} {context.user.role} {id}"},
			}},
			{Verb: "assign", Args: []ast.Expr{&ast.AttrExpr{Name: "user.owner"}}},
		},
	}
	got := ReferencedAttrs(rule)
	sort.Strings(got)
	want := []string{"pr.draft", "pr.lines_changed", "pr.new_dependencies", "user.owner"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReferencedAttrs: got %#v, want %#v", got, want)
	}
}

func TestReferencedAttrs_ContextOnlyFetchesNothing(t *testing.T) {
	rule := &ast.RuleBlock{
		Name: "Ctx",
		Do: []*ast.DoAction{
			{Verb: "comment", Args: []ast.Expr{
				&ast.LiteralExpr{Value: "{context.role} {context.user.role}"},
			}},
		},
	}
	if got := ReferencedAttrs(rule); len(got) != 0 {
		t.Fatalf("want no attrs for context-only refs, got %#v", got)
	}
}

func TestTemplateRefAttr(t *testing.T) {
	cases := map[string]string{
		"name":                     "name",
		"attr.km":                  "km",
		"item.name":                "name",
		"attr.pr.new_dependencies": "pr.new_dependencies",
		"item.pr.draft":            "pr.draft",
		"pr.lines_changed":         "pr.lines_changed",
		"attr.attr.x":              "attr.x",
		"attr.":                    "",
		"item.":                    "",
		"context.role":             "",
		"context.user.role":        "",
		"context.":                 "",
		"contextual.x":             "contextual.x",
		"attributes.x":             "attributes.x",
	}
	for in, want := range cases {
		if got := templateRefAttr(in); got != want {
			t.Errorf("templateRefAttr(%q) = %q, want %q", in, got, want)
		}
	}
}
