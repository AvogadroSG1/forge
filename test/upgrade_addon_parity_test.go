package test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	forge "forge"
	"forge/internal/upgrade"
)

// readTree maps every regular file under dir (slash-separated, relative) to
// its content and mode.
func readTree(t *testing.T, dir string) map[string]treeFile {
	t.Helper()

	tree := map[string]treeFile{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(rel)] = treeFile{data: data, mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

type treeFile struct {
	data []byte
	mode os.FileMode
}

// TestUpgradeAgentOptInMatchesInitWithAgent proves ADR-0028's claim that
// `forge upgrade --agent pi` delivers what `forge init --agent pi` writes,
// except the stack overlay content upgrade never edits.
func TestUpgradeAgentOptInMatchesInitWithAgent(t *testing.T) {
	for _, tc := range piAddonStacks {
		t.Run(tc.stack, func(t *testing.T) {
			// Overlay content upgrade names in its notice instead of editing.
			overlay := map[string]bool{".github/workflows/ci.yml": true}
			if tc.stack == "go-api-chi" {
				overlay["internal/httpapi/router.go"] = true
			}

			upgraded := scaffoldPiAddonStack(t, tc.stack, tc.projectType, "")
			if err := upgrade.Stamp(upgraded, upgrade.Manifest{Language: "go", Stack: tc.stack}); err != nil {
				t.Fatalf("Stamp() error = %v", err)
			}
			status, err := upgrade.RunWith(forge.Assets(), upgraded, upgrade.Options{Agent: "pi"})
			if err != nil {
				t.Fatalf("RunWith() error = %v", err)
			}
			if len(status.Updated) != len(piAddonCommonFiles)+len(tc.wiring) {
				t.Errorf("Updated = %v, want the %d add-on files", status.Updated, len(piAddonCommonFiles)+len(tc.wiring))
			}

			scaffolded := scaffoldPiAddonStack(t, tc.stack, tc.projectType, "pi")
			if err := upgrade.Stamp(scaffolded, upgrade.Manifest{Language: "go", Stack: tc.stack, Agent: "pi"}); err != nil {
				t.Fatalf("Stamp() error = %v", err)
			}

			got, want := readTree(t, upgraded), readTree(t, scaffolded)
			var diffs []string
			for path, w := range want {
				g, ok := got[path]
				switch {
				case overlay[path]:
					if ok && bytes.Equal(g.data, w.data) {
						t.Errorf("%s is expected to differ (overlay content upgrade does not edit) but is identical", path)
					}
				case !ok:
					diffs = append(diffs, "missing after upgrade: "+path)
				case !bytes.Equal(g.data, w.data):
					diffs = append(diffs, "content differs: "+path)
				case g.mode != w.mode:
					diffs = append(diffs, "mode differs: "+path)
				}
			}
			for path := range got {
				if _, ok := want[path]; !ok {
					diffs = append(diffs, "extra after upgrade: "+path)
				}
			}
			sort.Strings(diffs)
			for _, d := range diffs {
				t.Error(d)
			}
		})
	}
}

// optInWithCustomWiring scaffolds stack without the add-on, adds a custom
// agent source file, and runs the opt-in. It returns the repo and its tree
// from before the run.
func optInWithCustomWiring(t *testing.T, tc struct {
	stack       string
	projectType string
	wiring      []string
}, customData string) (dir string, before map[string]treeFile) {
	t.Helper()

	dir = scaffoldPiAddonStack(t, tc.stack, tc.projectType, "")
	if err := upgrade.Stamp(dir, upgrade.Manifest{Language: "go", Stack: tc.stack}); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	custom := filepath.Join(dir, filepath.FromSlash(tc.wiring[0]))
	if err := os.WriteFile(custom, []byte(customData), 0o644); err != nil {
		t.Fatal(err)
	}
	before = readTree(t, dir)
	if _, err := upgrade.RunWith(forge.Assets(), dir, upgrade.Options{Agent: "pi"}); err != nil {
		t.Fatalf("RunWith() error = %v", err)
	}
	return dir, before
}

// Own agent code that compiles in the stack's package and shares no name with
// forge's wiring.
var customAgentSource = map[string]string{
	"go-cli-cobra": "package cmd\n\nimport \"github.com/spf13/cobra\"\n\nfunc init() {\n\trootCmd.AddCommand(&cobra.Command{Use: \"agent\", Short: \"my own agent command\"})\n}\n",
	"go-api-chi":   "package httpapi\n\n// MyAgent is the repo's own agent handler.\nfunc MyAgent() string { return \"mine\" }\n",
}

// A repo with its own agent source keeps it, and forge does not install the
// test that exercises forge's implementation: that pair would not compile.
func TestUpgradeAgentOptInKeepsCustomWiringAndSkipsItsTest(t *testing.T) {
	for _, tc := range piAddonStacks {
		t.Run(tc.stack, func(t *testing.T) {
			dir, before := optInWithCustomWiring(t, tc, customAgentSource[tc.stack])
			after := readTree(t, dir)

			src, test := tc.wiring[0], tc.wiring[1]
			if !bytes.Equal(after[src].data, []byte(customAgentSource[tc.stack])) {
				t.Errorf("%s changed:\n%s", src, after[src].data)
			}
			if _, ok := after[test]; ok {
				t.Errorf("%s installed next to a custom %s", test, src)
			}
			for path, f := range before {
				if path == ".forge/manifest.json" {
					continue // records the add-on
				}
				if got, ok := after[path]; !ok || !bytes.Equal(got.data, f.data) {
					t.Errorf("%s was changed or removed by the opt-in", path)
				}
			}
			for _, path := range piAddonCommonFiles {
				if _, ok := after[path]; !ok {
					t.Errorf("%s missing: a kept group must not hold back the rest of the add-on", path)
				}
			}
			assertWiringTestsDeclared(t, dir, filepath.Dir(src))
		})
	}
}

// wiringSymbols are the package-level names forge's agent tests and sources
// refer to across files.
var wiringSymbols = []string{"agentRun", "agentCmd", "rootCmd", "RootCommand", "AgentPrompt", "AgentRunner", "RunPi", "NewRouter", "mountAgent"}

// assertWiringTestsDeclared is the structural stand-in for compiling the
// generated repo: a scaffolded api repo has no go.sum until `go mod tidy`
// reaches the module proxy, so the default test run cannot build it offline.
// It parses the package in dir and requires every wiring symbol a _test.go
// file uses to be declared by a non-test file of the same package.
func assertWiringTestsDeclared(t *testing.T, repo, dir string) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(repo, filepath.FromSlash(dir)))
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	used := map[string][]string{} // symbol -> test files using it
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(repo, filepath.FromSlash(dir), e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s/%s: %v", dir, e.Name(), err)
		}
		if !strings.HasSuffix(e.Name(), "_test.go") {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil {
						declared[d.Name.Name] = true
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.ValueSpec:
							for _, n := range s.Names {
								declared[n.Name] = true
							}
						case *ast.TypeSpec:
							declared[s.Name.Name] = true
						}
					}
				}
			}
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				ast.Inspect(sel.X, func(m ast.Node) bool { return recordUse(m, e.Name(), used) })
				return false
			}
			return recordUse(n, e.Name(), used)
		})
	}
	for _, sym := range wiringSymbols {
		if files, ok := used[sym]; ok && !declared[sym] {
			t.Errorf("%s uses %s, which no non-test file in %s declares", strings.Join(files, ", "), sym, dir)
		}
	}
}

