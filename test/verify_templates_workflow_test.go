package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		"uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"uses: actions/setup-go@d8cbb8045d7f43706b9177cceb36c1f91a6644d5 # v7.1.0",
		"go-version-file: go.mod",
		"uses: jdx/mise-action@236a21c3b91f5db6d4e174b348674a0f0caac06d # v5.2.9",
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
