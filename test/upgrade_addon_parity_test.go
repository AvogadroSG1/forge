package test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
