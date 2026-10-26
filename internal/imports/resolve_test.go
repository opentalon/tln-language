package imports

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/lexer"
	"github.com/opentalon/tln-language/internal/parser"
)

// writeFile writes a temp file and returns its full path.
func writeFile(t *testing.T, dir, name, src string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// parseFile reads + parses a single file (no import resolution).
func parseFile(t *testing.T, path string) *ast.Program {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	tokens, ld := lexer.Lex(filepath.Base(path), string(src))
	if ld.HasErrors() {
		t.Fatalf("lex: %v", ld)
	}
	prog, pd := parser.Parse(filepath.Base(path), tokens)
	if pd.HasErrors() {
		t.Fatalf("parse: %v", pd)
	}
	return prog
}

func TestResolveSingleImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "shared.tln", `
define "active" {
  status == "active"
}
`)
	mainPath := writeFile(t, dir, "main.tln", `
import "./shared.tln"

detect "Live" {
  for records where type == "item" and is "active"
  flag matching items
}
`)
	prog := parseFile(t, mainPath)
	merged, diags := Resolve(prog, mainPath)
	if diags.HasErrors() {
		t.Fatalf("Resolve errors: %v", diags)
	}
	// Should have both the imported define and the local detect.
	if len(merged.Blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(merged.Blocks))
	}
	// Imported "active" define should land first (walk order).
	if _, ok := merged.Blocks[0].(*ast.DefineBlock); !ok {
		t.Errorf("first block should be the imported define, got %T", merged.Blocks[0])
	}
}

func TestResolveNestedImports(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "leaf.tln", `
define "leaf_def" {
  type == "leaf"
}
`)
	writeFile(t, dir, "middle.tln", `
import "./leaf.tln"

define "middle_def" {
  is "leaf_def"
}
`)
	mainPath := writeFile(t, dir, "main.tln", `
import "./middle.tln"

detect "All" {
  for records where is "middle_def"
  flag matching items
}
`)
	prog := parseFile(t, mainPath)
	merged, diags := Resolve(prog, mainPath)
	if diags.HasErrors() {
		t.Fatalf("Resolve errors: %v", diags)
	}
	if len(merged.Blocks) != 3 {
		t.Fatalf("want 3 blocks (leaf + middle + main), got %d", len(merged.Blocks))
	}
	// Depth-first: leaf is inlined before middle.
	names := make([]string, 0, 3)
	for _, b := range merged.Blocks {
		names = append(names, b.BlockName())
	}
	if names[0] != "leaf_def" || names[1] != "middle_def" || names[2] != "All" {
		t.Errorf("order: got %v, want [leaf_def middle_def All]", names)
	}
}

func TestResolveDetectsCycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.tln", `
import "./b.tln"

define "a_def" {
  type == "a"
}
`)
	writeFile(t, dir, "b.tln", `
import "./a.tln"

define "b_def" {
  type == "b"
}
`)
	mainPath := writeFile(t, dir, "main.tln", `
import "./a.tln"

detect "X" {
  for records where type == "x"
  flag matching items
}
`)
	prog := parseFile(t, mainPath)
	_, diags := Resolve(prog, mainPath)
	if !diags.HasErrors() {
		t.Fatal("expected cycle error")
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "cycle") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want a cycle diagnostic, got %v", diags)
	}
}

func TestResolveMissingFile(t *testing.T) {
	dir := t.TempDir()
	mainPath := writeFile(t, dir, "main.tln", `
import "./nope.tln"

detect "X" {
  for records where type == "item"
  flag matching items
}
`)
	prog := parseFile(t, mainPath)
	_, diags := Resolve(prog, mainPath)
	if !diags.HasErrors() {
		t.Fatal("expected error for missing file")
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "cannot read") {
			return
		}
	}
	t.Errorf("want a 'cannot read' diagnostic, got %v", diags)
}

func TestResolveCallerShadowsImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "shared.tln", `
define "active" {
  status == "active"
}
`)
	mainPath := writeFile(t, dir, "main.tln", `
import "./shared.tln"

define "active" {
  status == "running"
}

