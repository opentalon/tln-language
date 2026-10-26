package main

import (
	"os"
	"path/filepath"
	"testing"
)

const tlnPathRules = `import "base.tln"

detect "Live" {
  for records where type == "item" and is "active"
  flag matching items
}
`

func writeTlnPathFixture(t *testing.T) (lib, rulesPath string) {
	t.Helper()
	lib = t.TempDir()
	if err := os.WriteFile(filepath.Join(lib, "base.tln"), []byte("define \"active\" {\n  status == \"active\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rulesPath = filepath.Join(t.TempDir(), "rules.tln")
	if err := os.WriteFile(rulesPath, []byte(tlnPathRules), 0o644); err != nil {
		t.Fatal(err)
	}
	return lib, rulesPath
}

func TestCompileResolvesImportsFromTlnPath(t *testing.T) {
	lib, rulesPath := writeTlnPathFixture(t)
	t.Setenv("TLN_PATH", string(filepath.ListSeparator)+t.TempDir()+string(filepath.ListSeparator)+lib)
	if _, ok := compile("rules.tln", rulesPath, tlnPathRules); !ok {
		t.Fatal("compile failed, want base.tln found through TLN_PATH")
	}
}

func TestCompileWithoutTlnPathCannotReadTheImport(t *testing.T) {
	_, rulesPath := writeTlnPathFixture(t)
	t.Setenv("TLN_PATH", "")
	if _, ok := compile("rules.tln", rulesPath, tlnPathRules); ok {
		t.Fatal("compile succeeded, want the missing import to fail without TLN_PATH")
	}
}
