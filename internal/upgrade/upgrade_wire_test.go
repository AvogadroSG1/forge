package upgrade

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// wireAssets carries realistic co-owned hook config so reconciliation
// behavior is observable, plus a parameterized opencode template.
func wireAssets() fstest.MapFS {
	settings := `{
  "enabledPlugins": {},
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "./.claude/hooks/guard"}
        ]
      }
    ],
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bd prime || true"},
          {"type": "command", "command": "command -v forge >/dev/null 2>&1 && forge upgrade --check || true"}
        ]
      }
    ]
  }
}
`
	return fstest.MapFS{
		"templates/common/claude/hooks/guard":              &fstest.MapFile{Data: []byte("#!/usr/bin/env bash\n# guard v2\n")},
		"templates/common/claude/hooks/secret-scan.sh":     &fstest.MapFile{Data: []byte("#!/usr/bin/env bash\n# secret-scan v2\n")},
		"templates/common/claude/settings.json":            &fstest.MapFile{Data: []byte(settings)},
		"templates/common/codex/hooks.json":                &fstest.MapFile{Data: []byte(settings)},
		"templates/common/opencode.jsonc.tmpl":             &fstest.MapFile{Data: []byte("{\n{{- if eq .Language \"go\" }}\n  \"lsp\": {\"gopls\": {}}\n{{- end }}\n}\n")},
		"templates/common/opencode/plugins/forge-hooks.js": &fstest.MapFile{Data: []byte(`export const ForgeHooks = async () => {};` + "\n")},
		"templates/common/mise/conf.d/otel.toml":           &fstest.MapFile{Data: []byte("[env]\nOTEL_EXPORTER_OTLP_ENDPOINT = \"http://localhost:4318\"\n")},
		"templates/common/otel/compose.yaml":               &fstest.MapFile{Data: []byte("name: forge-otel\n")},
		"templates/common/otel/collector.yaml":             &fstest.MapFile{Data: []byte("receivers:\n  otlp: {}\n")},
	}
}

