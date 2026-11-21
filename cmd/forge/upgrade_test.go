package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge"
	upgradepkg "forge/internal/upgrade"
)

func TestUpgradeFlagSetParsesAgentAndCheck(t *testing.T) {
	t.Parallel()

	flags, opts := upgradeFlagSet()
	if err := flags.Parse([]string{"--agent", "pi", "--check"}); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if opts.Agent != "pi" || !opts.CheckOnly {
		t.Fatalf("opts = %+v, want agent pi and check", *opts)
	}
	if flags.Lookup("agent") == nil {
		t.Fatal("upgrade has no --agent flag")
	}
}

func TestRunUpgradeAgentOptInWritesAddonAndListsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "hooks", "guard"), []byte("g"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x/y\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upgradepkg.Stamp(dir, upgradepkg.Manifest{Language: "go", Stack: "go-cli-cobra"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	output, err := captureStdout(t, func() error {
		return runUpgrade([]string{"--agent", "pi"}, forge.Assets())
	})
	if err != nil {
		t.Fatalf("runUpgrade(--agent pi) error = %v", err)
	}
	if strings.Contains(output, "infrastructure is current") || !strings.Contains(output, "Dockerfile.agent") || !strings.Contains(output, "cmd/agent.go") {
		t.Fatalf("output = %q, want the updated-files listing", output)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cmd", "agent.go"))
	if err != nil || !strings.Contains(string(data), "example.com/x/y/internal/piagent") {
		t.Fatalf("cmd/agent.go = %q, %v; want the go.mod module path", data, err)
	}

	if err := runUpgrade([]string{"--agent", "pi", "--check"}, forge.Assets()); err == nil {
		t.Fatal("runUpgrade(--agent pi --check) succeeded, want an error")
	}
}
