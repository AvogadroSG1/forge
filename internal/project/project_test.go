package project

import "testing"

func TestValidatePythonPackage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid underscore separated", input: "my_cool_api", wantErr: false},
		{name: "valid single word", input: "app", wantErr: false},
		{name: "valid leading underscore", input: "_private_pkg", wantErr: false},
		{name: "invalid starts with digit", input: "3d_printer", wantErr: true},
		{name: "invalid empty", input: "", wantErr: true},
		{name: "invalid uppercase", input: "MyPackage", wantErr: true},
		{name: "invalid hyphen", input: "my-package", wantErr: true},
		{name: "invalid dot", input: "my.package", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePythonPackage(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePythonPackage(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestResolveVariablesDerivesCanonicalNames(t *testing.T) {
	vars, err := ResolveVariables(Input{
		ProjectName: "My Cool API",
		Language:    "Go",
		ProjectType: "API",
		Stack:       "go-api-chi",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		GitHubUser:  "octocat",
		Remote:      RemoteGH,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	if vars.BdPrefix != "mycoolapi" {
		t.Fatalf("BdPrefix = %q, want %q", vars.BdPrefix, "mycoolapi")
	}
	if vars.ModulePath != "github.com/octocat/my-cool-api" {
		t.Fatalf("ModulePath = %q, want %q", vars.ModulePath, "github.com/octocat/my-cool-api")
	}
	if vars.PythonPackage != "my_cool_api" {
		t.Fatalf("PythonPackage = %q, want %q", vars.PythonPackage, "my_cool_api")
	}
	if vars.CSharpNamespace != "MyCoolApi" {
		t.Fatalf("CSharpNamespace = %q, want %q", vars.CSharpNamespace, "MyCoolApi")
	}
	if vars.RepoSlug != "my-cool-api" {
		t.Fatalf("RepoSlug = %q, want %q", vars.RepoSlug, "my-cool-api")
	}
}

func TestResolveVariablesUsesPlaceholderModulePathWithoutGitHubRemote(t *testing.T) {
	vars, err := ResolveVariables(Input{
		ProjectName: "CLI Helper",
		Language:    "python",
		ProjectType: "cli",
		Stack:       "python-cli-typer",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      RemoteNone,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	if vars.ModulePath != "github.com/your-org/cli-helper" {
		t.Fatalf("ModulePath = %q, want placeholder path", vars.ModulePath)
	}
}

func TestResolveVariablesRequiresThePromptSeedValues(t *testing.T) {
	_, err := ResolveVariables(Input{
		ProjectName: "   ",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
	})
	if err == nil {
		t.Fatal("ResolveVariables() error = nil, want error")
	}
}

func TestResolveVariablesDerivesFrontendWiring(t *testing.T) {
	tests := []struct {
		name           string
		stack          string
		frontend       string
		apiBaseURL     string
		wantPort       string
		wantAPIBaseURL string
	}{
		{
			name:           "fullstack go derives the chi port",
			stack:          "go-api-chi",
			frontend:       "vite-ts",
			wantPort:       "8080",
			wantAPIBaseURL: "http://localhost:8080",
		},
		{
			name:           "fullstack python derives the uvicorn port",
			stack:          "python-fastapi",
			frontend:       "sveltekit",
			wantPort:       "8000",
			wantAPIBaseURL: "http://localhost:8000",
		},
		{
			name:           "fullstack csharp derives the kestrel port",
			stack:          "csharp-webapi",
			frontend:       "angular",
			wantPort:       "5000",
			wantAPIBaseURL: "http://localhost:5000",
		},
		{
			name:           "explicit api base url wins",
			stack:          "go-api-chi",
			frontend:       "vite-ts",
			apiBaseURL:     "https://api.example.com",
			wantPort:       "8080",
			wantAPIBaseURL: "https://api.example.com",
		},
		{
			name:           "standalone frontend keeps the provided url",
			stack:          "vite-ts",
			apiBaseURL:     "https://api.example.com",
			wantPort:       "",
			wantAPIBaseURL: "https://api.example.com",
		},
		{
			name:           "backend without a frontend derives nothing",
			stack:          "go-api-chi",
			wantPort:       "8080",
			wantAPIBaseURL: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars, err := ResolveVariables(Input{
				ProjectName: "My Cool App",
				Language:    "go",
				ProjectType: "fullstack",
				Stack:       tt.stack,
				Frontend:    tt.frontend,
				APIBaseURL:  tt.apiBaseURL,
				AuthorName:  "Ada Lovelace",
				AuthorEmail: "ada@example.com",
			})
			if err != nil {
				t.Fatalf("ResolveVariables() error = %v", err)
			}

			if vars.BackendPort != tt.wantPort {
				t.Fatalf("BackendPort = %q, want %q", vars.BackendPort, tt.wantPort)
			}
			if vars.APIBaseURL != tt.wantAPIBaseURL {
				t.Fatalf("APIBaseURL = %q, want %q", vars.APIBaseURL, tt.wantAPIBaseURL)
			}
			if vars.NpmPackage != "my-cool-app" {
				t.Fatalf("NpmPackage = %q, want %q", vars.NpmPackage, "my-cool-app")
			}
		})
	}
}

func TestResolveVariablesAppliesPythonPackageOverride(t *testing.T) {
	t.Parallel()

	vars, err := ResolveVariables(Input{
		ProjectName:           "StackOverflow.CostInvestigator",
		Language:              "python",
		ProjectType:           "service",
		Stack:                 "python-fastapi",
		AuthorName:            "Ada Lovelace",
		AuthorEmail:           "ada@example.com",
		Remote:                RemoteNone,
		PythonPackageOverride: "cost_investigator",
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}
	if vars.PythonPackage != "cost_investigator" {
		t.Fatalf("PythonPackage = %q, want %q", vars.PythonPackage, "cost_investigator")
	}
}

func TestResolveVariablesWithGitHubOrg(t *testing.T) {
	t.Parallel()

	vars, err := ResolveVariables(Input{
		ProjectName: "Sample App",
		Language:    "go",
		ProjectType: "cli",
		Stack:       "go-cli-cobra",
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		GitHubUser:  "octocat",
		GitHubOrg:   "StackEng",
		Remote:      RemoteGH,
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}

	if vars.GitHubOrg != "StackEng" {
		t.Fatalf("GitHubOrg = %q, want %q", vars.GitHubOrg, "StackEng")
	}
	if vars.ModulePath != "github.com/StackEng/sample-app" {
		t.Fatalf("ModulePath = %q, want %q", vars.ModulePath, "github.com/StackEng/sample-app")
	}
	if vars.GoModule != "github.com/StackEng/sample-app" {
		t.Fatalf("GoModule = %q, want %q", vars.GoModule, "github.com/StackEng/sample-app")
	}
}
