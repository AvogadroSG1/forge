package remote

import (
	"context"
	"fmt"
	"strings"

	"forge/internal/delegate"
	"forge/internal/project"
)

type PublishOptions struct {
	RepoName string
	Org      string
	Remote   project.RemoteKind
	URL      string
}

func Publish(ctx context.Context, runner delegate.Runner, dir string, options PublishOptions) error {
	if err := runner.Run(ctx, dir, "git add", "git", "add", "."); err != nil {
		return err
	}
	if err := runner.Run(ctx, dir, "git commit", "git", "commit", "-m", "chore: scaffold project"); err != nil {
		return err
	}

	switch options.Remote {
	case project.RemoteNone:
		return nil
	case project.RemoteURL:
		if strings.TrimSpace(options.URL) == "" {
			return fmt.Errorf("remote url is required when --remote url is selected")
		}
		if err := runner.Run(ctx, dir, "git remote add origin", "git", "remote", "add", "origin", options.URL); err != nil {
			return err
		}
		return runner.Run(ctx, dir, "git push", "git", "push", "-u", "origin", "main")
	case project.RemoteGH:
		if strings.TrimSpace(options.RepoName) == "" {
			return fmt.Errorf("repository name is required for --remote gh")
		}
		repoTarget := strings.TrimSpace(options.RepoName)
		if strings.TrimSpace(options.Org) != "" && !strings.Contains(options.RepoName, "/") {
			repoTarget = fmt.Sprintf("%s/%s", strings.TrimSpace(options.Org), strings.TrimSpace(options.RepoName))
		}
		if err := runner.Run(ctx, dir, "gh repo create", "gh", "repo", "create", repoTarget, "--source=.", "--remote=origin", "--private", "--push"); err != nil {
			return fmt.Errorf("gh repo create failed: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported remote %q", options.Remote)
	}
}