func recordUse(n ast.Node, file string, used map[string][]string) bool {
	if id, ok := n.(*ast.Ident); ok && slices.Contains(wiringSymbols, id.Name) && !slices.Contains(used[id.Name], file) {
		used[id.Name] = append(used[id.Name], file)
	}
	return true
}

// The tree under testdata/go-api-chi-pre-slog is router.go.tmpl from c3ceafc
// (NewRouter(logger *log.Logger)), which has no template actions.
func TestUpgradeAgentOptInRefusesAnApiRepoThatPredatesSlog(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("testdata", "go-api-chi-pre-slog", "router.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	dir := scaffoldPiAddonStack(t, "go-api-chi", "api", "")
	if err := os.WriteFile(filepath.Join(dir, "internal", "httpapi", "router.go"), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upgrade.Stamp(dir, upgrade.Manifest{Language: "go", Stack: "go-api-chi"}); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	before := readTree(t, dir)

	_, err = upgrade.RunWith(forge.Assets(), dir, upgrade.Options{Agent: "pi"})
	if err == nil {
		t.Fatal("RunWith() succeeded on a router that takes *log.Logger")
	}
	for _, want := range []string{"NewRouter", "*slog.Logger", "log/slog"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	after := readTree(t, dir)
	if len(after) != len(before) {
		t.Errorf("tree has %d files after the refusal, want %d", len(after), len(before))
	}
	for path, f := range before {
		if got, ok := after[path]; !ok || !bytes.Equal(got.data, f.data) || got.mode != f.mode {
			t.Errorf("%s changed by a refused opt-in", path)
		}
	}
}
