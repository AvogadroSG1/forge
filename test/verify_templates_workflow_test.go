package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestVerifyTemplatesWorkflowDefinesRequiredTriggersAndJobs(t *testing.T) {
	repoRoot := repoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "verify-templates.yml")

	contentBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}

	content := string(contentBytes)
	for _, snippet := range []string{
		"name: Verify Templates",
		"push:",
		"pull_request:",
		"schedule:",
		"- cron: '17 4 * * *'",
		"workflow_dispatch:",
		"fast-gate:",
		"slow-gate:",
		"github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'",
		"fail-fast: false",
		"stack: [go-cli-cobra, python-cli-typer, csharp-cli, go-api-chi, python-fastapi, csharp-webapi]",
		"go-version-file: go.mod",
		"run: make verify-fast",
		`-run "TestLocalRelease/${{ matrix.stack }}" ./test/`,
	} {
		if !strings.Contains(content, snippet) {
			t.Fatalf("workflow missing %q\n%s", snippet, content)
		}
	}
}

func TestVerifyTemplatesWorkflowIsValidYAML(t *testing.T) {
	repoRoot := repoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "verify-templates.yml")

	contentBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}

	var workflow map[string]any
	if err := yaml.Unmarshal(contentBytes, &workflow); err != nil {
		t.Fatalf("parse %s: %v", workflowPath, err)
	}
}