detect "X" {
  for records where is "active"
  flag matching items
}
`)
	prog := parseFile(t, mainPath)
	merged, diags := Resolve(prog, mainPath)
	if !diags.HasErrors() {
		t.Fatalf("want an error for a caller block redefining an imported name, got %v", diags)
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, `duplicate block name "active"`) && strings.Contains(d.Message, "shared.tln") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a duplicate-name diagnostic naming the imported file, got %v", diags)
	}
	// The caller's block still wins so downstream phases see one "active".
	if len(merged.Blocks) != 2 {
		t.Fatalf("want 2 blocks after shadowing, got %d", len(merged.Blocks))
	}
}

// A caller must not be able to delete an imported strict rule by
// redefining its name — `overrides` against a strict rule is already a
// compile error, and this is the same defeat by another route.
func TestResolveCallerCannotShadowStrictRule(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "base.tln", `
strict rule "no direct prod writes" {
  for records where type == "deploy" and attr "target" == "prod"
  block "apply"
  reason "prod writes need review"
}
`)
	mainPath := writeFile(t, dir, "tenant.tln", `
import "./base.tln"

rule "no direct prod writes" {
  for records where type == "deploy" and attr "target" == "nonexistent"
  allow "apply"
}
`)
	prog := parseFile(t, mainPath)
	_, diags := Resolve(prog, mainPath)
	if !diags.HasErrors() {
		t.Fatalf("want an error for a caller rule redefining an imported strict rule, got %v", diags)
	}
	for _, d := range diags {
		if strings.Contains(d.Message, `duplicate block name "no direct prod writes"`) {
			return
		}
	}
	t.Errorf("want a duplicate-name diagnostic, got %v", diags)
}

// Distinct names across an import boundary stay clean — no false
// positives from the shadowing check.
func TestResolveNoShadowNoDiagnostic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "shared.tln", `
define "active" {
  status == "active"
}
`)
	mainPath := writeFile(t, dir, "main.tln", `
import "./shared.tln"

define "idle" {
  status == "idle"
}

detect "X" {
  for records where is "active"
  flag matching items
}
`)
	prog := parseFile(t, mainPath)
	merged, diags := Resolve(prog, mainPath)
	if len(diags) != 0 {
		t.Fatalf("want no diagnostics, got %v", diags)
	}
	if len(merged.Blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(merged.Blocks))
	}
}

func TestResolveSiblingConflictWarns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "left.tln", `
define "shared" {
  type == "left"
}
`)
	writeFile(t, dir, "right.tln", `
define "shared" {
  type == "right"
}
`)
	mainPath := writeFile(t, dir, "main.tln", `
import "./left.tln"
import "./right.tln"

detect "X" {
  for records where type == "item"
  flag matching items
}
`)
	prog := parseFile(t, mainPath)
	merged, diags := Resolve(prog, mainPath)
	// The first-wins resolution should keep going (warning, not error).
	if diags.HasErrors() {
		t.Fatalf("conflicting imports should warn, not error: %v", diags)
	}
	hasWarn := false
	for _, d := range diags {
		if strings.Contains(d.Message, "conflicts") {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("want a conflict warning, got %v", diags)
	}
	// Two blocks (the surviving shared + the detect).
	if len(merged.Blocks) != 2 {
		t.Fatalf("want 2 blocks (first-wins), got %d", len(merged.Blocks))
	}
}

func TestResolveLateImportRejected(t *testing.T) {
	// `import` after a block — the parser already rejects this, but
	// the test pins the diagnostic so we notice if it ever stops.
	dir := t.TempDir()
	src := `
detect "X" {
  for records where type == "x"
  flag matching items
}

import "./other.tln"
`
	mainPath := writeFile(t, dir, "main.tln", src)
	srcBytes, _ := os.ReadFile(mainPath)
	tokens, _ := lexer.Lex("main.tln", string(srcBytes))
	_, pd := parser.Parse("main.tln", tokens)
	if !pd.HasErrors() {
		t.Fatal("expected parse error for late import")
	}
	found := false
	for _, d := range pd {
		if strings.Contains(d.Message, "before any block") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a 'before any block' diagnostic, got %v", pd)
	}
}

func TestResolveNoImportsIsNoOp(t *testing.T) {
	src := `
