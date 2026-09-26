package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"forge"
	allowlist "forge/internal/allowlist"
	"forge/internal/delegate"
	initcmd "forge/internal/init"
	"forge/internal/project"
	"forge/internal/prompt"
	"forge/internal/scaffold"
	updatepkg "forge/internal/update"
	upgradepkg "forge/internal/upgrade"
)

var errCancelled = errors.New("cancelled")

func main() {
	if err := run(os.Args[1:], forge.Assets()); err != nil {
		if errors.Is(err, errCancelled) {
			fmt.Fprintln(os.Stderr, "cancelled")
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, assets fs.FS) error {
	command, args := selectCommand(args)

	var err error
	switch command {
	case "help":
		printUsage()
		return nil
	case "init":
		err = runInit(args, assets)
	case "sync-allowlist":
		err = runSyncAllowlist(args, assets)
	case "update":
		err = runUpdate(args, assets)
	case "upgrade":
		err = runUpgrade(args, assets)
	case "completion":
		err = runCompletion(args, os.Stdout)
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", command)
	}

	if isUserAbort(err) {
		return errCancelled
	}

	return err
}

func selectCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "help", nil
	}

	switch args[0] {
	case "init", "sync-allowlist", "update", "upgrade", "completion":
		return args[0], args[1:]
	case "help", "--help", "-h":
		return "help", nil
	}

	return args[0], args[1:]
}

func printUsage() {
	fmt.Print(`Scaffold AI-native repositories

Usage:
  forge [command]

Available Commands:
  init            Create a new project
  sync-allowlist  Reconcile managed allowlist block
  update          Refresh a vendored stack snapshot
  upgrade         Propagate infrastructure file updates
  completion      Generate shell completion script

Flags:
  -h, --help   help for forge

Use "forge [command] --help" for more information about a command.
`)
}

