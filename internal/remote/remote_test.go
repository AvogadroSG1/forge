package remote

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge/internal/project"
)

func TestPublishRemoteNoneOnlyCommitsLocally(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{}
	err := Publish(context.Background(), runner, t.TempDir(), PublishOptions{Remote: project.RemoteNone})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	want := []string{"git add", "git commit"}
	if got := runner.stepNames(); !equalStrings(got, want) {
		t.Fatalf("steps = %#v, want %#v", got, want)
	}
}

func TestPublishRemoteURLRequiresAURL(t *testing.T) {
	t.Parallel()

	err := Publish(context.Background(), &recordingRunner{}, t.TempDir(), PublishOptions{Remote: project.RemoteURL})
	if err == nil || !strings.Contains(err.Error(), "remote url is required") {
		t.Fatalf("Publish() error = %v, want missing URL error", err)
	}
}

func TestPublishRemoteGHReportsCreateFailure(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{failStep: "gh repo create"}
	err := Publish(context.Background(), runner, t.TempDir(), PublishOptions{Remote: project.RemoteGH, RepoName: "sample-app"})
	if err == nil {
		t.Fatal("Publish() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "gh repo create failed") {
		t.Fatalf("Publish() error = %v, want create failure message", err)
	}
}

func TestPublishRemoteGHPassesPushFlag(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{}
	err := Publish(context.Background(), runner, t.TempDir(), PublishOptions{Remote: project.RemoteGH, RepoName: "sample-app"})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	want := []string{"git add", "git commit", "gh repo create"}
	if got := runner.stepNames(); !equalStrings(got, want) {
		t.Fatalf("steps = %#v, want %#v", got, want)
	}
}

func TestPublishRemoteGHWithOrg(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{}
	err := Publish(context.Background(), runner, t.TempDir(), PublishOptions{
		Remote:   project.RemoteGH,
		RepoName: "sample-app",
		Org:      "StackEng",
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	var found bool
	for _, call := range runner.calls {
		if call.command == "gh" && len(call.args) >= 3 && call.args[0] == "repo" && call.args[1] == "create" {
			found = true
			if call.args[2] != "StackEng/sample-app" {
				t.Fatalf("gh repo create target = %q, want %q", call.args[2], "StackEng/sample-app")
			}
		}
	}
	if !found {
		t.Fatal("gh repo create was not executed")
	}
}

type runCall struct {
	step    string
	command string
	args    []string
}

type recordingRunner struct {
	failStep string
	steps    []string
	calls    []runCall
}

func (r *recordingRunner) Run(_ context.Context, _ string, step string, command string, args ...string) error {
	r.steps = append(r.steps, step)
	r.calls = append(r.calls, runCall{step: step, command: command, args: append([]string(nil), args...)})
	if step == r.failStep {
		return errors.New("boom")
	}

	return nil
}

func (r *recordingRunner) stepNames() []string {
	return append([]string(nil), r.steps...)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}
