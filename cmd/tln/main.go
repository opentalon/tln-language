package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opentalon/tln-language/internal/diagnostic"
	"github.com/opentalon/tln-language/internal/executor"
	"github.com/opentalon/tln-language/internal/explain"
	"github.com/opentalon/tln-language/internal/factstore"
	"github.com/opentalon/tln-language/internal/imports"
	"github.com/opentalon/tln-language/internal/lexer"
	tlnlog "github.com/opentalon/tln-language/internal/log"
	"github.com/opentalon/tln-language/internal/parser"
	"github.com/opentalon/tln-language/internal/planner"
	"github.com/opentalon/tln-language/internal/repl"
	"github.com/opentalon/tln-language/internal/testrunner"
	"github.com/opentalon/tln-language/internal/validator"
)

// version is the tln CLI version. Overridden at release time via
// `-ldflags="-X main.version=v0.1.0"`; left as "dev" for `go install`
// and `go run` builds.
var version = "dev"

func main() {
	// Strip `--log-format` and `--log-level` from os.Args before subcommand
	// dispatch, so each subcommand's own arg parser doesn't have to know
	// about them. Logging defaults to text format at warn level — quiet
	// enough for `tln build`/`repl` not to clutter output, but errors
	// still surface. Users opt into more detail with `--log-level=info`.
	stripGlobalLogFlags()

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tln [--log-format=text|json] [--log-level=debug|info|warn|error] <command> [args]")
		fmt.Fprintln(os.Stderr, "commands: build, test, run, repl, trace, explain, why, mod, version")
		os.Exit(diagnostic.ExitUsage)
	}

	switch os.Args[1] {
	case "build":
		runBuild()
	case "test":
		runTest()
	case "run":
		runExecute()
	case "init":
		runInit()
	case "bundle":
		runBundle()
	case "repl":
		preload := ""
		if len(os.Args) > 2 {
			preload = os.Args[2]
		}
		if err := repl.RunWithVersionFile(os.Stdin, os.Stdout, version, preload); err != nil {
			fmt.Fprintf(os.Stderr, "tln repl: %v\n", err)
			os.Exit(diagnostic.ExitError)
		}
	case "collect":
		runCollect()
	case "trace":
		runTrace()
	case "explain":
		runExplain()
	case "why":
		runWhy()
	case "mod":
		fmt.Fprintln(os.Stderr, "tln mod: not yet implemented")
		os.Exit(diagnostic.ExitError)
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "tln: unknown command %q\n", os.Args[1])
		os.Exit(diagnostic.ExitUsage)
	}
}

func runBuild() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: tln build <file.tln>")
		os.Exit(diagnostic.ExitUsage)
	}

	path := os.Args[2]
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln build: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}

	file := filepath.Base(path)
	var allDiags diagnostic.List

	// Lex
	tokens, ld := lexer.Lex(file, string(src))
	allDiags = append(allDiags, ld...)

	// Parse
	prog, pd := parser.Parse(file, tokens)
	allDiags = append(allDiags, pd...)

	// Resolve `import "./other.tln"` directives by merging the named
	// files' blocks into the program before validation.
	if len(prog.Imports) > 0 {
		merged, importDiags := imports.Resolve(prog, path, filepath.SplitList(os.Getenv("TLN_PATH"))...)
		allDiags = append(allDiags, importDiags...)
		if !importDiags.HasErrors() {
			fmt.Printf("==> %s: resolved %d import(s)\n", file, len(prog.Imports))
			prog = merged
		}
	}

	// Validate
	vd := validator.Validate(file, prog)
	allDiags = append(allDiags, vd...)

	// Print warnings/errors
	for _, d := range allDiags {
		if d.Severity == diagnostic.Error {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		} else if d.Severity == diagnostic.Warning {
			fmt.Fprintf(os.Stderr, "warn:  %s\n", d)
		}
	}

	if allDiags.HasErrors() {
		os.Exit(diagnostic.ExitError)
	}

	// Plan
	plans, planDiags := planner.Plan(prog)
	for _, d := range planDiags {
		fmt.Fprintf(os.Stderr, "error: %s\n", d)
	}
	if planDiags.HasErrors() {
		os.Exit(diagnostic.ExitError)
	}

	// Print summary
	fmt.Printf("==> %s: %d block(s) compiled\n", file, len(plans))

	// Sort block names for stable output
	names := make([]string, 0, len(plans))
	for name := range plans {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		plan := plans[name]
		fmt.Printf("\n  [%s] %d step(s)\n", plan.BlockName, len(plan.Steps))
		for i, step := range plan.Steps {
			printStep(i+1, step)
		}
	}
}

