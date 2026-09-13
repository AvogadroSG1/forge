package prompt

import (
	"strings"
	"testing"

	"forge/internal/project"
)

type promptCall struct {
	name         string
	label        string
	choices      []string
	defaultValue string
}

type stubPrompter struct {
	responses map[string]string
	calls     []promptCall
}

func (p *stubPrompter) Ask(name string, label string, choices []string, defaultValue string) (string, error) {
	p.calls = append(p.calls, promptCall{
		name:         name,
		label:        label,
		choices:      append([]string(nil), choices...),
		defaultValue: defaultValue,
	})
	if response, ok := p.responses[name]; ok {
		return response, nil
	}
	return defaultValue, nil
}

func TestResolveRequiresMissingFlagsWithoutATTY(t *testing.T) {
	t.Parallel()

	_, err := Resolve(Inputs{IsTTY: false}, nil)
	if err == nil || err.Error() != "missing required flag: --project-name" {
		t.Fatalf("Resolve() error = %v, want missing project-name", err)
	}
}

func TestResolveRejectsUnsupportedStacks(t *testing.T) {
	t.Parallel()

	_, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "typescript",
		ProjectType: "cli",
		Stack:       "ts-cli",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "none",
		IsTTY:       false,
	}, nil)
	if err == nil {
		t.Fatal("Resolve() error = nil, want invalid language error")
	}
}

func TestResolveBuildsTheApprovedNonInteractiveContract(t *testing.T) {
	t.Parallel()

	resolved, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "none",
		IsTTY:       false,
	}, nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.Remote != project.RemoteNone {
		t.Fatalf("Remote = %q, want %q", resolved.Remote, project.RemoteNone)
	}
}

func TestResolvePromptsForGitHubUserWhenRemoteIsGH(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{
		"remote":      "gh",
		"github-user": "octocat",
	}}
	resolved, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		IsTTY:       true,
	}, prompter)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.GitHubUser != "octocat" {
		t.Fatalf("GitHubUser = %q, want octocat", resolved.GitHubUser)
	}
	if !promptedFor(prompter.calls, "github-user") {
		t.Fatalf("Resolve() prompts = %#v, want github-user prompt", prompter.calls)
	}
}

func TestResolveSkipsGitHubUserPromptWhenRemoteIsNotGH(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{
		"remote": "none",
	}}
	resolved, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		IsTTY:       true,
	}, prompter)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.GitHubUser != "" {
		t.Fatalf("GitHubUser = %q, want empty", resolved.GitHubUser)
	}
	if promptedFor(prompter.calls, "github-user") {
		t.Fatalf("Resolve() prompts = %#v, should not ask for github-user", prompter.calls)
	}
}

func TestGitHubUserPrompt(t *testing.T) {
	baseInputs := Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "gh",
		IsTTY:       true,
	}

	t.Run("configured_via_input_skips_prompt", func(t *testing.T) {
		inputs := baseInputs
		inputs.GitHubUser = "configured-user"

		prompter := &stubPrompter{responses: map[string]string{}}
		resolved, err := Resolve(inputs, prompter)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if promptedFor(prompter.calls, "github-user") {
			t.Fatalf("Resolve() prompts = %#v, should not ask for github-user", prompter.calls)
		}
		if resolved.GitHubUser != "configured-user" {
			t.Fatalf("GitHubUser = %q, want configured-user", resolved.GitHubUser)
		}
	})

	t.Run("configured_via_env_skips_prompt", func(t *testing.T) {
		t.Setenv("GITHUB_USER", "env-user")

		inputs := baseInputs
		inputs.GitHubUser = ""

		prompter := &stubPrompter{responses: map[string]string{}}
		resolved, err := Resolve(inputs, prompter)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if promptedFor(prompter.calls, "github-user") {
			t.Fatalf("Resolve() prompts = %#v, should not ask for github-user", prompter.calls)
		}
		if resolved.GitHubUser != "env-user" {
			t.Fatalf("GitHubUser = %q, want env-user", resolved.GitHubUser)
		}
	})

	t.Run("unconfigured_prompts_user", func(t *testing.T) {
		t.Setenv("GITHUB_USER", "")
		t.Setenv("GH_USER", "")
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

		inputs := baseInputs
		inputs.GitHubUser = ""

		prompter := &stubPrompter{responses: map[string]string{
			"github-user": "prompted-user",
		}}
		resolved, err := Resolve(inputs, prompter)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if !promptedFor(prompter.calls, "github-user") {
			t.Fatalf("Resolve() prompts = %#v, want github-user prompt", prompter.calls)
		}
		if resolved.GitHubUser != "prompted-user" {
			t.Fatalf("GitHubUser = %q, want prompted-user", resolved.GitHubUser)
		}
	})
}

