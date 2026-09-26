package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRunCompletionZshWritesHeader(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := runCompletion([]string{"zsh"}, &buf); err != nil {
		t.Fatalf("runCompletion(zsh) error = %v", err)
	}

	if !strings.HasPrefix(buf.String(), "#compdef forge\n") {
		t.Fatalf("script does not start with #compdef header:\n%s", buf.String())
	}
}

func TestRunCompletionZshCoversEverySubcommand(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := runCompletion([]string{"zsh"}, &buf); err != nil {
		t.Fatalf("runCompletion(zsh) error = %v", err)
	}
	script := buf.String()

	for _, cmd := range completionCommands {
		if !strings.Contains(script, "'"+cmd.Name+":") {
			t.Fatalf("script missing commands entry for %q:\n%s", cmd.Name, script)
		}
	}
}

// TestRunCompletionZshCoversEveryFlag walks every flag constructor via
// VisitAll (not a hard-coded flag list) and asserts each flag name appears
// in the generated script, so a flag added to a runner is caught here.
func TestRunCompletionZshCoversEveryFlag(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := runCompletion([]string{"zsh"}, &buf); err != nil {
		t.Fatalf("runCompletion(zsh) error = %v", err)
	}
	script := buf.String()

	for _, cmd := range completionCommands {
		if cmd.Flags == nil {
			continue
		}
		cmd.Flags().VisitAll(func(f *flag.Flag) {
			needle := "--" + f.Name + "["
			if !strings.Contains(script, needle) {
				t.Fatalf("script missing flag %q for command %q:\n%s", f.Name, cmd.Name, script)
			}
		})
	}
}

func TestRunCompletionMissingShellErrors(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := runCompletion(nil, &buf)
	if err == nil {
		t.Fatal("runCompletion(nil) error = nil, want unsupported shell error")
	}
	want := `unsupported shell "" (supported: zsh)`
	if err.Error() != want {
		t.Fatalf("runCompletion(nil) error = %q, want %q", err.Error(), want)
	}
	if buf.Len() != 0 {
		t.Fatalf("runCompletion(nil) wrote %q to output, want nothing", buf.String())
	}
}

func TestRunCompletionUnsupportedShellErrors(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := runCompletion([]string{"bash"}, &buf)
	if err == nil {
		t.Fatal("runCompletion(bash) error = nil, want unsupported shell error")
	}
	want := `unsupported shell "bash" (supported: zsh)`
	if err.Error() != want {
		t.Fatalf("runCompletion(bash) error = %q, want %q", err.Error(), want)
	}
	if buf.Len() != 0 {
		t.Fatalf("runCompletion(bash) wrote %q to output, want nothing", buf.String())
	}
}

// writeZshCompletionScript renders the zsh completion script and writes it
// to dir/_forge, returning that path. Tests use this so the script placed
// in fpath is byte-for-byte what runCompletion produces.
func writeZshCompletionScript(t *testing.T, dir string) string {
	t.Helper()

	var buf bytes.Buffer
	if err := runCompletion([]string{"zsh"}, &buf); err != nil {
		t.Fatalf("runCompletion(zsh) error = %v", err)
	}

	path := dir + "/_forge"
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile(_forge) error = %v", err)
	}
	return path
}

// runZshDriver writes driverScript to a file and runs it with `zsh -f`
// (the repo's guard hook blocks interpreter -c invocations, so scripts are
// always run from a file). It returns combined stdout+stderr.
func runZshDriver(t *testing.T, zshPath, dir, driverScript string) string {
	t.Helper()

	driverPath := dir + "/driver.zsh"
	if err := os.WriteFile(driverPath, []byte(driverScript), 0o755); err != nil {
		t.Fatalf("WriteFile(driver.zsh) error = %v", err)
	}

	cmd := exec.Command(zshPath, "-f", driverPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("zsh -f %s error = %v\n%s", driverPath, err, output)
	}
	return string(output)
}