func runTest() {
	args := os.Args[2:]
	var paths []string
	runFilter := ""
	verbose := false
	junitOut := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-run" && i+1 < len(args):
			runFilter = args[i+1]
			i++
		case a == "-v":
			verbose = true
		case a == "--junit" && i+1 < len(args):
			junitOut = args[i+1]
			i++
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "tln test: unknown flag %q\n", a)
			fmt.Fprintln(os.Stderr, "usage: tln test [paths...] [-run NAME] [-v] [--junit FILE]")
			os.Exit(diagnostic.ExitUsage)
		default:
			paths = append(paths, a)
		}
	}

	pairs, err := resolveTestPairs(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln test: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}
	if len(pairs) == 0 {
		fmt.Fprintln(os.Stderr, "tln test: no .tln.test files found")
		os.Exit(diagnostic.ExitError)
	}

	var suites []testrunner.JUnitSuite
	totalPassed, totalFailed := 0, 0
	for _, p := range pairs {
		results, ok := runTestPair(p.rules, p.test, runFilter, verbose)
		if !ok {
			os.Exit(diagnostic.ExitError)
		}
		suites = append(suites, testrunner.JUnitSuite{
			File:    filepath.Base(p.test),
			Results: results,
		})
		for _, r := range results {
			if r.Passed {
				totalPassed++
			} else {
				totalFailed++
			}
		}
	}
	fmt.Printf("\n%d passed, %d failed\n", totalPassed, totalFailed)

	if junitOut != "" {
		f, err := os.Create(junitOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tln test: junit: %v\n", err)
			os.Exit(diagnostic.ExitError)
		}
		if err := testrunner.WriteJUnit(f, suites); err != nil {
			f.Close()
			fmt.Fprintf(os.Stderr, "tln test: junit: %v\n", err)
			os.Exit(diagnostic.ExitError)
		}
		if err := f.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "tln test: junit: %v\n", err)
			os.Exit(diagnostic.ExitError)
		}
	}

	if totalFailed > 0 {
		os.Exit(diagnostic.ExitError)
	}
}

type testPair struct {
	rules string
	test  string
}

