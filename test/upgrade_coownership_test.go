//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/upgrade"
)

// TestUpgradePreservesCoOwnedConfigEndToEnd exercises the released binary
// against a synthetic legacy repo shaped like the real todo-cli incident:
// bd-installed hook entries alongside forge's, a hand-edited opencode.jsonc,
// a stale infra marker, and a world-readable .beads directory.
func TestUpgradePreservesCoOwnedConfigEndToEnd(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	mustMkdir(t, filepath.Join(repoDir, ".claude", "hooks"))
	mustWrite(t, filepath.Join(repoDir, ".claude", "hooks", "guard"), "#!/usr/bin/env bash\n# stale guard\n", 0o755)

	// Start from the real shipped template and inject third-party entries the
	// way bd does: its own matcher groups.
	templateBytes, err := os.ReadFile(filepath.Join(runtimeWorkspaceRoot, "templates", "common", "claude", "settings.json"))
	if err != nil {
		t.Fatalf("read template settings.json: %v", err)
	}
	seeded := injectThirdPartyHooks(t, templateBytes)
	mustWrite(t, filepath.Join(repoDir, ".claude", "settings.json"), string(seeded), 0o644)

	codexTemplate, err := os.ReadFile(filepath.Join(runtimeWorkspaceRoot, "templates", "common", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read template codex hooks.json: %v", err)
	}
	mustMkdir(t, filepath.Join(repoDir, ".codex"))
	mustWrite(t, filepath.Join(repoDir, ".codex", "hooks.json"), string(injectThirdPartyHooks(t, codexTemplate)), 0o644)

	handEditedOpencode := "{\n  // hand-tuned\n  \"lsp\": {\"gopls\": {\"command\": \"gopls\"}}\n}\n"
	mustWrite(t, filepath.Join(repoDir, "opencode.jsonc"), handEditedOpencode, 0o644)

	localSettings := "{\n  \"permissions\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:2\",\n      \"Bash(go:*)\",\n      \"// END FORGE ALLOW\"\n    ]\n  }\n}\n"
	mustWrite(t, filepath.Join(repoDir, ".claude", "settings.local.json"), localSettings, 0o644)

	mustWrite(t, filepath.Join(repoDir, ".forge-infra-version"), "3\n", 0o644)

	beadsDir := filepath.Join(repoDir, ".beads")
	mustMkdir(t, beadsDir)
	if err := os.Chmod(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// --check on a stale repo must exit non-zero.
	if _, err := runForge(t, repoDir, "upgrade", "--check"); err == nil {
		t.Fatal("upgrade --check exited 0 on a stale repo, want non-zero")
	}

	// Mutating upgrade.
	if out, err := runForge(t, repoDir, "upgrade"); err != nil {
		t.Fatalf("forge upgrade failed: %v\n%s", err, out)
	}

	settings, err := os.ReadFile(filepath.Join(repoDir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, thirdParty := range []string{"bd prime --hook-json", "agent-fitness-functions-git-guard"} {
		if got := strings.Count(string(settings), thirdParty); got != 1 {
			t.Fatalf("third-party entry %q occurs %d times after upgrade, want 1\n%s", thirdParty, got, settings)
		}
	}
	for _, owned := range []string{"./.claude/hooks/guard", "forge upgrade --check"} {
		if got := strings.Count(string(settings), owned); got != 1 {
			t.Fatalf("owned entry %q occurs %d times after upgrade, want 1\n%s", owned, got, settings)
		}
	}

	guard, err := os.ReadFile(filepath.Join(repoDir, ".claude", "hooks", "guard"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(guard), "stale guard") {
		t.Fatal("wholly-owned guard was not replaced by upgrade")
	}

	opencode, err := os.ReadFile(filepath.Join(repoDir, "opencode.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if string(opencode) != handEditedOpencode {
		t.Fatalf("hand-edited opencode.jsonc mutated by upgrade:\n%s", opencode)
	}

	marker, err := os.ReadFile(filepath.Join(repoDir, ".forge-infra-version"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(marker)) == "3" {
		t.Fatalf("infra marker not advanced: %q", marker)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(repoDir, ".forge", "manifest.json"))
	if err != nil {
		t.Fatalf("manifest not backfilled from allowlist inference: %v", err)
	}
	if !strings.Contains(string(manifestBytes), `"language": "go"`) {
		t.Fatalf("backfilled manifest missing inferred language:\n%s", manifestBytes)
	}

	info, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf(".beads mode after upgrade = %o, want 700", info.Mode().Perm())
	}

	// Idempotence: a second upgrade changes nothing and --check is quiet.
	before, err := os.ReadFile(filepath.Join(repoDir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runForge(t, repoDir, "upgrade"); err != nil {
		t.Fatalf("second forge upgrade failed: %v\n%s", err, out)
	}
	after, err := os.ReadFile(filepath.Join(repoDir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("settings.json churned on second upgrade:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if out, err := runForge(t, repoDir, "upgrade", "--check"); err != nil {
		t.Fatalf("upgrade --check non-zero after upgrade: %v\n%s", err, out)
	}

	// No file written by the upgrade may contain template syntax.
	for _, rel := range []string{".claude/settings.json", ".codex/hooks.json", "opencode.jsonc"} {
		data, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "{{") {
			t.Fatalf("%s contains unrendered template syntax:\n%s", rel, data)
		}
	}
}

func injectThirdPartyHooks(t *testing.T, templateBytes []byte) []byte {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(templateBytes, &doc); err != nil {
		t.Fatalf("template is not valid JSON: %v", err)
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		t.Fatal("template has no hooks object")
	}

	entry := func(command string) map[string]any {
		return map[string]any{"type": "command", "command": command}
	}
	group := func(matcher string, commands ...string) map[string]any {
		hookList := make([]any, 0, len(commands))
		for _, c := range commands {
			hookList = append(hookList, entry(c))
		}
		return map[string]any{"matcher": matcher, "hooks": hookList}
	}

	sessionStart, _ := hooks["SessionStart"].([]any)
	hooks["SessionStart"] = append(sessionStart, group("", "bd prime --hook-json"))
	preToolUse, _ := hooks["PreToolUse"].([]any)
	hooks["PreToolUse"] = append(preToolUse, group("Bash", "/abs/.beads/hooks/agent-fitness-functions-git-guard"))

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func runForge(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command(forgeBinary, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeAgentFlagEndToEnd runs the built binary: --agent pi opts a
// go-cli-cobra repo in (ADR-0028), and --agent with --check is refused.
func TestUpgradeAgentFlagEndToEnd(t *testing.T) {
	t.Parallel()

	repoDir := scaffoldPiAddonStack(t, "go-cli-cobra", "cli", "")
	if err := upgrade.Stamp(repoDir, upgrade.Manifest{Language: "go", Stack: "go-cli-cobra"}); err != nil {
		t.Fatal(err)
	}

	if out, err := runForge(t, repoDir, "upgrade", "--agent", "pi", "--check"); err == nil {
		t.Fatalf("upgrade --agent pi --check succeeded, want failure:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "Dockerfile.agent")); err == nil {
		t.Fatal("refused run wrote Dockerfile.agent")
	}

	out, err := runForge(t, repoDir, "upgrade", "--agent", "pi")
	if err != nil {
		t.Fatalf("upgrade --agent pi failed: %v\n%s", err, out)
	}
	for _, want := range []string{"Dockerfile.agent", "cmd/agent.go", "image:agent:scan"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, rel := range []string{"Dockerfile.agent", "AGENT.md", "cmd/agent.go", "internal/piagent/client.go"} {
		if _, err := os.Stat(filepath.Join(repoDir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s not written: %v", rel, err)
		}
	}
	if out, err := runForge(t, repoDir, "upgrade", "--check"); err != nil {
		t.Fatalf("upgrade --check non-zero after opt-in: %v\n%s", err, out)
	}
}