func TestGitHubOrgPrompt(t *testing.T) {
	baseInputs := Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "gh",
		GitHubUser:  "AvogadroSG1",
		IsTTY:       true,
	}

	t.Run("configured_via_input_skips_prompt", func(t *testing.T) {
		inputs := baseInputs
		inputs.GitHubOrg = "StackEng"

		prompter := &stubPrompter{responses: map[string]string{}}
		resolved, err := Resolve(inputs, prompter)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if promptedForOrg(prompter.calls) {
			t.Fatalf("Resolve() prompts = %#v, should not ask for github-org", prompter.calls)
		}
		if resolved.GitHubOrg != "StackEng" {
			t.Fatalf("GitHubOrg = %q, want StackEng", resolved.GitHubOrg)
		}
	})

	t.Run("interactive_selects_stackeng", func(t *testing.T) {
		inputs := baseInputs
		inputs.GitHubOrg = ""

		prompter := &stubPrompter{responses: map[string]string{
			"github-org": "StackEng",
			"org":        "StackEng",
		}}
		resolved, err := Resolve(inputs, prompter)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if !promptedForOrg(prompter.calls) {
			t.Fatalf("Resolve() prompts = %#v, want github-org prompt", prompter.calls)
		}
		if resolved.GitHubOrg != "StackEng" {
			t.Fatalf("GitHubOrg = %q, want StackEng", resolved.GitHubOrg)
		}
	})

	t.Run("interactive_selects_personal_account_with_username", func(t *testing.T) {
		inputs := baseInputs
		inputs.GitHubOrg = ""

		prompter := &stubPrompter{responses: map[string]string{
			"github-org": "Personal (AvogadroSG1)",
			"org":        "Personal (AvogadroSG1)",
		}}
		resolved, err := Resolve(inputs, prompter)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if !promptedForOrg(prompter.calls) {
			t.Fatalf("Resolve() prompts = %#v, want github-org prompt", prompter.calls)
		}
		if resolved.GitHubOrg != "" {
			t.Fatalf("GitHubOrg = %q, want empty for personal account", resolved.GitHubOrg)
		}
	})

	t.Run("interactive_selects_personal", func(t *testing.T) {
		inputs := baseInputs
		inputs.GitHubOrg = ""

		prompter := &stubPrompter{responses: map[string]string{
			"github-org": "Personal",
			"org":        "Personal",
		}}
		resolved, err := Resolve(inputs, prompter)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if !promptedForOrg(prompter.calls) {
			t.Fatalf("Resolve() prompts = %#v, want github-org prompt", prompter.calls)
		}
		if resolved.GitHubOrg != "" {
			t.Fatalf("GitHubOrg = %q, want empty for personal account", resolved.GitHubOrg)
		}
	})
}

func promptedForOrg(calls []promptCall) bool {
	return promptedFor(calls, "github-org") || promptedFor(calls, "org")
}

func promptedFor(calls []promptCall, name string) bool {
	for _, call := range calls {
		if call.name == name {
			return true
		}
	}

	return false
}

func TestResolveAsksFrontendOnlyForFullstackAPIBackends(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{
		"frontend": "vite-ts",
		"remote":   "none",
	}}
	resolved, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "fullstack",
		Stack:       "go-api-chi",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		IsTTY:       true,
	}, prompter)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Frontend != "vite-ts" {
		t.Fatalf("Frontend = %q, want vite-ts", resolved.Frontend)
	}
	if !promptedFor(prompter.calls, "frontend") {
		t.Fatalf("Resolve() prompts = %#v, want frontend prompt", prompter.calls)
	}
	for _, call := range prompter.calls {
		if call.name == "frontend" {
			want := []string{"vite-ts", "sveltekit", "angular"}
			if len(call.choices) != len(want) {
				t.Fatalf("frontend choices = %#v, want %#v", call.choices, want)
			}
		}
	}
}

func TestResolveSkipsFrontendPromptForNativeFullstackStacks(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{
		"remote": "none",
	}}
	resolved, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "fullstack",
		Stack:       "go-web-templ",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		IsTTY:       true,
	}, prompter)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Frontend != "" {
		t.Fatalf("Frontend = %q, want empty", resolved.Frontend)
	}
	if promptedFor(prompter.calls, "frontend") {
		t.Fatalf("Resolve() prompts = %#v, should not ask for frontend", prompter.calls)
	}
}