// resolveTestPairs maps the user-supplied paths to rules/test file pairs.
// Two positional args of the form <rules.tln> <tests.tln.test> are paired
// directly so the legacy CLI keeps working. Otherwise each path is treated as
// a directory or `.tln.test` file; rules files are matched to tests by base
// name (`foo.tln` ↔ `foo.tln.test`).
func resolveTestPairs(paths []string) ([]testPair, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	if len(paths) == 2 {
		a, b := paths[0], paths[1]
		ca, cb := classifyTlnPath(a), classifyTlnPath(b)
		if ca == "rules" && cb == "test" {
			return []testPair{{rules: a, test: b}}, nil
		}
		if ca == "test" && cb == "rules" {
			return []testPair{{rules: b, test: a}}, nil
		}
	}

	var rulesFiles, testFiles []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			switch classifyTlnPath(p) {
			case "rules":
				rulesFiles = append(rulesFiles, p)
			case "test":
				testFiles = append(testFiles, p)
			default:
				return nil, fmt.Errorf("not a .tln or .tln.test file: %s", p)
			}
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			switch classifyTlnPath(path) {
			case "rules":
				rulesFiles = append(rulesFiles, path)
			case "test":
				testFiles = append(testFiles, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	rulesByBase := map[string]string{}
	for _, r := range rulesFiles {
		base := strings.TrimSuffix(filepath.Base(r), ".tln")
		rulesByBase[base] = r
	}

	var pairs []testPair
	for _, t := range testFiles {
		base := strings.TrimSuffix(filepath.Base(t), ".tln.test")
		r, ok := rulesByBase[base]
		if !ok {
			sibling := strings.TrimSuffix(t, ".test")
			if _, err := os.Stat(sibling); err == nil {
				r = sibling
			} else {
				return nil, fmt.Errorf("no rules file found for %s (need %s.tln nearby)", t, base)
			}
		}
		pairs = append(pairs, testPair{rules: r, test: t})
	}
	return pairs, nil
}

func classifyTlnPath(p string) string {
	switch {
	case strings.HasSuffix(p, ".tln.test"):
		return "test"
	case strings.HasSuffix(p, ".tln"):
		return "rules"
	}
	return ""
}

func runTestPair(rulesPath, testPath, filter string, verbose bool) ([]testrunner.TestResult, bool) {
	rulesSrc, err := os.ReadFile(rulesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln test: %v\n", err)
		return nil, false
	}
	rulesFile := filepath.Base(rulesPath)
	plans, ok := compile(rulesFile, rulesPath, string(rulesSrc))
	if !ok {
		return nil, false
	}

	// Re-parse rules so the testrunner can see ML-tuning clauses (`tune
	// against test`) on detect blocks alongside the test fixtures. Without
	// the merge, computeTunings sees only TestBlocks and skips ABC.
	rulesTokens, _ := lexer.Lex(rulesFile, string(rulesSrc))
	rulesProg, _ := parser.Parse(rulesFile, rulesTokens)

	testSrc, err := os.ReadFile(testPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln test: %v\n", err)
		return nil, false
	}
	testFile := filepath.Base(testPath)
	testTokens, tld := lexer.Lex(testFile, string(testSrc))
	if tld.HasErrors() {
		for _, d := range tld {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		return nil, false
	}
	testProg, tpd := parser.Parse(testFile, testTokens)
	if tpd.HasErrors() {
		for _, d := range tpd {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		return nil, false
	}
	// Validate against the merged program: checking a did / did_not verb needs
	// the rule blocks, which the test program alone does not carry.
	merged := *rulesProg
	merged.Blocks = append(merged.Blocks, testProg.Blocks...)
	if tvd := testrunner.Validate(&merged, plans); tvd.HasErrors() {
		for _, d := range tvd {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		return nil, false
	}

	results := testrunner.Run(&merged, plans)
	filtered := testrunner.FilterByName(results, filter)
	fmt.Printf("==> %s: %d test(s)\n", testFile, len(filtered))
	testrunner.PrintResults(filtered, verbose)
	return filtered, true
}

func runExecute() {
	// Parse args: tln run <file.tln> [--seed file.tln.test]
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: tln run <file.tln> [--seed file.tln.test]")
		os.Exit(diagnostic.ExitUsage)
	}

	path := os.Args[2]
	// If the project has been bundled (`tln bundle`), run through the
	// project-local binary so declared plugins are loaded (forwarding flags like
	// --seed). Exits on completion.
	maybeExecBundle(path, os.Args[3:])
	seedPath := ""
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--seed" && i+1 < len(os.Args) {
			seedPath = os.Args[i+1]
			i++
		}
	}

	// Compile
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln run: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}

	file := filepath.Base(path)
	plans, ok := compile(file, path, string(src))
	if !ok {
		os.Exit(diagnostic.ExitError)
	}

	ctx := context.Background()

	// Non-bundled runs use the in-process store. A program that needs a real
	// backing store declares one in config/store.tln and runs via `tln bundle`,
	// which loads the store plugin (e.g. tln-datalevin). See ADR 0013.
	store := factstore.NewMemoryStore()

	exec := executor.NewExecutor(store)

	// Seed from .tln.test file if requested
	if seedPath != "" {
		seedSrc, err := os.ReadFile(seedPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tln run: %v\n", err)
			os.Exit(diagnostic.ExitError)
		}
		seedFile := filepath.Base(seedPath)
		seedTokens, sld := lexer.Lex(seedFile, string(seedSrc))
		if sld.HasErrors() {
			for _, d := range sld {
				fmt.Fprintf(os.Stderr, "error: %s\n", d)
			}
			os.Exit(diagnostic.ExitError)
		}
		seedProg, spd := parser.Parse(seedFile, seedTokens)
		if spd.HasErrors() {
			for _, d := range spd {
				fmt.Fprintf(os.Stderr, "error: %s\n", d)
			}
			os.Exit(diagnostic.ExitError)
		}
		n, err := exec.Seed(ctx, seedProg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tln run: seed: %v\n", err)
			os.Exit(diagnostic.ExitError)
		}
		fmt.Printf("==> seeded %d entity(s) from %s\n", n, seedFile)
	}

	// Execute all plans
	results, err := exec.RunAll(ctx, plans)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln run: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}

	// Resolve entity names for readable output
	entityNames, resolveErr := exec.ResolveNames(ctx, nil)
	if resolveErr != nil {
		fmt.Fprintf(os.Stderr, "warn: could not resolve names: %v\n", resolveErr)
	}

	// Print results
	fmt.Printf("==> %s: %d block(s) executed\n", file, len(results))

	blockNames := make([]string, 0, len(results))
	for name := range results {
		blockNames = append(blockNames, name)
	}
	sort.Strings(blockNames)

	for _, name := range blockNames {
		r := results[name]
		fmt.Printf("\n  [%s] %d row(s) matched\n", r.BlockName, len(r.Flagged))
		for _, row := range r.Flagged {
			if len(row) == 0 {
				continue
			}
			eid, _ := row[0].(float64)
			ename := entityNames[int(eid)]
			if ename == "" {
				ename = fmt.Sprintf("entity %d", int(eid))
			}
			if len(row) == 1 {
				fmt.Printf("    - %s\n", ename)
			} else {
				// Include extra columns (e.g. km, last_service_km)
				extras := make([]string, 0, len(row)-1)
				for _, v := range row[1:] {
					extras = append(extras, fmt.Sprintf("%v", v))
				}
				fmt.Printf("    - %s (%s)\n", ename, strings.Join(extras, ", "))
			}
		}
	}
}

