package initcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"forge/internal/delegate"
	"forge/internal/project"
	"forge/internal/remote"
	"forge/internal/scaffold"
	"forge/internal/upgrade"
)

type Initializer struct {
	Writer scaffold.Writer
	Runner delegate.Runner
}

func (i Initializer) Run(ctx context.Context, targetDir string, vars project.Variables) error {
	if err := scaffoldEnsureEmptyDir(targetDir); err != nil {
		return failWithRecovery(targetDir, "empty-directory precondition", err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return failWithRecovery(targetDir, "target directory setup", err)
	}

	if err := i.Runner.Run(ctx, targetDir, "git init", "git", "init", "-b", "main"); err != nil {
		return failWithRecovery(targetDir, "git init", err)
	}
	if err := i.Runner.Run(ctx, targetDir, "git identity name", "git", "config", "user.name", vars.AuthorName); err != nil {
		return failWithRecovery(targetDir, "git identity name", err)
	}
	if err := i.Runner.Run(ctx, targetDir, "git identity email", "git", "config", "user.email", vars.AuthorEmail); err != nil {
		return failWithRecovery(targetDir, "git identity email", err)
	}

	if err := i.Writer.Write(targetDir, vars); err != nil {
		return failWithRecovery(targetDir, "phase 1 scaffold writer", err)
	}

	// Stamp the manifest + legacy marker now, before Phase 3's git add/commit,
	// so a freshly scaffolded repo is born at the current infrastructure
	// version instead of v0 (defect A) and its init params are captured for
	// later re-rendering by `forge upgrade` (defect E, wired in a later unit).
	if err := upgrade.Stamp(targetDir, upgrade.Manifest{
		Language:        vars.Language,
		Frontend:        vars.Frontend,
		IncludePersonal: vars.IncludePersonal,
		Stack:           vars.Stack,
	}); err != nil {
		return failWithRecovery(targetDir, "infra version stamp", err)
	}

	skills, err := readSeedSkills(i.Writer.Assets, vars.Language)
	if err != nil {
		return failWithRecovery(targetDir, "skill seed render", err)
	}

	steps := []struct {
		name    string
		command string
		args    []string
	}{
		{name: "bd init", command: "bd", args: []string{"init"}},
		{name: "mise trust", command: "mise", args: []string{"trust", "--all"}},
		{name: "mise install", command: "mise", args: []string{"install"}},
		{name: "lefthook install", command: "mise", args: []string{"exec", "--", "lefthook", "install", "--force"}},
	}

	for _, step := range steps {
		if err := i.Runner.Run(ctx, targetDir, step.name, step.command, step.args...); err != nil {
			return failWithRecovery(targetDir, step.name, err)
		}
		if step.name == "bd init" {
			if _, err := upgrade.EnsureBeadsDirPerms(targetDir); err != nil {
				return failWithRecovery(targetDir, "beads permissions", err)
			}
			if err := i.Runner.Run(ctx, targetDir, "instill bootstrap", "instill", "bootstrap"); err != nil {
				// APM bootstrap needs Homebrew; a repo without skills is still
				// usable and self-heals later via `instill bootstrap && instill sync`.
				fmt.Fprintf(os.Stderr, "skipping skill setup: instill bootstrap failed: %v\n", err)
			} else {
				initializedSkills, err := runInstillInit(ctx, i.Runner, targetDir, skills)
				if err != nil {
					return failWithRecovery(targetDir, "instill init", err)
				}
				if len(initializedSkills) > 0 {
					if err := i.Runner.Run(ctx, targetDir, "instill pick plugin", "instill", "pick", "--type", "plugin", "peters-bdd-orchestrator"); err != nil {
						return failWithRecovery(targetDir, "instill pick plugin", err)
					}
					if err := i.Runner.Run(ctx, targetDir, "instill sync", "instill", "sync"); err != nil {
						return failWithRecovery(targetDir, "instill sync", err)
					}
				}
			}
		}
		if step.name == "mise install" {
			switch vars.Language {
			case "go":
				if err := i.Runner.Run(ctx, targetDir, "go mod tidy", "go", "mod", "tidy"); err != nil {
					return failWithRecovery(targetDir, "go mod tidy", err)
				}
			case "python":
				if err := i.Runner.Run(ctx, targetDir, "pip install", "mise", "exec", "--", "pip", "install", "-e", ".[dev]"); err != nil {
					return failWithRecovery(targetDir, "pip install", err)
				}
			case "typescript":
				if err := i.Runner.Run(ctx, targetDir, "npm install", "mise", "exec", "--", "npm", "install"); err != nil {
					return failWithRecovery(targetDir, "npm install", err)
				}
			}
			if vars.Frontend != "" {
				if err := i.Runner.Run(ctx, targetDir, "npm install (web)", "mise", "exec", "--", "npm", "--prefix", "web", "install"); err != nil {
					return failWithRecovery(targetDir, "npm install (web)", err)
				}
			}
		}
		if step.name == "lefthook install" {
			if err := repairBeadsHookChain(targetDir); err != nil {
				return failWithRecovery(targetDir, "lefthook chain repair", err)
			}
			if err := i.Runner.Run(ctx, targetDir, "fitness functions onboard", "agent-fitness-functions", "client", "onboard", "--enforcement", "advisory"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: agent-fitness-functions onboarding failed: %v\n", err)
			}
		}
	}

	if err := cleanupEmptyToolDirs(targetDir); err != nil {
		return failWithRecovery(targetDir, "tool directory cleanup", err)
	}

	if err := remote.Publish(ctx, i.Runner, targetDir, remote.PublishOptions{
		RepoName: vars.RepoSlug,
		Org:      vars.GitHubOrg,
		Remote:   vars.Remote,
		URL:      vars.RemoteURL,
	}); err != nil {
		return failWithRecovery(targetDir, "phase 3 remote publish", err)
	}

	return nil
}

type skillManifest struct {
	Skills []string `json:"skills"`
}

func readSeedSkills(assets fs.FS, language string) ([]string, error) {
	data, err := fs.ReadFile(assets, "templates/seed/skills.json.tmpl")
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("skills.json.tmpl").Option("missingkey=error").Parse(string(data))
	if err != nil {
		return nil, err
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct{ Language string }{Language: strings.TrimSpace(language)}); err != nil {
		return nil, err
	}

	var manifest skillManifest
	if err := json.Unmarshal(rendered.Bytes(), &manifest); err != nil {
		return nil, fmt.Errorf("parse skill seed: %w", err)
	}
	if len(manifest.Skills) == 0 {
		return nil, fmt.Errorf("skill seed is empty")
	}

	return manifest.Skills, nil
}

func runInstillInit(ctx context.Context, runner delegate.Runner, targetDir string, skills []string) ([]string, error) {
	if err := runner.Run(ctx, targetDir, "instill init", "instill", "init", "--force", "--skills", strings.Join(skills, ","), "--targets", "claude,codex,opencode"); err == nil {
		return skills, nil
	} else if len(skills) == 1 {
		return nil, err
	}

	// `instill init --force` overwrites apm.yml on every call, so the per-skill
	// probes below only discover which skills resolve; one final combined init
	// writes the manifest containing the full working set.
	initialized := make([]string, 0, len(skills))
	for _, skill := range skills {
		stepName := fmt.Sprintf("instill init (%s)", skill)
		if skillErr := runner.Run(ctx, targetDir, stepName, "instill", "init", "--force", "--skills", skill, "--targets", "claude,codex,opencode"); skillErr != nil {
			continue
		}
		initialized = append(initialized, skill)
	}
	if len(initialized) == 0 {
		return nil, nil
	}

	if err := runner.Run(ctx, targetDir, "instill init (retry)", "instill", "init", "--force", "--skills", strings.Join(initialized, ","), "--targets", "claude,codex,opencode"); err != nil {
		return nil, err
	}

	return initialized, nil
}

func repairBeadsHookChain(targetDir string) error {
	hooksDir := filepath.Join(targetDir, ".beads", "hooks")
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".old") {
			continue
		}
		if err := repairBeadsHook(hooksDir, entry.Name()); err != nil {
			return err
		}
	}

	return nil
}

