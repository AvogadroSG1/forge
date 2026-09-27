package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"

	"forge"
	"forge/internal/prompt"
)

func TestTerminalPrompterImplementsPrompterWithoutBufioFields(t *testing.T) {
	t.Parallel()

	p := terminalPrompter{}
	var iface prompt.Prompter = p
	_ = iface
}

func TestIsUserAbortDetectsHuhAbortError(t *testing.T) {
	t.Parallel()

	if !isUserAbort(huh.ErrUserAborted) {
		t.Fatal("isUserAbort(huh.ErrUserAborted) = false, want true")
	}
	if isUserAbort(fmt.Errorf("some other error")) {
		t.Fatal("isUserAbort(other) = true, want false")
	}
	if isUserAbort(nil) {
		t.Fatal("isUserAbort(nil) = true, want false")
	}
}

func TestRunInitReturnsErrCancelledOnUserAbort(t *testing.T) {
	t.Parallel()

	err := runInit([]string{
		"--project-name", "Test",
		"--language", "go",
		"--project-type", "cli",
	}, forge.Assets())

	if err == nil {
		t.Fatal("runInit() should fail when stack is missing in non-TTY mode")
	}
	if isUserAbort(err) {
		t.Fatal("non-TTY missing flag should not be a user abort")
	}
	if !strings.Contains(err.Error(), "missing required flag") {
		t.Fatalf("runInit() error = %q, want missing required flag", err)
	}
}

func TestRunReturnsErrCancelledWhenUserAborts(t *testing.T) {
	t.Parallel()

	err := run([]string{"init", "--project-name", "Test"}, forge.Assets())
	if err == nil {
		t.Fatal("run() should return error for missing flags in non-TTY")
	}

	if errors.Is(err, errCancelled) {
		t.Fatal("missing flag error should not be errCancelled")
	}
}

func TestIsUserAbortWrappedErrorStillDetected(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("prompt failed: %w", huh.ErrUserAborted)
	if !isUserAbort(wrapped) {
		t.Fatal("isUserAbort(wrapped huh.ErrUserAborted) = false, want true")
	}
}

func TestSelectCommandDefaultsToHelp(t *testing.T) {
	t.Parallel()

	command, remaining := selectCommand(nil)
	if command != "help" {
		t.Fatalf("command = %q, want help", command)
	}
	if remaining != nil {
		t.Fatalf("remaining args = %#v, want nil", remaining)
	}
}

func TestSelectCommandRecognizesHelpVariants(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"help", "--help", "-h"} {
		command, remaining := selectCommand([]string{arg})
		if command != "help" {
			t.Fatalf("selectCommand(%q): command = %q, want help", arg, command)
		}
		if remaining != nil {
			t.Fatalf("selectCommand(%q): remaining = %#v, want nil", arg, remaining)
		}
	}
}

func TestSelectCommandRecognizesExplicitSubcommands(t *testing.T) {
	t.Parallel()

	command, remaining := selectCommand([]string{"sync-allowlist", "--check"})
	if command != "sync-allowlist" {
		t.Fatalf("command = %q, want sync-allowlist", command)
	}
	if len(remaining) != 1 || remaining[0] != "--check" {
		t.Fatalf("remaining args = %#v, want [--check]", remaining)
	}
}