func TestResolveRequiresFrontendFlagWithoutATTY(t *testing.T) {
	t.Parallel()

	_, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "fullstack",
		Stack:       "go-api-chi",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "none",
		IsTTY:       false,
	}, nil)
	if err == nil || err.Error() != "missing required flag: --frontend" {
		t.Fatalf("Resolve() error = %v, want missing frontend", err)
	}
}

func TestResolveRejectsFrontendFlagOutsideFullstackBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		inputs Inputs
	}{
		{
			name: "cli project",
			inputs: Inputs{
				ProjectName: "Sample App",
				Language:    "go",
				ProjectType: "cli",
				Stack:       "go-cli-cobra",
				Frontend:    "vite-ts",
				AuthorName:  "Ada",
				AuthorEmail: "ada@example.com",
				Remote:      "none",
			},
		},
		{
			name: "native fullstack stack",
			inputs: Inputs{
				ProjectName: "Sample App",
				Language:    "python",
				ProjectType: "fullstack",
				Stack:       "python-web-jinja",
				Frontend:    "angular",
				AuthorName:  "Ada",
				AuthorEmail: "ada@example.com",
				Remote:      "none",
			},
		},
		{
			name: "frontend-only project",
			inputs: Inputs{
				ProjectName: "Sample App",
				Language:    "typescript",
				Stack:       "vite-ts",
				Frontend:    "vite-ts",
				APIBaseURL:  "https://api.example.com",
				AuthorName:  "Ada",
				AuthorEmail: "ada@example.com",
				Remote:      "none",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(tt.inputs, nil)
			if err == nil || !strings.Contains(err.Error(), "--frontend only applies") {
				t.Fatalf("Resolve() error = %v, want frontend context error", err)
			}
		})
	}
}

func TestResolveAutoSelectsTheSoleProjectTypeForTypescript(t *testing.T) {
	t.Parallel()

	resolved, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "typescript",
		Stack:       "vite-ts",
		APIBaseURL:  "https://api.example.com",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "none",
		IsTTY:       false,
	}, nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.ProjectType != "frontend" {
		t.Fatalf("ProjectType = %q, want frontend", resolved.ProjectType)
	}
}

func TestResolveRequiresAPIBaseURLForFrontendWithoutATTY(t *testing.T) {
	t.Parallel()

	_, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "typescript",
		Stack:       "sveltekit",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "none",
		IsTTY:       false,
	}, nil)
	if err == nil || err.Error() != "missing required flag: --api-base-url" {
		t.Fatalf("Resolve() error = %v, want missing api-base-url", err)
	}
}

func TestResolvePromptsAPIBaseURLWithLocalhostDefault(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{
		"language":     "typescript",
		"stack":        "angular",
		"project-name": "Sample App",
		"remote":       "none",
	}}
	resolved, err := Resolve(Inputs{
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		IsTTY:       true,
	}, prompter)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.APIBaseURL != "http://localhost:8080" {
		t.Fatalf("APIBaseURL = %q, want the localhost default", resolved.APIBaseURL)
	}
	if promptedFor(prompter.calls, "project-type") {
		t.Fatalf("Resolve() prompts = %#v, should auto-select the sole project type", prompter.calls)
	}
}

func TestResolveRejectsAPIBaseURLForBackendOnlyProjects(t *testing.T) {
	t.Parallel()

	_, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "api",
		Stack:       "go-api-chi",
		APIBaseURL:  "https://api.example.com",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "none",
		IsTTY:       false,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "--api-base-url only applies") {
		t.Fatalf("Resolve() error = %v, want api-base-url context error", err)
	}
}

func TestResolveAcceptsAPIBaseURLOverrideForFullstack(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{}}
	resolved, err := Resolve(Inputs{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "fullstack",
		Stack:       "go-api-chi",
		Frontend:    "sveltekit",
		APIBaseURL:  "https://api.example.com",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "none",
		IsTTY:       true,
	}, prompter)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.APIBaseURL != "https://api.example.com" {
		t.Fatalf("APIBaseURL = %q, want the override", resolved.APIBaseURL)
	}
	if promptedFor(prompter.calls, "api-base-url") {
		t.Fatalf("Resolve() prompts = %#v, should not ask for api-base-url on fullstack", prompter.calls)
	}
}

