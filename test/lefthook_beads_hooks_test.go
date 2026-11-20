package test

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"forge"
)

// ADR-0024: Lefthook owns .git/hooks and Beads is a Lefthook command. This
// drives the real git, bd and lefthook against a scratch repository and the
// shipped lefthook.yml reduced to its beads commands (the other commands need
// a stack's toolchain). It skips unless all three tools are on PATH.
func TestLefthookOwnsGitHooksAndRunsBeadsOncePerHook(t *testing.T) {
	var bin = map[string]string{}
	for _, tool := range []string{"git", "bd", "lefthook"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
		bin[tool] = path
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	stubDir := filepath.Join(root, "stubs")
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	for _, dir := range []string{home, stubDir, repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// A bd wrapper that records each invocation, then runs the real bd.
	invocations := filepath.Join(root, "bd-invocations.log")
	wrapper := "#!/bin/sh\necho \"$*\" >> '" + invocations + "'\nexec '" + bin["bd"] + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write bd wrapper: %v", err)
	}

	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"PATH=" + strings.Join([]string{stubDir, filepath.Dir(bin["git"]), filepath.Dir(bin["lefthook"]), filepath.Dir(bin["bd"]), "/usr/bin", "/bin"}, string(os.PathListSeparator)),
		"BEADS_NO_DAEMON=1",
	}
	run := func(dir string, name string, args ...string) string {
		t.Helper()
		out, err := runCommandWithin(dir, env, 2*time.Minute, name, args...)
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return out
	}

	run(root, "git", "init", "-q", "--bare", remote)
	run(repo, "git", "init", "-q", "-b", "main")
	run(repo, "git", "config", "user.email", "ci@example.com")
	run(repo, "git", "config", "user.name", "CI Bot")
	run(repo, "git", "remote", "add", "origin", remote)
	run(repo, "bd", "init", "--skip-hooks", "--quiet")

	if out, err := runCommandWithin(repo, env, time.Minute, "git", "config", "--get", "core.hooksPath"); err == nil {
		t.Fatalf("bd init --skip-hooks set core.hooksPath = %q; Lefthook must own .git/hooks", strings.TrimSpace(out))
	}

	config := beadsOnlyLefthookConfig(t)
	if err := os.WriteFile(filepath.Join(repo, "lefthook.yml"), config, 0o644); err != nil {
		t.Fatalf("write lefthook.yml: %v", err)
	}
	run(repo, "lefthook", "install")

	hooksDir := filepath.Join(repo, ".git", "hooks")
	assertNoHookLeftovers := func() {
		t.Helper()
		entries, err := os.ReadDir(hooksDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			for _, suffix := range []string{".old", ".lefthook", ".backup"} {
				if strings.HasSuffix(entry.Name(), suffix) {
					t.Errorf(".git/hooks/%s: Lefthook must be the only owner, no wrappers or backups", entry.Name())
				}
			}
		}
	}
	assertNoHookLeftovers()
	if _, err := os.Stat(filepath.Join(repo, ".beads", "hooks")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(repo, ".beads", "hooks"))
		if len(entries) > 0 {
			t.Errorf(".beads/hooks is populated although bd init --skip-hooks was used: %v", entries)
		}
	}

	hooksBefore := hashHookFiles(t, hooksDir)

	countInvocations := func(hook string) int {
		t.Helper()
		data, err := os.ReadFile(invocations)
		if err != nil {
			return 0
		}
		n := 0
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(line, "hooks run "+hook) {
				n++
			}
		}
		return n
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "git", "add", "README.md")
	out := run(repo, "git", "commit", "-q", "-m", "first commit")
	for _, hook := range []string{"pre-commit", "prepare-commit-msg"} {
		if got := countInvocations(hook); got != 1 {
			t.Errorf("bd hooks run %s invoked %d times by `git commit`, want exactly 1\n%s", hook, got, out)
		}
	}
	run(repo, "git", "push", "-q", "origin", "main")
	if got := countInvocations("pre-push"); got != 1 {
		t.Errorf("bd hooks run pre-push invoked %d times by `git push`, want exactly 1", got)
	}
	run(repo, "git", "checkout", "-q", "-b", "topic")
	if got := countInvocations("post-checkout"); got != 1 {
		t.Errorf("bd hooks run post-checkout invoked %d times by `git checkout -b`, want exactly 1", got)
	}

	run(repo, "lefthook", "install")
	assertNoHookLeftovers()
	hooksAfter := hashHookFiles(t, hooksDir)
	if len(hooksAfter) != len(hooksBefore) {
		t.Errorf("hook files after a second `lefthook install` = %d, want %d", len(hooksAfter), len(hooksBefore))
	}
	for name, sum := range hooksBefore {
		if hooksAfter[name] != sum {
			t.Errorf(".git/hooks/%s changed on the second `lefthook install`", name)
		}
	}
	if out, err := runCommandWithin(repo, env, time.Minute, "git", "config", "--get", "core.hooksPath"); err == nil {
		t.Errorf("core.hooksPath = %q after `lefthook install`, want unset", strings.TrimSpace(out))
	}
}

// beadsOnlyLefthookConfig returns the shipped go-cli-cobra lefthook.yml with
// every command except `beads` removed.
func beadsOnlyLefthookConfig(t *testing.T) []byte {
	t.Helper()

	data, err := fs.ReadFile(forge.Assets(), "templates/golden/go-cli-cobra/.forge-overlay/lefthook.yml")
	if err != nil {
		t.Fatalf("read shipped lefthook.yml: %v", err)
	}
	var config map[string]map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse shipped lefthook.yml: %v", err)
	}
	for hook, body := range config {
		commands, ok := body["commands"].(map[string]any)
		if !ok {
			t.Fatalf("hook %s has no commands map", hook)
		}
		for name := range commands {
			if name != "beads" {
				delete(commands, name)
			}
		}
		if _, ok := commands["beads"]; !ok {
			t.Fatalf("hook %s has no beads command", hook)
		}
	}
	out, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func hashHookFiles(t *testing.T, hooksDir string) map[string][sha256.Size]byte {
	t.Helper()

	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		t.Fatal(err)
	}
	sums := map[string][sha256.Size]byte{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(hooksDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sums[entry.Name()] = sha256.Sum256(data)
	}
	if len(sums) == 0 {
		t.Fatalf("no hook files in %s; lefthook install did not run", hooksDir)
	}
	return sums
}
