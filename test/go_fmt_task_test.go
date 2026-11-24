package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// goFmtFind captures the find(1) expression that selects the files a Go
// stack's fmt task hands to gofmt.
var goFmtFind = regexp.MustCompile(`gofmt -w \$\((find [^)]+)\)`)

// TestGoStackFmtTaskSelectsOnlyModuleSources guards the fmt gate's reach.
// instill installs skills whose example .go files land in hidden agent
// directories and apm_modules/; formatting them would rewrite files the repo
// does not own on every commit.
func TestGoStackFmtTaskSelectsOnlyModuleSources(t *testing.T) {
	for _, tc := range goContainerStacks {
		t.Run(tc.stack, func(t *testing.T) {
			match := goFmtFind.FindStringSubmatch(readGoContainerAsset(t, tc.stack, tc.miseFile))
			if match == nil {
				t.Fatalf("%s fmt task has no `gofmt -w $(find ...)` selection", tc.miseFile)
			}

			dir := t.TempDir()
			for _, rel := range []string{
				"main.go",
				"cmd/root.go",
				"internal/app/app.go",
				".git/hooks/sample.go",
				".agents/skills/golang-cli/assets/examples/main.go",
				".claude/skills/golang-cli/assets/examples/main.go",
				"apm_modules/_local/golang-cli/assets/examples/main.go",
			} {
				path := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.Command("sh", "-c", match[1])
			cmd.Dir = dir
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s: %v", match[1], err)
			}

			got := strings.Fields(string(out))
			slices.Sort(got)
			want := []string{"./cmd/root.go", "./internal/app/app.go", "./main.go"}
			if !slices.Equal(got, want) {
				t.Errorf("fmt selects %v, want %v", got, want)
			}
		})
	}
}
