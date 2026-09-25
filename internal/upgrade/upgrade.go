// Package upgrade propagates managed static infrastructure files from the
// embedded template into existing forge-scaffolded repositories.
package upgrade

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"forge/internal/allowlist"
	"forge/internal/hookcfg"
)

const Version = 6

const versionFile = ".forge-infra-version"

type managedFile struct {
	src  string
	dest string
	mode os.FileMode
}

// managedFiles is the full set of statically-copied managed files, in
// canonical order. It exists only for gate_test.go's version-hash pin; Run
// itself writes wholly-owned and co-owned files through the two slices
// below, which own the write-path behavior for each entry.
var managedFiles = []managedFile{
	{src: "templates/common/claude/hooks/guard", dest: ".claude/hooks/guard", mode: 0o755},
	{src: "templates/common/claude/hooks/secret-scan.sh", dest: ".claude/hooks/secret-scan.sh", mode: 0o755},
	{src: "templates/common/claude/settings.json", dest: ".claude/settings.json", mode: 0o644},
	{src: "templates/common/codex/hooks.json", dest: ".codex/hooks.json", mode: 0o644},
	{src: "templates/common/opencode/plugins/forge-hooks.js", dest: ".opencode/plugins/forge-hooks.js", mode: 0o644},
	{src: "templates/common/mise/conf.d/otel.toml", dest: ".config/mise/conf.d/otel.toml", mode: 0o644},
	{src: "templates/common/otel/compose.yaml", dest: ".otel/compose.yaml", mode: 0o644},
	{src: "templates/common/otel/collector.yaml", dest: ".otel/collector.yaml", mode: 0o644},
}

// whollyOwnedFiles are managed files nothing but forge ever writes to: a
// blind byte-copy from the embedded template is always safe.
var whollyOwnedFiles = []managedFile{
	{src: "templates/common/claude/hooks/guard", dest: ".claude/hooks/guard", mode: 0o755},
	{src: "templates/common/claude/hooks/secret-scan.sh", dest: ".claude/hooks/secret-scan.sh", mode: 0o755},
	{src: "templates/common/opencode/plugins/forge-hooks.js", dest: ".opencode/plugins/forge-hooks.js", mode: 0o644},
	{src: "templates/common/mise/conf.d/otel.toml", dest: ".config/mise/conf.d/otel.toml", mode: 0o644},
	{src: "templates/common/otel/compose.yaml", dest: ".otel/compose.yaml", mode: 0o644},
	{src: "templates/common/otel/collector.yaml", dest: ".otel/collector.yaml", mode: 0o644},
}

// coOwnedFiles are managed files other tools also append entries into (bd's
// hook groups, notably). They are reconciled by owned-entry identity via
// internal/hookcfg rather than blind-copied (ADR-0016).
var coOwnedFiles = []managedFile{
	{src: "templates/common/claude/settings.json", dest: ".claude/settings.json", mode: 0o644},
	{src: "templates/common/codex/hooks.json", dest: ".codex/hooks.json", mode: 0o644},
}

// defaultRegistry declares forge's historical hook-entry command forms for
// hookcfg reconciliation, so a repo still carrying an old shipped form
// converges to the current canonical command instead of surviving as a
// stale duplicate alongside it. Keep this small; it is the place future
// forge-owned command-string renames land.
var defaultRegistry = hookcfg.Registry{
	Historical: map[string][]string{
		"bd prime || true": {"bd prime"},
	},
}

type Status struct {
	Version       int
	OnDisk        int
	Stale         bool
	Updated       []string
	PermsDrift    bool
	PermsRepaired bool
}