func runTrace() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: tln trace <rules.tln> <tests.tln.test> [--test NAME]")
		os.Exit(diagnostic.ExitUsage)
	}

	rulesPath := os.Args[2]
	testPath := os.Args[3]
	wantTest := ""
	for i := 4; i < len(os.Args); i++ {
		if os.Args[i] == "--test" && i+1 < len(os.Args) {
			wantTest = os.Args[i+1]
			i++
		}
	}

	rulesSrc, err := os.ReadFile(rulesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln trace: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}
	rulesFile := filepath.Base(rulesPath)
	plans, ok := compile(rulesFile, rulesPath, string(rulesSrc))
	if !ok {
		os.Exit(diagnostic.ExitError)
	}

	testSrc, err := os.ReadFile(testPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln trace: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}
	testFile := filepath.Base(testPath)
	testTokens, tld := lexer.Lex(testFile, string(testSrc))
	if tld.HasErrors() {
		for _, d := range tld {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		os.Exit(diagnostic.ExitError)
	}
	testProg, tpd := parser.Parse(testFile, testTokens)
	if tpd.HasErrors() {
		for _, d := range tpd {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		os.Exit(diagnostic.ExitError)
	}
	// Re-parse the rules so trace sees `tune against test` clauses.
	rulesTokens, _ := lexer.Lex(rulesFile, string(rulesSrc))
	rulesProg, _ := parser.Parse(rulesFile, rulesTokens)
	mergedTrace := *rulesProg
	mergedTrace.Blocks = append(mergedTrace.Blocks, testProg.Blocks...)
	// Validate against the merged program: checking a did / did_not verb needs
	// the rule blocks, which the test program alone does not carry.
	if tvd := testrunner.Validate(&mergedTrace, plans); tvd.HasErrors() {
		for _, d := range tvd {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		os.Exit(diagnostic.ExitError)
	}
	traces := testrunner.Trace(&mergedTrace, plans)
	if wantTest != "" {
		filtered := traces[:0]
		for _, t := range traces {
			if t.Name == wantTest {
				filtered = append(filtered, t)
			}
		}
		traces = filtered
		if len(traces) == 0 {
			fmt.Fprintf(os.Stderr, "tln trace: no test matched %q\n", wantTest)
			os.Exit(diagnostic.ExitError)
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"traces": traces}); err != nil {
		fmt.Fprintf(os.Stderr, "tln trace: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}
}

// compile runs lex → parse → resolve imports → validate → plan and
// returns plans. The `label` argument is used as the diagnostic label
// (usually the basename); the `basePath` argument is the file's
// on-disk path used to resolve relative imports against. Callers
// without a real path can pass the label as both.
func compile(label, basePath, src string) (map[string]*planner.QueryPlan, bool) {
	var allDiags diagnostic.List

	tokens, ld := lexer.Lex(label, src)
	allDiags = append(allDiags, ld...)

	prog, pd := parser.Parse(label, tokens)
	allDiags = append(allDiags, pd...)

	if len(prog.Imports) > 0 {
		merged, importDiags := imports.Resolve(prog, basePath, filepath.SplitList(os.Getenv("TLN_PATH"))...)
		allDiags = append(allDiags, importDiags...)
		if !importDiags.HasErrors() {
			prog = merged
		}
	}

	vd := validator.Validate(label, prog)
	allDiags = append(allDiags, vd...)

	for _, d := range allDiags {
		if d.Severity == diagnostic.Error {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		} else if d.Severity == diagnostic.Warning {
			fmt.Fprintf(os.Stderr, "warn:  %s\n", d)
		}
	}
	if allDiags.HasErrors() {
		return nil, false
	}

	plans, planDiags := planner.Plan(prog)
	for _, d := range planDiags {
		fmt.Fprintf(os.Stderr, "error: %s\n", d)
	}
	if planDiags.HasErrors() {
		return nil, false
	}
	return plans, true
}

func printStep(num int, step planner.PlanStep) {
	switch s := step.(type) {
	case *planner.FactQuery:
		fmt.Printf("    step %d  FactQuery → %s\n", num, s.Into)
		// Render the structured query in Datalog form for readability.
		for _, line := range strings.Split(s.Query.String(), "\n") {
			fmt.Printf("            %s\n", line)
		}
	case *planner.GoComputation:
		if s.Input != "" {
			fmt.Printf("    step %d  GoComputation  %s(%s) → %s\n", num, s.Function, s.Input, s.Into)
		} else {
			fmt.Printf("    step %d  GoComputation  %s() → %s\n", num, s.Function, s.Into)
		}
	case *planner.Filter:
		fmt.Printf("    step %d  Filter         %s → %s\n", num, s.Input, s.Into)
	}
}

// runExplain renders Tier-1 explanations for every decision produced by
// the given rules + tests files. See docs/design/0003-explainability.md.
func runExplain() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: tln explain <rules.tln> <tests.tln.test> [--test NAME] [--json|--csv]")
		os.Exit(diagnostic.ExitUsage)
	}

	rulesPath := os.Args[2]
	testPath := os.Args[3]
	wantTest := ""
	asJSON := false
	asCSV := false
	for i := 4; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--test":
			if i+1 < len(os.Args) {
				wantTest = os.Args[i+1]
				i++
			}
		case "--json":
			asJSON = true
		case "--csv":
			asCSV = true
		}
	}

	rulesSrc, err := os.ReadFile(rulesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln explain: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}
	rulesFile := filepath.Base(rulesPath)
	plans, ok := compile(rulesFile, rulesPath, string(rulesSrc))
	if !ok {
		os.Exit(diagnostic.ExitError)
	}

	// Re-parse the rules to recover the AST for cross-block decision linking.
	rulesTokens, _ := lexer.Lex(rulesFile, string(rulesSrc))
	rulesProg, _ := parser.Parse(rulesFile, rulesTokens)

	testSrc, err := os.ReadFile(testPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln explain: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}
	testFile := filepath.Base(testPath)
	testTokens, tld := lexer.Lex(testFile, string(testSrc))
	if tld.HasErrors() {
		for _, d := range tld {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		os.Exit(diagnostic.ExitError)
	}
	testProg, tpd := parser.Parse(testFile, testTokens)
	if tpd.HasErrors() {
		for _, d := range tpd {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		os.Exit(diagnostic.ExitError)
	}

	// Merge: Decisions needs both the rule blocks and the test blocks.
	merged := *rulesProg
	merged.Blocks = append(merged.Blocks, testProg.Blocks...)

	decisions := testrunner.Decisions(&merged, plans)

	// Stable ordering by test name.
	names := make([]string, 0, len(decisions))
	for n := range decisions {
		if wantTest != "" && n != wantTest {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "tln explain: no decisions produced")
		os.Exit(diagnostic.ExitError)
	}

	if asJSON {
		out := map[string][]explain.Decision{}
		for _, n := range names {
			out[n] = decisions[n]
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}

	if asCSV {
		if err := writeDecisionsCSV(os.Stdout, names, decisions); err != nil {
			fmt.Fprintf(os.Stderr, "tln explain: csv: %v\n", err)
			os.Exit(diagnostic.ExitError)
		}
		return
	}

	for _, n := range names {
		ds := decisions[n]
		if len(ds) == 0 {
			continue
		}
		fmt.Printf("== %s ==\n", n)
		fmt.Println(explain.RenderAll(ds))
	}
}

// runWhy answers "why did <block> flag <entity>?" by walking the same
// Decision chain that `tln explain` materialises, then filtering it to
// the single (block, entity) pair the user asked about. Backward-chained
// debugging: instead of dumping every decision a rule file produces, the
// caller anchors on one observable outcome and gets just the evidence
// and upstream triggers that led to it.
//
// Reuses tln explain's compile+seed flow verbatim — see runExplain.
// The only divergence is the post-filter that keeps decisions where
// (BlockName matches if --block given) AND (EntityID matches if
// --entity given).
//
// Usage:
//
//	tln why <rules.tln> <tests.tln.test> [--block NAME] [--entity ID] [--test NAME] [--json]
func runWhy() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: tln why <rules.tln> <tests.tln.test> [--block NAME] [--entity ID] [--test NAME] [--json]")
		os.Exit(diagnostic.ExitUsage)
	}

	rulesPath := os.Args[2]
	testPath := os.Args[3]
	wantTest := ""
	wantBlock := ""
	wantEntity := -1
	asJSON := false
	for i := 4; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--test":
			if i+1 < len(os.Args) {
				wantTest = os.Args[i+1]
				i++
			}
		case "--block":
			if i+1 < len(os.Args) {
				wantBlock = os.Args[i+1]
				i++
			}
		case "--entity":
			if i+1 < len(os.Args) {
				n, err := strconv.Atoi(os.Args[i+1])
				if err != nil {
					fmt.Fprintf(os.Stderr, "tln why: --entity must be an integer, got %q\n", os.Args[i+1])
					os.Exit(diagnostic.ExitUsage)
				}
				wantEntity = n
				i++
			}
		case "--json":
			asJSON = true
		}
	}
	if wantBlock == "" && wantEntity < 0 {
		fmt.Fprintln(os.Stderr, "tln why: provide at least one of --block or --entity (a goal to chain backwards from)")
		os.Exit(diagnostic.ExitUsage)
	}

	rulesSrc, err := os.ReadFile(rulesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln why: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}
	rulesFile := filepath.Base(rulesPath)
	plans, ok := compile(rulesFile, rulesPath, string(rulesSrc))
	if !ok {
		os.Exit(diagnostic.ExitError)
	}
	rulesTokens, _ := lexer.Lex(rulesFile, string(rulesSrc))
	rulesProg, _ := parser.Parse(rulesFile, rulesTokens)

	testSrc, err := os.ReadFile(testPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tln why: %v\n", err)
		os.Exit(diagnostic.ExitError)
	}
	testFile := filepath.Base(testPath)
	testTokens, tld := lexer.Lex(testFile, string(testSrc))
	if tld.HasErrors() {
		for _, d := range tld {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		os.Exit(diagnostic.ExitError)
	}
	testProg, tpd := parser.Parse(testFile, testTokens)
	if tpd.HasErrors() {
		for _, d := range tpd {
			fmt.Fprintf(os.Stderr, "error: %s\n", d)
		}
		os.Exit(diagnostic.ExitError)
	}

	merged := *rulesProg
	merged.Blocks = append(merged.Blocks, testProg.Blocks...)
	decisions := testrunner.Decisions(&merged, plans)

	// Filter by --test name first (the testrunner keys decisions by test
	// block name), then by --block / --entity within each test's chain.
	filtered := map[string][]explain.Decision{}
	for testName, ds := range decisions {
		if wantTest != "" && testName != wantTest {
			continue
		}
		var matched []explain.Decision
		for _, d := range ds {
			if wantBlock != "" && d.BlockName != wantBlock {
				continue
			}
			if wantEntity >= 0 && d.EntityID != wantEntity {
				continue
			}
			matched = append(matched, d)
		}
		if len(matched) > 0 {
			filtered[testName] = matched
		}
	}

	if len(filtered) == 0 {
		// No match isn't an error — it's the answer ("nothing fired this
		// way"). Exit 1 so scripts can branch on it, but say so clearly.
		switch {
		case wantBlock != "" && wantEntity >= 0:
			fmt.Fprintf(os.Stderr, "tln why: no decision matched block %q on entity %d\n", wantBlock, wantEntity)
		case wantBlock != "":
			fmt.Fprintf(os.Stderr, "tln why: no decision matched block %q\n", wantBlock)
		default:
			fmt.Fprintf(os.Stderr, "tln why: no decision matched entity %d\n", wantEntity)
		}
		os.Exit(diagnostic.ExitError)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(filtered)
		return
	}

	names := make([]string, 0, len(filtered))
	for n := range filtered {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("== %s ==\n", n)
		fmt.Println(explain.RenderAll(filtered[n]))
	}
}