func repairBeadsHook(hooksDir string, oldName string) error {
	hookName := strings.TrimSuffix(oldName, ".old")
	hookPath := filepath.Join(hooksDir, hookName)
	lefthookPath := hookPath + ".lefthook"

	if _, err := os.Stat(hookPath); err != nil {
		return err
	}
	if _, err := os.Stat(lefthookPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(hookPath, lefthookPath); err != nil {
		return err
	}

	wrapper := chainedHookWrapper(hookName)
	return os.WriteFile(hookPath, []byte(wrapper), 0o755)
}

func chainedHookWrapper(hookName string) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname "$0")" && pwd)"
"$script_dir/%[1]s.old" "$@"
exec "$script_dir/%[1]s.lefthook" "$@"
`, hookName)
}

func cleanupEmptyToolDirs(targetDir string) error {
	candidates := []string{"claude", "codex", "opencode"}
	for _, candidate := range candidates {
		candidateDir := filepath.Join(targetDir, candidate)
		info, err := os.Stat(candidateDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(candidateDir)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(candidateDir); err != nil {
				return err
			}
		}
	}
	return nil
}

func failWithRecovery(targetDir string, step string, err error) error {
	return fmt.Errorf("init failed at step %q: %w\nRecovery: delete the directory recursively and retry: %s", step, err, targetDir)
}

func scaffoldEnsureEmptyDir(targetDir string) error {
	info, err := os.Stat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", targetDir)
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".DS_Store" {
			continue
		}
		return fmt.Errorf("directory not empty: %s", targetDir)
	}

	return nil
}