func Run(assets fs.FS, targetDir string, checkOnly bool) (Status, error) {
	if err := validateForgeRepo(targetDir); err != nil {
		return Status{}, err
	}

	onDisk := readOnDiskVersion(targetDir)
	status := Status{
		Version: Version,
		OnDisk:  onDisk,
		Stale:   onDisk < Version,
	}

	if checkOnly {
		status.PermsDrift = beadsPermsDrifted(targetDir)
		return status, nil
	}

	if !status.Stale {
		return status, nil
	}

	for _, mf := range whollyOwnedFiles {
		if err := writeManagedFile(assets, targetDir, mf); err != nil {
			return Status{}, err
		}
		status.Updated = append(status.Updated, mf.dest)
	}

	for _, mf := range coOwnedFiles {
		changed, err := reconcileOrWriteCoOwned(assets, targetDir, mf)
		if err != nil {
			return Status{}, err
		}
		if changed {
			status.Updated = append(status.Updated, mf.dest)
		}
	}

	// Read the manifest once: used for opencode render-if-missing below, and
	// (when absent) drives the backfill-by-inference decision further down.
	// Errors here are informational only — a missing/unreadable manifest is
	// the ordinary legacy-repo case, not a failure.
	m, manifestErr := ReadManifest(targetDir)
	if manifestErr == nil {
		rendered, err := renderOpencodeIfMissing(assets, targetDir, m)
		if err != nil {
			return Status{}, err
		}
		if rendered {
			status.Updated = append(status.Updated, "opencode.jsonc")
		}
	}

	repaired, err := EnsureBeadsDirPerms(targetDir)
	if err != nil {
		return Status{}, err
	}
	status.PermsRepaired = repaired

	// Version stamping happens last so an interrupted upgrade re-applies.
	// A repo with an existing manifest carries its params forward unchanged
	// while infraVersion advances. A legacy repo with no manifest yet is
	// best-effort backfilled by inferring params from its managed allowlist
	// block (ADR-0017 Consequences); when inference is impossible, the
	// upgrade still succeeds — it stamps the bare legacy marker and prints a
	// one-line notice, since params must never be guessed.
	if manifestErr != nil {
		backfilled, ok := backfillManifestFromAllowlist(targetDir)
		if ok {
			if err := Stamp(targetDir, backfilled); err != nil {
				return Status{}, err
			}
			return status, nil
		}
		if err := writeLegacyMarker(targetDir); err != nil {
			return Status{}, err
		}
		fmt.Fprintln(os.Stderr, "forge upgrade: could not infer project params from .claude/settings.local.json; run `forge init`-recorded params are unavailable for this repo — create .forge/manifest.json to enable full upgrade-management")
		return status, nil
	}

	if err := Stamp(targetDir, m); err != nil {
		return Status{}, err
	}

	return status, nil
}

// writeManagedFile blind byte-copies a wholly-owned managed file from the
// embedded template, creating parent directories and fixing permissions as
// needed.
func writeManagedFile(assets fs.FS, targetDir string, mf managedFile) error {
	data, err := fs.ReadFile(assets, mf.src)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", mf.src, err)
	}
	dest := filepath.Join(targetDir, filepath.FromSlash(mf.dest))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", mf.dest, err)
	}
	if err := os.WriteFile(dest, data, mf.mode); err != nil {
		return fmt.Errorf("write %s: %w", mf.dest, err)
	}
	if err := os.Chmod(dest, mf.mode); err != nil {
		return fmt.Errorf("chmod %s: %w", mf.dest, err)
	}
	return nil
}

// reconcileOrWriteCoOwned writes a co-owned file. When the destination does
// not exist yet, the embedded template is written directly (there is
// nothing to reconcile against). When it does exist, it is merged through
// hookcfg.Reconcile so third-party entries survive untouched; a malformed
// on-disk file fails loud and is left unmodified (ADR-0016).
func reconcileOrWriteCoOwned(assets fs.FS, targetDir string, mf managedFile) (changed bool, err error) {
	data, err := fs.ReadFile(assets, mf.src)
	if err != nil {
		return false, fmt.Errorf("read embedded %s: %w", mf.src, err)
	}
	dest := filepath.Join(targetDir, filepath.FromSlash(mf.dest))

	existing, err := os.ReadFile(dest)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", mf.dest, err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return false, fmt.Errorf("create directory for %s: %w", mf.dest, err)
		}
		if err := os.WriteFile(dest, data, mf.mode); err != nil {
			return false, fmt.Errorf("write %s: %w", mf.dest, err)
		}
		if err := os.Chmod(dest, mf.mode); err != nil {
			return false, fmt.Errorf("chmod %s: %w", mf.dest, err)
		}
		return true, nil
	}

	out, reconciled, err := hookcfg.Reconcile(existing, data, defaultRegistry)
	if err != nil {
		return false, fmt.Errorf("reconcile %s: %w", mf.dest, err)
	}
	if !reconciled {
		return false, nil
	}
	if err := os.WriteFile(dest, out, mf.mode); err != nil {
		return false, fmt.Errorf("write %s: %w", mf.dest, err)
	}
	if err := os.Chmod(dest, mf.mode); err != nil {
		return false, fmt.Errorf("chmod %s: %w", mf.dest, err)
	}
	return true, nil
}