// writeDecisionsCSV emits one row per Decision in stable order so downstream
// tools (R, Python, DuckDB, BigQuery) can read tln results without parsing
// the nested JSON shape. Evidence is serialized as a key=value-joined string
// in a single column rather than exploded into a dynamic schema — that keeps
// the CSV stable when rules add or remove evidence keys between runs.
//
// Column order is fixed and documented at docs/optimizers/csv-export.md.
func writeDecisionsCSV(w io.Writer, names []string, decisions map[string][]explain.Decision) error {
	cw := csv.NewWriter(w)
	header := []string{
		"test", "block", "kind", "entity_id", "entity_name",
		"fired_at", "priority", "confidence", "action", "why", "evidence",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, name := range names {
		for _, d := range decisions[name] {
			row := []string{
				name,
				d.BlockName,
				d.BlockKind,
				strconv.Itoa(d.EntityID),
				d.EntityName,
				d.FiredAt.UTC().Format(time.RFC3339),
				d.Priority,
				d.Confidence,
				d.Action,
				strings.Join(d.Why, " | "),
				flattenEvidence(d.Evidence),
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

// flattenEvidence joins (attribute, value) pairs into a single "k1=v1; k2=v2"
// string. Keeps the CSV schema stable while preserving the underlying data.
// For JSON-faithful evidence, callers should use --json instead.
func flattenEvidence(facts []explain.Fact) string {
	parts := make([]string, 0, len(facts))
	for _, f := range facts {
		parts = append(parts, fmt.Sprintf("%s=%v", f.Attribute, f.Value))
	}
	return strings.Join(parts, "; ")
}

// stripGlobalLogFlags scans os.Args for the two log-control flags, applies
// them via internal/log.Init, and rewrites os.Args without them so each
// subcommand's own arg parser stays unchanged. Accepts both `--flag=value`
// and `--flag value` shapes. Unknown values fall back to the defaults
// (text format, warn level) with a one-line stderr complaint.
func stripGlobalLogFlags() {
	format := tlnlog.FormatText
	level := slog.LevelWarn

	out := make([]string, 0, len(os.Args))
	out = append(out, os.Args[0])

	consumeNext := false
	var consumeKind string
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if consumeNext {
			consumeNext = false
			applyLogFlag(consumeKind, arg, &format, &level)
			continue
		}
		switch {
		case strings.HasPrefix(arg, "--log-format="):
			applyLogFlag("format", strings.TrimPrefix(arg, "--log-format="), &format, &level)
		case arg == "--log-format":
			consumeNext, consumeKind = true, "format"
		case strings.HasPrefix(arg, "--log-level="):
			applyLogFlag("level", strings.TrimPrefix(arg, "--log-level="), &format, &level)
		case arg == "--log-level":
			consumeNext, consumeKind = true, "level"
		default:
			out = append(out, arg)
		}
	}
	os.Args = out
	tlnlog.Init(format, level, os.Stderr)
}

func applyLogFlag(kind, value string, format *tlnlog.Format, level *slog.Level) {
	switch kind {
	case "format":
		f, err := tlnlog.ParseFormat(value)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		*format = f
	case "level":
		l, err := tlnlog.ParseLevel(value)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		*level = l
	}
}
