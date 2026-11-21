// internal/upgrade/gate_test.go
package upgrade

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"forge"
)

func TestVersionMatchesEmbeddedFileHashes(t *testing.T) {
	t.Parallel()

	hashFile := filepath.Join("testdata", "pinned-hashes.txt")
	pinned, err := os.ReadFile(hashFile)
	if err != nil {
		// First run: generate the file
		t.Logf("generating %s for version %d", hashFile, Version)
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		content := generatePinnedHashes(t, forge.Assets())
		if err := os.WriteFile(hashFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	// Verify current hashes match pinned
	current := generatePinnedHashes(t, forge.Assets())
	if string(pinned) != current {
		t.Fatalf("managed file hashes changed but upgrade.Version (%d) was not bumped.\n"+
			"If you changed a managed template file, increment Version and run:\n"+
			"  go test ./internal/upgrade/ -run TestVersionMatchesEmbeddedFileHashes -count=1\n"+
			"to regenerate the pinned hashes.\n\nPinned:\n%s\nCurrent:\n%s",
			Version, string(pinned), current)
	}
}

func generatePinnedHashes(t *testing.T, assets fs.FS) string {
	t.Helper()
	var lines []string
	lines = append(lines, fmt.Sprintf("version: %d", Version))
	// Add-on owned files follow the managed files, add-ons in name order.
	files := slices.Clone(managedFiles)
	for _, name := range addonNames() {
		files = append(files, addonOwnedFiles[name]...)
	}
	for _, mf := range files {
		data, err := fs.ReadFile(assets, mf.src)
		if err != nil {
			t.Fatalf("read %s: %v", mf.src, err)
		}
		hash := sha256.Sum256(data)
		lines = append(lines, fmt.Sprintf("%s %x", mf.src, hash))
	}
	return strings.Join(lines, "\n") + "\n"
}