// initFlagSet builds the flag.FlagSet for "forge init". It is shared by
// runInit and the completion generator, so a flag added here shows up in
// both places automatically.
func initFlagSet() (*flag.FlagSet, *prompt.Inputs) {
	flags := flag.NewFlagSet("forge init", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	inputs := &prompt.Inputs{}
	flags.StringVar(&inputs.ProjectName, "project-name", "", "Project name")
	flags.StringVar(&inputs.Language, "language", "", "Language")
	flags.StringVar(&inputs.ProjectType, "project-type", "", "Project type")
	flags.StringVar(&inputs.Stack, "stack", "", "Stack key")
	flags.StringVar(&inputs.Frontend, "frontend", "", "Frontend fragment for fullstack projects (vite-ts|sveltekit|angular)")
	flags.StringVar(&inputs.APIBaseURL, "api-base-url", "", "API base URL the frontend client targets")
	flags.StringVar(&inputs.AuthorName, "author-name", gitConfig("user.name"), "Author name")
	flags.StringVar(&inputs.AuthorEmail, "author-email", gitConfig("user.email"), "Author email")
	flags.StringVar(&inputs.GitHubUser, "github-user", prompt.DefaultGitHubUser(), "GitHub user for module path derivation")
	flags.StringVar(&inputs.GitHubOrg, "github-org", "", "GitHub organization")
	flags.StringVar(&inputs.GitHubOrg, "org", "", "GitHub organization (alias for --github-org)")
	flags.StringVar(&inputs.Remote, "remote", "", "Remote choice (gh|url|none)")
	flags.StringVar(&inputs.RemoteURL, "remote-url", "", "Remote URL when --remote url is selected")
	flags.StringVar(&inputs.ModulePath, "module-path", "", "Module path override")
	flags.StringVar(&inputs.BdPrefix, "bd-prefix", "", "Beads prefix override")
	flags.StringVar(&inputs.PythonPackageOverride, "python-package", "", "Python package name override (Python stacks only)")
	return flags, inputs
}

func runInit(args []string, assets fs.FS) error {
	flags, inputsPtr := initFlagSet()
	if err := flags.Parse(args); err != nil {
		return err
	}
	inputs := *inputsPtr

	var prompter prompt.Prompter
	inputs.IsTTY = isInteractiveSession()
	if inputs.IsTTY {
		prompter = terminalPrompter{}
	}

	resolved, err := prompt.Resolve(inputs, prompter)
	if err != nil {
		return err
	}
	vars, err := project.ResolveVariables(resolved)
	if err != nil {
		return err
	}

	cwd, err := currentWorkingDir()
	if err != nil {
		return err
	}

	initializer := initcmd.Initializer{
		Writer: scaffold.Writer{Assets: assets},
		Runner: delegate.NewVerboseRunner(os.Stdout),
	}

	return initializer.Run(context.Background(), cwd, vars)
}

// syncAllowlistFlags holds the parsed values for "forge sync-allowlist".
type syncAllowlistFlags struct {
	CheckOnly       bool
	IncludePersonal bool
	SettingsPath    string
}

// syncAllowlistFlagSet builds the flag.FlagSet for "forge sync-allowlist".
// cwd anchors the default --path value; the completion generator passes ""
// since it only needs flag names, not the resolved default.
func syncAllowlistFlagSet(cwd string) (*flag.FlagSet, *syncAllowlistFlags) {
	flags := flag.NewFlagSet("forge sync-allowlist", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	parsed := &syncAllowlistFlags{}
	flags.BoolVar(&parsed.CheckOnly, "check", false, "Only report staleness")
	flags.BoolVar(&parsed.IncludePersonal, "include-personal", false, "Include personal allowlist rules")
	flags.StringVar(&parsed.SettingsPath, "path", filepath.Join(cwd, ".claude", "settings.local.json"), "settings.local.json path")
	return flags, parsed
}

func runSyncAllowlist(args []string, assets fs.FS) error {
	cwd, err := currentWorkingDir()
	if err != nil {
		return err
	}

	flags, parsed := syncAllowlistFlagSet(cwd)
	if err := flags.Parse(args); err != nil {
		return err
	}
	checkOnly := parsed.CheckOnly
	includePersonal := parsed.IncludePersonal
	settingsPath := parsed.SettingsPath

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return err
	}
	language, err := allowlist.InferLanguage(string(data))
	if err != nil {
		return err
	}
	block, err := allowlist.CanonicalBlock(assets, language, allowlist.InferFrontend(string(data)), includePersonal)
	if err != nil {
		return err
	}

	claudeStatus, err := allowlist.Sync(settingsPath, block, checkOnly)
	if err != nil {
		return err
	}

	opencodePath := filepath.Join(cwd, "opencode.jsonc")
	if _, err := os.Stat(opencodePath); os.IsNotExist(err) {
		opencodePath = filepath.Join(cwd, "opencode.json")
	}
	var opencodeStale bool
	if opencodeData, opencodeErr := os.ReadFile(opencodePath); opencodeErr == nil {
		opencodeLang, opencodeLangErr := allowlist.InferLanguage(string(opencodeData))
		if opencodeLangErr != nil {
			opencodeLang = language
		}
		opencodeBlock, opencodeBlockErr := allowlist.CanonicalBlockOpenCode(assets, opencodeLang, allowlist.InferFrontend(string(opencodeData)), includePersonal)
		if opencodeBlockErr == nil {
			ocStatus, ocErr := allowlist.Sync(opencodePath, opencodeBlock, checkOnly)
			if ocErr == nil {
				opencodeStale = ocStatus.Stale
			}
		}
	}

	if checkOnly {
		if claudeStatus.Stale || opencodeStale {
			fmt.Printf("allowlist is %d version(s) behind; run forge sync-allowlist\n", claudeStatus.Embedded-claudeStatus.CurrentVersion)
		}
		return nil
	}

	fmt.Printf("allowlist synced to version %d\n", claudeStatus.CurrentVersion)
	return nil
}

// updateFlagSet builds the flag.FlagSet for "forge update".
func updateFlagSet() (*flag.FlagSet, *string) {
	flags := flag.NewFlagSet("forge update", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var stack string
	flags.StringVar(&stack, "stack", "", "Stack key to refresh")
	return flags, &stack
}

func runUpdate(args []string, assets fs.FS) error {
	flags, stackPtr := updateFlagSet()
	if err := flags.Parse(args); err != nil {
		return err
	}
	stack := *stackPtr
	if strings.TrimSpace(stack) == "" {
		return fmt.Errorf("missing required flag: --stack")
	}

	return updatepkg.Run(context.Background(), assets, stack, delegate.ExecRunner{}, updatepkg.ExecGitRunner{})
}

// upgradeFlagSet builds the flag.FlagSet for "forge upgrade".
func upgradeFlagSet() (*flag.FlagSet, *bool) {
	flags := flag.NewFlagSet("forge upgrade", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var checkOnly bool
	flags.BoolVar(&checkOnly, "check", false, "Only report staleness")
	return flags, &checkOnly
}

func runUpgrade(args []string, assets fs.FS) error {
	cwd, err := currentWorkingDir()
	if err != nil {
		return err
	}

	flags, checkOnlyPtr := upgradeFlagSet()
	if err := flags.Parse(args); err != nil {
		return err
	}
	checkOnly := *checkOnlyPtr

	status, err := upgradepkg.Run(assets, cwd, checkOnly)
	if err != nil {
		return err
	}

	if checkOnly {
		exitNonZero := false
		if status.Stale {
			fmt.Printf("forge upgrade: infrastructure is at v%d, current is v%d; run `forge upgrade`\n", status.OnDisk, status.Version)
			exitNonZero = true
		}
		if status.PermsDrift {
			fmt.Println("forge upgrade: .beads permissions drifted from 0700; run `forge upgrade` to repair")
			exitNonZero = true
		}
		if exitNonZero {
			os.Exit(1)
		}
		return nil
	}

	if status.PermsRepaired {
		fmt.Println("forge upgrade: repaired .beads permissions to 0700")
	}

	if len(status.Updated) == 0 {
		fmt.Printf("forge upgrade: infrastructure is current (v%d)\n", status.Version)
		return nil
	}

	fmt.Printf("forge upgrade: updated %d files to infrastructure v%d\n", len(status.Updated), status.Version)
	for _, path := range status.Updated {
		fmt.Printf("  %s\n", path)
	}
	return nil
}

func currentWorkingDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	return wd, nil
}

func gitConfig(key string) string {
	output, err := exec.Command("git", "config", "--global", key).Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}

func isInteractiveSession() bool {
	stdinInfo, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}

	return (stdinInfo.Mode()&os.ModeCharDevice) != 0 && (stdoutInfo.Mode()&os.ModeCharDevice) != 0
}

type terminalPrompter struct{}

func (p terminalPrompter) Ask(_ string, label string, choices []string, defaultValue string) (string, error) {
	if len(choices) > 0 {
		return p.askSelect(label, choices, defaultValue)
	}
	return p.askText(label, defaultValue)
}

func (p terminalPrompter) askSelect(label string, choices []string, defaultValue string) (string, error) {
	// Seed the target variable so huh positions the cursor on the default.
	selected := defaultValue
	if selected == "" {
		selected = choices[0]
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(label).
				Options(huh.NewOptions(choices...)...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		return "", err
	}

	return selected, nil
}

func (p terminalPrompter) askText(label string, defaultValue string) (string, error) {
	value := defaultValue
	input := huh.NewInput().
		Title(label).
		Value(&value)

	form := huh.NewForm(huh.NewGroup(input))
	if err := form.Run(); err != nil {
		return "", err
	}

	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultValue, nil
	}

	return trimmed, nil
}

func isUserAbort(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, huh.ErrUserAborted)
}