func TestResolveValidatesAPIBaseURL(t *testing.T) {
	t.Parallel()

	frontendInputs := func(apiBaseURL string) Inputs {
		return Inputs{
			ProjectName: "Sample App",
			Language:    "typescript",
			Stack:       "vite-ts",
			APIBaseURL:  apiBaseURL,
			AuthorName:  "Ada",
			AuthorEmail: "ada@example.com",
			Remote:      "none",
		}
	}

	t.Run("rejects values with quotes or backslashes", func(t *testing.T) {
		for _, value := range []string{
			"https://api.example.com/'",
			`https://api.example.com/"`,
			`https://api.example.com\path`,
			"https://api.example.com/`",
		} {
			_, err := Resolve(frontendInputs(value), nil)
			if err == nil || !strings.Contains(err.Error(), "invalid --api-base-url") {
				t.Fatalf("Resolve(%q) error = %v, want invalid api-base-url", value, err)
			}
		}
	})

	t.Run("rejects non-URLs and non-http schemes", func(t *testing.T) {
		for _, value := range []string{"not a url", "ftp://api.example.com", "localhost:8080"} {
			_, err := Resolve(frontendInputs(value), nil)
			if err == nil || !strings.Contains(err.Error(), "invalid --api-base-url") {
				t.Fatalf("Resolve(%q) error = %v, want invalid api-base-url", value, err)
			}
		}
	})

	t.Run("accepts absolute http urls and trims trailing slashes", func(t *testing.T) {
		resolved, err := Resolve(frontendInputs("https://api.example.com/"), nil)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if resolved.APIBaseURL != "https://api.example.com" {
			t.Fatalf("APIBaseURL = %q, want trailing slash trimmed", resolved.APIBaseURL)
		}
	})

	t.Run("validates the fullstack override too", func(t *testing.T) {
		_, err := Resolve(Inputs{
			ProjectName: "Sample App",
			Language:    "go",
			ProjectType: "fullstack",
			Stack:       "go-api-chi",
			Frontend:    "vite-ts",
			APIBaseURL:  "https://api.example.com/'",
			AuthorName:  "Ada",
			AuthorEmail: "ada@example.com",
			Remote:      "none",
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid --api-base-url") {
			t.Fatalf("Resolve() error = %v, want invalid api-base-url", err)
		}
	})
}

func TestResolveSkipsPythonPackagePromptWhenDerivedNameIsValid(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{
		"remote": "none",
	}}
	resolved, err := Resolve(Inputs{
		ProjectName: "My Cool API",
		Language:    "python",
		ProjectType: "api",
		Stack:       "python-fastapi",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		IsTTY:       true,
	}, prompter)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.PythonPackageOverride != "" {
		t.Fatalf("PythonPackageOverride = %q, want empty (derived name is valid)", resolved.PythonPackageOverride)
	}
	if promptedFor(prompter.calls, "python-package") {
		t.Fatalf("Resolve() prompts = %#v, should not ask for python-package", prompter.calls)
	}
}

func TestResolveRequiresPythonPackageOverrideWithoutATTYWhenDerivedNameIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := Resolve(Inputs{
		ProjectName: "3D Printer",
		Language:    "python",
		ProjectType: "api",
		Stack:       "python-fastapi",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		Remote:      "none",
		IsTTY:       false,
	}, nil)
	if err == nil || err.Error() != "missing required flag: --python-package" {
		t.Fatalf("Resolve() error = %v, want missing python-package", err)
	}
}

func TestResolvePromptsForPythonPackageOverrideWhenDerivedNameIsInvalid(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{
		"remote":         "none",
		"python-package": "printer3d",
	}}
	resolved, err := Resolve(Inputs{
		ProjectName: "3D Printer",
		Language:    "python",
		ProjectType: "api",
		Stack:       "python-fastapi",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		IsTTY:       true,
	}, prompter)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.PythonPackageOverride != "printer3d" {
		t.Fatalf("PythonPackageOverride = %q, want %q", resolved.PythonPackageOverride, "printer3d")
	}
	if !promptedFor(prompter.calls, "python-package") {
		t.Fatalf("Resolve() prompts = %#v, want python-package prompt", prompter.calls)
	}
}

func TestResolveRejectsAnInvalidPythonPackageOverride(t *testing.T) {
	t.Parallel()

	prompter := &stubPrompter{responses: map[string]string{
		"remote":         "none",
		"python-package": "3d-printer",
	}}
	_, err := Resolve(Inputs{
		ProjectName: "3D Printer",
		Language:    "python",
		ProjectType: "api",
		Stack:       "python-fastapi",
		AuthorName:  "Ada",
		AuthorEmail: "ada@example.com",
		IsTTY:       true,
	}, prompter)
	if err == nil || !strings.Contains(err.Error(), "python package override") {
		t.Fatalf("Resolve() error = %v, want invalid python package override error", err)
	}
}