// renderOpencodeIfMissing renders templates/common/opencode.jsonc.tmpl using
// m's params and writes it to <targetDir>/opencode.jsonc, but only when that
// file does not already exist — it is co-owned and hand-edited once
// present, so upgrade never touches it again.
func renderOpencodeIfMissing(assets fs.FS, targetDir string, m Manifest) (bool, error) {
	dest := filepath.Join(targetDir, "opencode.jsonc")
	if _, err := os.Stat(dest); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat opencode.jsonc: %w", err)
	}

	data, err := fs.ReadFile(assets, "templates/common/opencode.jsonc.tmpl")
	if err != nil {
		return false, fmt.Errorf("read embedded opencode.jsonc.tmpl: %w", err)
	}

	tmpl, err := template.New("opencode.jsonc.tmpl").Option("missingkey=error").Parse(string(data))
	if err != nil {
		return false, fmt.Errorf("parse opencode.jsonc.tmpl: %w", err)
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct {
		Language        string
		Frontend        string
		IncludePersonal bool
	}{
		Language:        m.Language,
		Frontend:        m.Frontend,
		IncludePersonal: m.IncludePersonal,
	}); err != nil {
		return false, fmt.Errorf("render opencode.jsonc.tmpl: %w", err)
	}

	if err := os.WriteFile(dest, rendered.Bytes(), 0o644); err != nil {
		return false, fmt.Errorf("write opencode.jsonc: %w", err)
	}
	return true, nil
}

// backfillManifestFromAllowlist attempts to reconstruct init params for a
// legacy repo (marker only, no manifest) by inferring them from its managed
// allowlist block in .claude/settings.local.json. It never guesses: any
// missing file or inference failure reports ok=false rather than a
// half-populated manifest. includePersonal is never inferable and always
// defaults false.
func backfillManifestFromAllowlist(targetDir string) (Manifest, bool) {
	data, err := os.ReadFile(filepath.Join(targetDir, ".claude", "settings.local.json"))
	if err != nil {
		return Manifest{}, false
	}
	contents := string(data)

	lang, err := allowlist.InferLanguage(contents)
	if err != nil {
		return Manifest{}, false
	}

	frontend := ""
	if allowlist.InferFrontend(contents) {
		frontend = "frontend"
	}

	return Manifest{
		Language:        lang,
		Frontend:        frontend,
		IncludePersonal: false,
	}, true
}

// writeLegacyMarker writes only .forge-infra-version, for repos with no
// manifest and uninferable params. It does not create .forge/manifest.json,
// since this path must not fabricate init params for pre-existing repos.
func writeLegacyMarker(targetDir string) error {
	versionContent := fmt.Sprintf("%d\n", Version)
	versionPath := filepath.Join(targetDir, versionFile)
	if err := os.WriteFile(versionPath, []byte(versionContent), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", versionFile, err)
	}
	return nil
}

func validateForgeRepo(targetDir string) error {
	markers := []string{
		filepath.Join(targetDir, ".claude", "hooks", "guard"),
		filepath.Join(targetDir, ".forge-infra-version"),
		filepath.Join(targetDir, ".claude", "settings.json"),
		filepath.Join(targetDir, ".opencode", "plugins", "forge-hooks.js"),
	}
	for _, marker := range markers {
		if _, err := os.Stat(marker); err == nil {
			return nil
		}
	}
	return fmt.Errorf("not a forge-managed repository (run from a project created with forge init)")
}

func readOnDiskVersion(targetDir string) int {
	if m, err := ReadManifest(targetDir); err == nil {
		return m.InfraVersion
	}

	data, err := os.ReadFile(filepath.Join(targetDir, versionFile))
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return v
}
