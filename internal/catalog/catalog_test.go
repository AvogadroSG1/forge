package catalog

import (
	"reflect"
	"testing"
)

func TestV1StacksReturnsOnlyTheShippedStacks(t *testing.T) {
	want := []Stack{
		{Key: "go-cli-cobra", Language: "go", ProjectType: "cli", AgentAddons: []string{"pi"}},
		{Key: "go-api-chi", Language: "go", ProjectType: "api", FullstackBackend: true, AgentAddons: []string{"pi"}},
		{Key: "python-cli-typer", Language: "python", ProjectType: "cli"},
		{Key: "python-fastapi", Language: "python", ProjectType: "api", FullstackBackend: true},
		{Key: "csharp-cli", Language: "csharp", ProjectType: "cli"},
		{Key: "csharp-webapi", Language: "csharp", ProjectType: "api", FullstackBackend: true},
		{Key: "go-web-templ", Language: "go", ProjectType: "fullstack"},
		{Key: "python-web-jinja", Language: "python", ProjectType: "fullstack"},
		{Key: "csharp-blazor", Language: "csharp", ProjectType: "fullstack"},
		{Key: "vite-ts", Language: "typescript", ProjectType: "frontend"},
		{Key: "sveltekit", Language: "typescript", ProjectType: "frontend"},
		{Key: "angular", Language: "typescript", ProjectType: "frontend"},
	}

	got := V1Stacks()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("V1Stacks() = %#v, want %#v", got, want)
	}
}

func TestLanguagesReturnsThePickerOrder(t *testing.T) {
	want := []string{"go", "python", "csharp", "typescript"}

	if got := Languages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Languages() = %#v, want %#v", got, want)
	}
}

func TestProjectTypesFiltersByLanguage(t *testing.T) {
	tests := []struct {
		language string
		want     []string
	}{
		{"go", []string{"cli", "api", "fullstack"}},
		{"Python", []string{"cli", "api", "fullstack"}},
		{"csharp", []string{"cli", "api", "fullstack"}},
		{"typescript", []string{"frontend"}},
		{"rust", nil},
	}

	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			if got := ProjectTypes(tt.language); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ProjectTypes(%q) = %#v, want %#v", tt.language, got, tt.want)
			}
		})
	}
}

func TestSelectableStacksFiltersTheV1Boundary(t *testing.T) {
	tests := []struct {
		name        string
		language    string
		projectType string
		wantKeys    []string
	}{
		{
			name:        "go cli",
			language:    "Go",
			projectType: "CLI",
			wantKeys:    []string{"go-cli-cobra"},
		},
		{
			name:        "python api",
			language:    "python",
			projectType: "api",
			wantKeys:    []string{"python-fastapi"},
		},
		{
			name:        "csharp all project types",
			language:    "csharp",
			projectType: "",
			wantKeys:    []string{"csharp-cli", "csharp-webapi", "csharp-blazor"},
		},
		{
			name:        "go fullstack offers the api backend and the native stack",
			language:    "go",
			projectType: "fullstack",
			wantKeys:    []string{"go-api-chi", "go-web-templ"},
		},
		{
			name:        "python fullstack offers the api backend and the native stack",
			language:    "python",
			projectType: "fullstack",
			wantKeys:    []string{"python-fastapi", "python-web-jinja"},
		},
		{
			name:        "csharp fullstack offers the api backend and the native stack",
			language:    "csharp",
			projectType: "fullstack",
			wantKeys:    []string{"csharp-webapi", "csharp-blazor"},
		},
		{
			name:        "typescript frontend stacks",
			language:    "TypeScript",
			projectType: "frontend",
			wantKeys:    []string{"vite-ts", "sveltekit", "angular"},
		},
		{
			name:        "typescript has no cli stacks",
			language:    "TypeScript",
			projectType: "CLI",
			wantKeys:    nil,
		},
		{
			name:        "unsupported rust stack",
			language:    "rust",
			projectType: "api",
			wantKeys:    nil,
		},
		{
			name:        "unsupported bash stack",
			language:    "bash",
			projectType: "util",
			wantKeys:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectableStacks(tt.language, tt.projectType)
			gotKeys := stackKeys(got)

			if !reflect.DeepEqual(gotKeys, tt.wantKeys) {
				t.Fatalf("SelectableStacks(%q, %q) = %#v, want %#v", tt.language, tt.projectType, gotKeys, tt.wantKeys)
			}
		})
	}
}

func TestFrontendStacksReturnsTheFragmentChoices(t *testing.T) {
	want := []string{"vite-ts", "sveltekit", "angular"}

	if got := stackKeys(FrontendStacks()); !reflect.DeepEqual(got, want) {
		t.Fatalf("FrontendStacks() = %#v, want %#v", got, want)
	}
}

func TestGetLooksUpStacksByKey(t *testing.T) {
	stack, ok := Get("go-api-chi")
	if !ok {
		t.Fatal("Get(go-api-chi) not found")
	}
	if !stack.FullstackBackend || stack.Language != "go" || stack.ProjectType != "api" {
		t.Fatalf("Get(go-api-chi) = %#v", stack)
	}

	if _, ok := Get("nope"); ok {
		t.Fatal("Get(nope) found an unknown stack")
	}
}

func stackKeys(stacks []Stack) []string {
	if len(stacks) == 0 {
		return nil
	}

	keys := make([]string, 0, len(stacks))
	for _, stack := range stacks {
		keys = append(keys, stack.Key)
	}

	return keys
}

func TestAgentAddonStacksListsTheStacksThatOfferAnAddon(t *testing.T) {
	if got, want := AgentAddonStacks("pi"), []string{"go-cli-cobra", "go-api-chi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AgentAddonStacks(pi) = %#v, want %#v", got, want)
	}
	if got := AgentAddonStacks("unknown"); len(got) != 0 {
		t.Fatalf("AgentAddonStacks(unknown) = %#v, want none", got)
	}
}