func TestSyncAllowlistUsesCanonicalEmbeddedEntriesForTheProjectLanguage(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, "settings.local.json")
	original := "{\n  \"permissions\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:0\",\n      \"Bash(go:*)\",\n      \"// END FORGE ALLOW\",\n      \"Bash(true)\"\n    ]\n  }\n}\n"
	if err := os.WriteFile(settingsPath, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := runSyncAllowlist([]string{"--path", settingsPath}, forge.Assets()); err != nil {
		t.Fatalf("runSyncAllowlist() error = %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	block := string(data)
	for _, snippet := range []string{`"Bash(git status:*)",`, `"Bash(instill:*)",`, `"Bash(apm:*)",`, `"Skill(*)",`, `"Bash(lefthook:*)",`, `"Bash(go:*)",`} {
		if !strings.Contains(block, snippet) {
			t.Fatalf("synced allowlist missing %q in %s", snippet, block)
		}
	}
	if strings.Contains(block, `"Bash(python:*)",`) {
		t.Fatalf("synced allowlist should not inject python rules into a go project:\n%s", block)
	}
}

func TestRunSyncAllowlistCheckNotifiesWithoutMutatingSettings(t *testing.T) {
	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, "settings.local.json")
	original := "{\n  \"permissions\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:0\",\n      \"Bash(go:*)\",\n      \"// END FORGE ALLOW\",\n      \"Bash(true)\"\n    ]\n  }\n}\n"
	if err := os.WriteFile(settingsPath, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	output, err := captureStdout(t, func() error {
		return runSyncAllowlist([]string{"--check", "--path", settingsPath}, forge.Assets())
	})
	if err != nil {
		t.Fatalf("runSyncAllowlist(--check) error = %v", err)
	}
	if !strings.Contains(output, "allowlist is 2 version(s) behind; run forge sync-allowlist") {
		t.Fatalf("runSyncAllowlist(--check) output = %q, want stale notice", output)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != original {
		t.Fatalf("runSyncAllowlist(--check) mutated settings.local.json:\n%s", string(data))
	}
}

func TestRunSyncAllowlistIncludesPersonalRulesOnlyWhenRequested(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, "settings.local.json")
	original := "{\n  \"permissions\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:0\",\n      \"Bash(go:*)\",\n      \"// END FORGE ALLOW\",\n      \"Bash(true)\"\n    ]\n  }\n}\n"
	if err := os.WriteFile(settingsPath, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := runSyncAllowlist([]string{"--path", settingsPath}, forge.Assets()); err != nil {
		t.Fatalf("runSyncAllowlist(default) error = %v", err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile(default) error = %v", err)
	}
	if strings.Contains(string(data), `"Bash(gw:*)",`) {
		t.Fatalf("default sync unexpectedly included personal rules:\n%s", string(data))
	}

	if err := runSyncAllowlist([]string{"--include-personal", "--path", settingsPath}, forge.Assets()); err != nil {
		t.Fatalf("runSyncAllowlist(--include-personal) error = %v", err)
	}
	data, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile(include personal) error = %v", err)
	}
	for _, snippet := range []string{`"Bash(gw:*)",`, `"Bash(slack-cli:*)",`} {
		if !strings.Contains(string(data), snippet) {
			t.Fatalf("sync with personal rules missing %q in:\n%s", snippet, string(data))
		}
	}
}

func TestRunSyncAllowlistRejectsConflictingManagedBlockLanguageMarkers(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, "settings.local.json")
	original := "{\n  \"permissions\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:0\",\n      \"Bash(go:*)\",\n      \"Bash(python:*)\",\n      \"// END FORGE ALLOW\",\n      \"Bash(true)\"\n    ]\n  }\n}\n"
	if err := os.WriteFile(settingsPath, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := runSyncAllowlist([]string{"--path", settingsPath}, forge.Assets())
	if err == nil || !strings.Contains(err.Error(), "conflicting language markers") {
		t.Fatalf("runSyncAllowlist() error = %v, want conflicting language markers", err)
	}
}

func TestRunSyncAllowlistUpdatesOpenCodeJsonc(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	settingsPath := filepath.Join(tempDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	settingsOriginal := "{\n  \"permissions\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:0\",\n      \"Bash(go:*)\",\n      \"// END FORGE ALLOW\"\n    ]\n  }\n}\n"
	if err := os.WriteFile(settingsPath, []byte(settingsOriginal), 0o644); err != nil {
		t.Fatalf("WriteFile(settings) error = %v", err)
	}

	opencodeOriginal := "{\n  \"permission\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:0\",\n      \"go:*\",\n      \"// END FORGE ALLOW\"\n    ]\n  }\n}\n"
	opencodePath := filepath.Join(tempDir, "opencode.jsonc")
	if err := os.WriteFile(opencodePath, []byte(opencodeOriginal), 0o644); err != nil {
		t.Fatalf("WriteFile(opencode) error = %v", err)
	}

	if err := runSyncAllowlist([]string{"--path", settingsPath}, forge.Assets()); err != nil {
		t.Fatalf("runSyncAllowlist() error = %v", err)
	}

	data, err := os.ReadFile(opencodePath)
	if err != nil {
		t.Fatalf("ReadFile(opencode.jsonc) error = %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"go*": "allow"`) || !strings.Contains(content, `"git status*": "allow"`) {
		t.Fatalf("expected updated rules in opencode.jsonc, got:\n%s", content)
	}
}

func TestRunSyncAllowlistFallsBackToOpenCodeJson(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	settingsPath := filepath.Join(tempDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	settingsOriginal := "{\n  \"permissions\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:0\",\n      \"Bash(go:*)\",\n      \"// END FORGE ALLOW\"\n    ]\n  }\n}\n"
	if err := os.WriteFile(settingsPath, []byte(settingsOriginal), 0o644); err != nil {
		t.Fatalf("WriteFile(settings) error = %v", err)
	}

	opencodeOriginal := "{\n  \"permission\": {\n    \"allow\": [\n      \"// BEGIN FORGE ALLOW v:0\",\n      \"go:*\",\n      \"// END FORGE ALLOW\"\n    ]\n  }\n}\n"
	opencodePath := filepath.Join(tempDir, "opencode.json")
	if err := os.WriteFile(opencodePath, []byte(opencodeOriginal), 0o644); err != nil {
		t.Fatalf("WriteFile(opencode) error = %v", err)
	}

	if err := runSyncAllowlist([]string{"--path", settingsPath}, forge.Assets()); err != nil {
		t.Fatalf("runSyncAllowlist() error = %v", err)
	}

	data, err := os.ReadFile(opencodePath)
	if err != nil {
		t.Fatalf("ReadFile(opencode.json) error = %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"go*": "allow"`) || !strings.Contains(content, `"git status*": "allow"`) {
		t.Fatalf("expected updated rules in opencode.json, got:\n%s", content)
	}
}

func TestSelectCommandRecognizesCompletion(t *testing.T) {
	t.Parallel()

	command, remaining := selectCommand([]string{"completion", "zsh"})
	if command != "completion" {
		t.Fatalf("command = %q, want completion", command)
	}
	if len(remaining) != 1 || remaining[0] != "zsh" {
		t.Fatalf("remaining args = %#v, want [zsh]", remaining)
	}
}

func TestSelectCommandRecognizesUpdate(t *testing.T) {
	t.Parallel()

	command, remaining := selectCommand([]string{"update", "--stack", "go-cli-cobra"})
	if command != "update" {
		t.Fatalf("command = %q, want update", command)
	}
	if got := strings.Join(remaining, " "); got != "--stack go-cli-cobra" {
		t.Fatalf("remaining args = %q, want %q", got, "--stack go-cli-cobra")
	}
}

func TestRunUpdateRequiresStackFlag(t *testing.T) {
	t.Parallel()

	err := runUpdate(nil, forge.Assets())
	if err == nil {
		t.Fatal("runUpdate() error = nil, want missing stack flag")
	}
	if !strings.Contains(err.Error(), "missing required flag: --stack") {
		t.Fatalf("runUpdate() error = %q, want missing stack flag", err)
	}
}

func TestRunHelpListsCompletionCommand(t *testing.T) {
	// Not parallel: captureStdout swaps the process-wide os.Stdout.

	output, err := captureStdout(t, func() error {
		return run([]string{"help"}, forge.Assets())
	})
	if err != nil {
		t.Fatalf("run([help]) error = %v", err)
	}
	if !strings.Contains(output, "completion      Generate shell completion script") {
		t.Fatalf("usage output = %q, want completion command listed", output)
	}
}

func TestRunDispatchesCompletionCommand(t *testing.T) {
	// Not parallel: captureStdout swaps the process-wide os.Stdout.

	output, err := captureStdout(t, func() error {
		return run([]string{"completion", "zsh"}, forge.Assets())
	})
	if err != nil {
		t.Fatalf("run([completion zsh]) error = %v", err)
	}
	if !strings.HasPrefix(output, "#compdef forge\n") {
		t.Fatalf("run([completion zsh]) output = %q, want #compdef header", output)
	}
}

func TestRunDispatchesCompletionUnsupportedShellError(t *testing.T) {
	t.Parallel()

	err := run([]string{"completion", "fish"}, forge.Assets())
	if err == nil {
		t.Fatal("run([completion fish]) error = nil, want unsupported shell error")
	}
	if !strings.Contains(err.Error(), `unsupported shell "fish"`) {
		t.Fatalf("run([completion fish]) error = %q, want unsupported shell", err)
	}
}

// TestPrintUsageMatchesCompletionCommandsDescriptions guards against
// printUsage's "Available Commands" text drifting from the descriptions
// completion.go embeds in generated scripts: every completionCommands entry
// except "help" (which printUsage does not list as a command) must appear,
// name and description together, on one line of the usage output.
func TestPrintUsageMatchesCompletionCommandsDescriptions(t *testing.T) {
	// Not parallel: captureStdout swaps the process-wide os.Stdout.

	output, err := captureStdout(t, func() error {
		printUsage()
		return nil
	})
	if err != nil {
		t.Fatalf("printUsage() capture error = %v", err)
	}

	for _, cmd := range completionCommands {
		if cmd.Name == "help" {
			continue
		}

		found := false
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(line, cmd.Name) && strings.Contains(line, cmd.Description) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("printUsage() output missing a line with both %q and %q:\n%s", cmd.Name, cmd.Description, output)
		}
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()

	runErr := fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close() error = %v", err)
	}

	return string(output), runErr
}
