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

const Version = 12

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

// Run is RunWith without an add-on request.
func Run(assets fs.FS, targetDir string, checkOnly bool) (Status, error) {
	return RunWith(assets, targetDir, Options{CheckOnly: checkOnly})
}

// RunWith upgrades targetDir. An opts.Agent request is validated before any
// write; see ADR-0028.
func RunWith(assets fs.FS, targetDir string, opts Options) (Status, error) {
	if err := validateForgeRepo(targetDir); err != nil {
		return Status{}, err
	}

	var optIn *addonOptIn
	if opts.Agent != "" {
		var err error
		if optIn, err = prepareAddonOptIn(targetDir, opts); err != nil {
			return Status{}, err
		}
	}

	onDisk := readOnDiskVersion(targetDir)
	status := Status{
		Version: Version,
		OnDisk:  onDisk,
		Stale:   onDisk < Version,
	}

	if opts.CheckOnly {
		status.PermsDrift = beadsPermsDrifted(targetDir)
		return status, nil
	}

	if !status.Stale && optIn == nil {
		return status, nil
	}

	if status.Stale {
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
	}

	// Read the manifest once: used for opencode render-if-missing below, and
	// (when absent) drives the backfill-by-inference decision further down.
	// Errors here are informational only — a missing/unreadable manifest is
	// the ordinary legacy-repo case, not a failure.
	m, manifestErr := ReadManifest(targetDir)
	if status.Stale && manifestErr == nil {
		rendered, err := renderOpencodeIfMissing(assets, targetDir, m)
		if err != nil {
			return Status{}, err
		}
		if rendered {
			status.Updated = append(status.Updated, "opencode.jsonc")
		}

		added, err := addContainerFilesIfMissing(assets, targetDir, m)
		if err != nil {
			return Status{}, err
		}
		if len(added) > 0 {
			status.Updated = append(status.Updated, added...)
			fmt.Fprintln(os.Stderr, containerStacks[m.Stack])
		}
	}

	// The add-on step (ADR-0028) runs before Stamp, so a failure leaves the
	// manifest unchanged and a re-run completes it.
	if manifestErr == nil {
		if err := applyAddon(assets, targetDir, m, optIn, &status); err != nil {
			return Status{}, err
		}
	}

	if status.Stale {
		repaired, err := EnsureBeadsDirPerms(targetDir)
		if err != nil {
			return Status{}, err
		}
		status.PermsRepaired = repaired
	}

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

	if optIn != nil {
		m.Agent = optIn.agent
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

// containerFiles are the static container files every container-pattern stack
// ships in its overlay (Go, ADR-0023; Python, ADR-0026). upgrade delivers them
// add-if-missing; they are hand-edited once present, so they are never part of
// managedFiles.
var containerFiles = []string{"Dockerfile", ".dockerignore", "CONTAINER.md"}

// containerStacks maps each stack whose overlay carries containerFiles to the
// notice printed when upgrade adds them. The notice is chosen by stack key,
// not by manifest Language.
var containerStacks = map[string]string{
	"go-cli-cobra": goContainerNotice,
	"go-api-chi":   goContainerNotice,
	"go-web-templ": goContainerNotice,

	"python-cli-typer": pythonContainerNotice,
	"python-fastapi":   pythonContainerNotice,
	"python-web-jinja": pythonContainerNotice,
}

const goContainerNotice = "forge upgrade: added Dockerfile, .dockerignore and CONTAINER.md for the Go container image pattern (ADR-0023).\n" +
	"  Not touched by upgrade (stack overlay content): the `image`, `image:lint` and `image:scan` mise tasks, the hadolint and trivy tool pins,\n" +
	"  the CI image scan step, and the PORT / slog changes in the application code.\n" +
	"  Align the Go and golangci-lint (and templ) versions in the Dockerfile with this repo's mise.toml before building; see CONTAINER.md."

const pythonContainerNotice = "forge upgrade: added Dockerfile, .dockerignore and CONTAINER.md for the Python container image pattern (ADR-0026).\n" +
	"  Upgrade did not change pyproject.toml, mise.toml, CI or application code.\n" +
	"  The Dockerfile builds only once the repo has a uv.lock (run `uv lock`) and an `app` console script in [project.scripts];\n" +
	"  align the Python, uv, ruff and pyright versions in the Dockerfile with this repo's mise.toml before building; see CONTAINER.md."

// addContainerFilesIfMissing delivers the container files to an existing repo
// whose manifest names a container-pattern stack (Go or Python). A repo whose
// manifest names any other stack (including a legacy repo with no stack) is
// skipped: the stack is never guessed.
//
// The build pair (Dockerfile, .dockerignore) is all-or-nothing against a
// custom setup: if either exists with content that is not forge's, nothing is
// added, because the shipped .dockerignore is an allowlist (it starts with
// `*`) that would break a hand-written Dockerfile. CONTAINER.md is never
// overwritten.
//
// An interrupted run is recoverable without any progress record: ownership is
// decided by content. A build-pair file that is byte-identical to the embedded
// one was written by forge, so a retry treats it as already delivered and
// writes whatever is still missing. Each file is written to a temporary name
// and renamed into place, so a partial write can never be mistaken for a
// hand-written file. The Dockerfile goes last: without the allowlist
// .dockerignore its `COPY . .` would take the whole checkout.
func addContainerFilesIfMissing(assets fs.FS, targetDir string, m Manifest) (added []string, err error) {
	if _, ok := containerStacks[m.Stack]; !ok {
		return nil, nil
	}

	// Read every source first so a missing embedded file fails before any write.
	contents := make(map[string][]byte, len(containerFiles))
	for _, name := range containerFiles {
		src := "templates/golden/" + m.Stack + "/.forge-overlay/" + name
		data, err := fs.ReadFile(assets, src)
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", src, err)
		}
		contents[name] = data
	}

	missing := map[string]bool{}
	for _, name := range []string{".dockerignore", "Dockerfile"} {
		state, err := containerFileState(filepath.Join(targetDir, name), contents[name])
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", name, err)
		}
		switch state {
		case containerFileCustom:
			return nil, nil
		case containerFileMissing:
			missing[name] = true
		}
	}
	state, err := containerFileState(filepath.Join(targetDir, "CONTAINER.md"), contents["CONTAINER.md"])
	if err != nil {
		return nil, fmt.Errorf("inspect CONTAINER.md: %w", err)
	}
	if state == containerFileMissing {
		missing["CONTAINER.md"] = true
	}

	for _, name := range []string{".dockerignore", "CONTAINER.md", "Dockerfile"} {
		if !missing[name] {
			continue
		}
		if err := writeFileViaRename(filepath.Join(targetDir, name), contents[name], 0o644); err != nil {
			return added, fmt.Errorf("write %s: %w", name, err)
		}
		added = append(added, name)
	}
	return added, nil
}

type containerFileKind int

const (
	containerFileMissing containerFileKind = iota
	// containerFileOurs is byte-identical to the embedded file.
	containerFileOurs
	// containerFileCustom is anything else: other content, a symlink, a directory.
	containerFileCustom
)

func containerFileState(path string, embedded []byte) (containerFileKind, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return containerFileMissing, nil
		}
		return containerFileMissing, err
	}
	if !info.Mode().IsRegular() {
		return containerFileCustom, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return containerFileMissing, err
	}
	if bytes.Equal(data, embedded) {
		return containerFileOurs, nil
	}
	return containerFileCustom, nil
}

// writeFileViaRename writes data to a temporary file next to path and renames
// it into place, so path either does not exist or holds the complete content.
func writeFileViaRename(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".forge-upgrade-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