detect "X" {
  for records where type == "item"
  flag matching items
}
`
	tokens, _ := lexer.Lex("test.tln", src)
	prog, _ := parser.Parse("test.tln", tokens)
	merged, diags := Resolve(prog, "test.tln")
	if diags.HasErrors() {
		t.Fatalf("no-import resolve should be clean: %v", diags)
	}
	if merged != prog {
		t.Error("no-imports path should return the same program pointer (no-op fast path)")
	}
}

func TestResolveFallsBackToSearchDirs(t *testing.T) {
	lib := t.TempDir()
	writeFile(t, lib, "base.tln", `
define "active" {
  status == "active"
}
`)
	dir := t.TempDir()
	mainPath := writeFile(t, dir, "main.tln", `
import "base.tln"

detect "Live" {
  for records where type == "item" and is "active"
  flag matching items
}
`)
	merged, diags := Resolve(parseFile(t, mainPath), mainPath, "", t.TempDir(), lib)
	if diags.HasErrors() {
		t.Fatalf("Resolve errors: %v", diags)
	}
	if len(merged.Blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(merged.Blocks))
	}
	if d, ok := merged.Blocks[0].(*ast.DefineBlock); !ok || d.Name != "active" {
		t.Errorf("first block = %#v, want the define from the search dir", merged.Blocks[0])
	}
}

func TestResolvePrefersTheImportersDirOverSearchDirs(t *testing.T) {
	lib := t.TempDir()
	writeFile(t, lib, "base.tln", `
define "from_lib" {
  status == "active"
}
`)
	dir := t.TempDir()
	writeFile(t, dir, "base.tln", `
define "from_here" {
  status == "active"
}
`)
	mainPath := writeFile(t, dir, "main.tln", `
import "base.tln"

detect "X" {
  for records where type == "item"
  flag matching items
}
`)
	merged, diags := Resolve(parseFile(t, mainPath), mainPath, lib)
	if diags.HasErrors() {
		t.Fatalf("Resolve errors: %v", diags)
	}
	if d, ok := merged.Blocks[0].(*ast.DefineBlock); !ok || d.Name != "from_here" {
		t.Errorf("first block = %#v, want the define next to main.tln", merged.Blocks[0])
	}
}

func TestResolveSearchDirImportResolvesItsOwnImportsFromItsDir(t *testing.T) {
	lib := t.TempDir()
	writeFile(t, lib, "helpers.tln", `
define "active" {
  status == "active"
}
`)
	writeFile(t, lib, "base.tln", `
import "helpers.tln"

detect "Base" {
  for records where type == "item" and is "active"
  flag matching items
}
`)
	dir := t.TempDir()
	mainPath := writeFile(t, dir, "main.tln", `
import "base.tln"

detect "X" {
  for records where type == "item"
  flag matching items
}
`)
	merged, diags := Resolve(parseFile(t, mainPath), mainPath, lib)
	if diags.HasErrors() {
		t.Fatalf("Resolve errors: %v", diags)
	}
	if len(merged.Blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(merged.Blocks))
	}
}

func TestResolveMissingInEverySearchDir(t *testing.T) {
	dir := t.TempDir()
	mainPath := writeFile(t, dir, "main.tln", `
import "base.tln"

detect "X" {
  for records where type == "item"
  flag matching items
}
`)
	_, diags := Resolve(parseFile(t, mainPath), mainPath, "", t.TempDir(), filepath.Join(dir, "absent"))
	if !diags.HasErrors() {
		t.Fatal("expected error for an import found in no search dir")
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "cannot read") && strings.Contains(d.Message, filepath.Join(dir, "base.tln")) {
			return
		}
	}
	t.Errorf("want a 'cannot read' diagnostic naming the importer's dir, got %v", diags)
}

func TestResolveSearchDirsIgnoreAbsoluteImports(t *testing.T) {
	lib := t.TempDir()
	writeFile(t, lib, "base.tln", `
define "active" {
  status == "active"
}
`)
	dir := t.TempDir()
	missing := filepath.Join(dir, "base.tln")
	mainPath := writeFile(t, dir, "main.tln", `
import "`+filepath.ToSlash(missing)+`"

detect "X" {
  for records where type == "item"
  flag matching items
}
`)
	_, diags := Resolve(parseFile(t, mainPath), mainPath, lib)
	if !diags.HasErrors() {
		t.Fatal("an absolute import must not fall back to a search dir")
	}
}