// TestRunCompletionZshAutoloadFirstCallCompletesTopLevel is a regression
// test for the bug where zsh runs an autoloaded function file's body as the
// function's own first call: if the tail of the script does not re-invoke
// _forge on that first call, the very first TAB after `compinit` produces no
// completions at all. This stubs _describe/_arguments/compdef, mimics
// compinit's CURRENT/words state for `forge <TAB>`, and asserts the first
// (and only) call to _forge reaches _describe.
func TestRunCompletionZshAutoloadFirstCallCompletesTopLevel(t *testing.T) {
	t.Parallel()

	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not found on PATH, skipping")
	}

	dir := t.TempDir()
	writeZshCompletionScript(t, dir)

	output := runZshDriver(t, zshPath, dir, `
fpath=(`+dir+` $fpath)
_describe() { print -r -- "DESCRIBE:$@"; }
_arguments() { print -r -- "ARGS:$@"; }
compdef() { print COMPDEF; }
autoload -Uz _forge
words=(forge '')
CURRENT=2
_forge
`)

	if !strings.Contains(output, "DESCRIBE:") {
		t.Fatalf("first autoloaded _forge call produced no completions:\n%s", output)
	}
}

// TestRunCompletionZshAutoloadFirstCallCompletesSubcommandFlags is the same
// regression as above but one level deeper: `forge init <TAB>` on the very
// first call must reach _arguments with the init flags, not silently do
// nothing.
func TestRunCompletionZshAutoloadFirstCallCompletesSubcommandFlags(t *testing.T) {
	t.Parallel()

	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not found on PATH, skipping")
	}

	dir := t.TempDir()
	writeZshCompletionScript(t, dir)

	output := runZshDriver(t, zshPath, dir, `
fpath=(`+dir+` $fpath)
_describe() { print -r -- "DESCRIBE:$@"; }
_arguments() { print -r -- "ARGS:$@"; }
compdef() { print COMPDEF; }
autoload -Uz _forge
words=(forge init '')
CURRENT=3
_forge
`)

	if !strings.Contains(output, "ARGS:") || !strings.Contains(output, "--project-name") {
		t.Fatalf("first autoloaded _forge call for init produced no flag completions:\n%s", output)
	}
}

// TestRunCompletionZshSourcedRegistersViaCompdef confirms the other half of
// the tail conditional: when the script is sourced directly (not autoloaded
// from fpath), it must register itself via `compdef _forge forge` and must
// not itself call _describe/_arguments.
func TestRunCompletionZshSourcedRegistersViaCompdef(t *testing.T) {
	t.Parallel()

	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not found on PATH, skipping")
	}

	dir := t.TempDir()
	scriptPath := writeZshCompletionScript(t, dir)

	output := runZshDriver(t, zshPath, dir, `
_describe() { print -r -- "DESCRIBE:$@"; }
_arguments() { print -r -- "ARGS:$@"; }
compdef() { print COMPDEF; }
source `+scriptPath+`
`)

	if !strings.Contains(output, "COMPDEF") {
		t.Fatalf("sourcing the script did not register via compdef:\n%s", output)
	}
	if strings.Contains(output, "DESCRIBE:") || strings.Contains(output, "ARGS:") {
		t.Fatalf("sourcing the script should not itself produce completions:\n%s", output)
	}
}

func TestRunCompletionZshScriptPassesZshSyntaxCheck(t *testing.T) {
	t.Parallel()

	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not found on PATH, skipping syntax check")
	}

	var buf bytes.Buffer
	if err := runCompletion([]string{"zsh"}, &buf); err != nil {
		t.Fatalf("runCompletion(zsh) error = %v", err)
	}

	tempFile, err := os.CreateTemp(t.TempDir(), "_forge")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if _, err := tempFile.WriteString(buf.String()); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	cmd := exec.Command(zshPath, "-n", tempFile.Name())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zsh -n %s error = %v\n%s", tempFile.Name(), err, output)
	}
}
