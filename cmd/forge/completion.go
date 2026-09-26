package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// completionCommand describes one forge subcommand as far as shell
// completion is concerned: its name, its one-line description (matching
// printUsage), and how to build the flag.FlagSet that lists its flags.
// Flags is nil for commands with no flags of their own.
type completionCommand struct {
	Name        string
	Description string
	Flags       func() *flag.FlagSet
}

// completionCommands is the single source of truth completion scripts are
// generated from. Each entry's FlagSet is built from the same constructor
// the real runner uses, so a flag added to a runner shows up in completion
// automatically.
var completionCommands = []completionCommand{
	{"init", "Create a new project", func() *flag.FlagSet {
		flags, _ := initFlagSet()
		return flags
	}},
	{"sync-allowlist", "Reconcile managed allowlist block", func() *flag.FlagSet {
		flags, _ := syncAllowlistFlagSet("")
		return flags
	}},
	{"update", "Refresh a vendored stack snapshot", func() *flag.FlagSet {
		flags, _ := updateFlagSet()
		return flags
	}},
	{"upgrade", "Propagate infrastructure file updates", func() *flag.FlagSet {
		flags, _ := upgradeFlagSet()
		return flags
	}},
	{"completion", "Generate shell completion script", nil},
	{"help", "Show usage", nil},
}

// supportedCompletionShells lists the shells runCompletion knows how to
// generate a script for.
var supportedCompletionShells = []string{"zsh"}

// runCompletion writes a shell completion script for the requested shell to
// w. args[0] names the shell; a missing or unsupported shell is an error.
func runCompletion(args []string, w io.Writer) error {
	var shell string
	if len(args) > 0 {
		shell = args[0]
	}

	switch shell {
	case "zsh":
		_, err := io.WriteString(w, zshCompletionScript())
		return err
	default:
		return fmt.Errorf("unsupported shell %q (supported: %s)", shell, strings.Join(supportedCompletionShells, ", "))
	}
}

// zshCompletionScript renders a zsh completion script for forge, built from
// completionCommands. The script is valid both saved as "_forge" in fpath
// (autoloaded via the #compdef header during compinit: zsh runs the file
// body as the function's first call, so the tail re-invokes _forge when
// $funcstack[1] shows that's what just happened) and sourced directly
// (where the tail's compdef call registers the function instead).
func zshCompletionScript() string {
	var b strings.Builder

	b.WriteString("#compdef forge\n\n")
	b.WriteString("_forge() {\n")
	b.WriteString("  local -a commands\n")
	b.WriteString("  commands=(\n")
	for _, cmd := range completionCommands {
		fmt.Fprintf(&b, "    '%s:%s'\n", cmd.Name, zshEscape(cmd.Description))
	}
	b.WriteString("  )\n\n")

	b.WriteString("  if (( CURRENT == 2 )); then\n")
	b.WriteString("    _describe 'command' commands\n")
	b.WriteString("    return\n")
	b.WriteString("  fi\n\n")

	b.WriteString("  local cmd=${words[2]}\n")
	b.WriteString("  case $cmd in\n")
	for _, cmd := range completionCommands {
		if cmd.Name == "completion" {
			b.WriteString("    completion)\n")
			b.WriteString("      _arguments \\\n")
			fmt.Fprintf(&b, "        '1:shell:(%s)'\n", strings.Join(supportedCompletionShells, " "))
			b.WriteString("      ;;\n")
			continue
		}
		if cmd.Flags == nil {
			continue
		}
		writeZshCommandCase(&b, cmd)
	}
	b.WriteString("  esac\n")
	b.WriteString("}\n\n")

	b.WriteString("if [ \"$funcstack[1]\" = \"_forge\" ]; then\n")
	b.WriteString("  _forge \"$@\"\n")
	b.WriteString("else\n")
	b.WriteString("  compdef _forge forge\n")
	b.WriteString("fi\n")

	return b.String()
}

// writeZshCommandCase appends the `case` arm for one subcommand, listing its
// flags via flag.FlagSet.VisitAll so the completion script always reflects
// the runner's actual flags.
func writeZshCommandCase(b *strings.Builder, cmd completionCommand) {
	fmt.Fprintf(b, "    %s)\n", cmd.Name)

	var specs []string
	cmd.Flags().VisitAll(func(f *flag.Flag) {
		desc := zshEscape(f.Usage)
		if isBoolFlag(f) {
			specs = append(specs, fmt.Sprintf("'--%s[%s]'", f.Name, desc))
			return
		}
		specs = append(specs, fmt.Sprintf("'--%s[%s]:%s:'", f.Name, desc, f.Name))
	})

	if len(specs) == 0 {
		b.WriteString("      ;;\n")
		return
	}

	b.WriteString("      _arguments \\\n")
	for i, spec := range specs {
		sep := " \\\n"
		if i == len(specs)-1 {
			sep = "\n"
		}
		fmt.Fprintf(b, "        %s%s", spec, sep)
	}
	b.WriteString("      ;;\n")
}

// isBoolFlag reports whether f is a boolean flag, mirroring the check the
// stdlib flag package itself uses to decide whether "-flag" (no value) is
// accepted.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// zshEscape makes s safe to embed inside a single-quoted zsh string used as
// an _arguments option description.
func zshEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `'\''`)
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	s = strings.ReplaceAll(s, `:`, `\:`)
	return s
}