// seedStaleRepo creates a marker-valid repo with a manifest carrying params
// but a stale infra version.
func seedStaleRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	staleManifest := Manifest{SchemaVersion: 1, InfraVersion: 0, Language: "go", Stack: "go-cli-cobra"}
	data, err := json.Marshal(staleManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "manifest.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const thirdPartySettings = `{
  "enabledPlugins": {},
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "./.claude/hooks/guard"}
        ]
      },
      {
        "matcher": "Edit|Write",
        "hooks": [
          {"type": "command", "command": "/abs/.beads/hooks/agent-fitness-functions-pre-tool-use"}
        ]
      }
    ],
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bd prime || true"},
          {"type": "command", "command": "command -v forge >/dev/null 2>&1 && forge upgrade --check || true"}
        ]
      },
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bd prime --hook-json"}
        ]
      }
    ]
  }
}
`

func TestRunPreservesThirdPartyHookEntries(t *testing.T) {
	t.Parallel()

	dir := seedStaleRepo(t)
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(settingsPath, []byte(thirdPartySettings), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(wireAssets(), dir, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	out, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"bd prime --hook-json",
		"agent-fitness-functions-pre-tool-use",
	} {
		if got := strings.Count(string(out), want); got != 1 {
			t.Fatalf("third-party entry %q occurs %d times after upgrade, want 1\n%s", want, got, out)
		}
	}
	for _, owned := range []string{"./.claude/hooks/guard", "bd prime || true", "forge upgrade --check"} {
		if got := strings.Count(string(out), owned); got != 1 {
			t.Fatalf("owned entry %q occurs %d times after upgrade, want 1\n%s", owned, got, out)
		}
	}
}

func TestRunStillOverwritesWhollyOwnedFiles(t *testing.T) {
	t.Parallel()

	dir := seedStaleRepo(t)

	if _, err := Run(wireAssets(), dir, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".claude", "hooks", "guard"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/usr/bin/env bash\n# guard v2\n" {
		t.Fatalf("wholly-owned guard not byte-replaced: got %q", string(data))
	}
}

func TestRunTwiceIsByteIdentical(t *testing.T) {
	t.Parallel()

	dir := seedStaleRepo(t)
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(settingsPath, []byte(thirdPartySettings), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(wireAssets(), dir, false); err != nil {
		t.Fatalf("Run(1) error = %v", err)
	}
	first, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Force staleness again and re-run: the reconciled file must not churn.
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.InfraVersion = 0
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "manifest.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(wireAssets(), dir, false); err != nil {
		t.Fatalf("Run(2) error = %v", err)
	}
	second, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("settings.json churned on second upgrade:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestRunFailsLoudOnMalformedCoOwnedFile(t *testing.T) {
	t.Parallel()

	dir := seedStaleRepo(t)
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(settingsPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Run(wireAssets(), dir, false)
	if err == nil {
		t.Fatal("Run() error = nil for malformed co-owned settings.json, want failure")
	}
	if !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("Run() error = %q, want it to name the malformed file", err)
	}

	data, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "{not json" {
		t.Fatalf("malformed co-owned file was overwritten: %q", string(data))
	}
}

func TestRunMigratesLegacyMarkerToManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge-infra-version"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := "{\n  \"permissions\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:2\",\n      \"Bash(go:*)\",\n      \"// END FORGE ALLOW\"\n    ]\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(wireAssets(), dir, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest() after migration error = %v; legacy repos must be backfilled when inferable", err)
	}
	if m.InfraVersion != Version {
		t.Fatalf("migrated manifest infraVersion = %d, want %d", m.InfraVersion, Version)
	}
	if m.Language != "go" {
		t.Fatalf("migrated manifest language = %q, want go (inferred from managed allow block)", m.Language)
	}
}

func TestRunSkipsManifestBackfillWhenInferenceFails(t *testing.T) {
	t.Parallel()

	// No settings.local.json at all: params are uninferable. The upgrade must
	// still succeed (hooks reconciled, marker stamped) without guessing.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge-infra-version"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(wireAssets(), dir, false); err != nil {
		t.Fatalf("Run() error = %v; uninferable params must not block the infra upgrade", err)
	}

	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("manifest was created despite uninferable params; params must never be guessed")
	}
	legacy, err := os.ReadFile(filepath.Join(dir, ".forge-infra-version"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(Version); strings.TrimSpace(string(legacy)) != want {
		t.Fatalf("legacy marker = %q, want current version stamped (%s)", string(legacy), want)
	}
}

func TestCheckReportsBeadsPermissionDriftWithoutMutation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Stamp(dir, Manifest{Language: "go"}); err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	status, err := Run(wireAssets(), dir, true)
	if err != nil {
		t.Fatalf("Run(check) error = %v", err)
	}
	if !status.PermsDrift {
		t.Fatal("Run(check) PermsDrift = false, want true for 0755 .beads")
	}
	info, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		// check mode must never mutate
		if info.Mode().Perm() != 0o755 {
			t.Fatalf(".beads mode after check = %o, want untouched 755", info.Mode().Perm())
		}
	} else {
		t.Fatal("Run(check) mutated .beads permissions; check must be read-only")
	}
}

func TestRunRepairsBeadsDirPermissions(t *testing.T) {
	t.Parallel()

	dir := seedStaleRepo(t)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(wireAssets(), dir, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	info, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf(".beads mode after upgrade = %o, want 700", info.Mode().Perm())
	}
}

func TestRunRendersOpencodeFromManifestOnlyWhenMissing(t *testing.T) {
	t.Parallel()

	dir := seedStaleRepo(t) // manifest carries language "go"

	if _, err := Run(wireAssets(), dir, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "opencode.jsonc"))
	if err != nil {
		t.Fatalf("opencode.jsonc not rendered for repo with manifest params: %v", err)
	}
	if strings.Contains(string(data), "{{") {
		t.Fatalf("rendered opencode.jsonc contains template syntax:\n%s", data)
	}
	if !strings.Contains(string(data), "gopls") {
		t.Fatalf("rendered opencode.jsonc missing go-conditional content:\n%s", data)
	}
}
