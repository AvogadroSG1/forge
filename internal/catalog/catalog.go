// Package catalog defines the executable v1 stack boundary for forge.
package catalog

import "strings"

var v1Stacks = []Stack{
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

// Stack identifies one shippable forge stack in the v1 catalog.
type Stack struct {
	Key         string
	Language    string
	ProjectType string
	// FullstackBackend marks api stacks that can host a JS frontend
	// fragment under web/ when the user picks the fullstack project type.
	FullstackBackend bool
	// AgentAddons lists the opt-in agent add-ons (templates/addons/<name>)
	// that can be layered onto this stack with --agent. Empty means the
	// stack offers none and the question is never asked.
	AgentAddons []string
}

// V1Stacks returns the shipped v1 catalog in stable picker order.
func V1Stacks() []Stack {
	stacks := make([]Stack, len(v1Stacks))
	copy(stacks, v1Stacks)

	return stacks
}

// Languages returns the selectable languages in stable picker order.
func Languages() []string {
	seen := make(map[string]bool)
	var languages []string
	for _, stack := range v1Stacks {
		if seen[stack.Language] {
			continue
		}
		seen[stack.Language] = true
		languages = append(languages, stack.Language)
	}

	return languages
}

// ProjectTypes returns the project types selectable for a language in
// stable picker order. FullstackBackend api stacks make "fullstack"
// selectable in addition to their own project type.
func ProjectTypes(language string) []string {
	language = normalize(language)

	seen := make(map[string]bool)
	var types []string
	add := func(projectType string) {
		if seen[projectType] {
			return
		}
		seen[projectType] = true
		types = append(types, projectType)
	}

	for _, stack := range v1Stacks {
		if language != "" && stack.Language != language {
			continue
		}
		add(stack.ProjectType)
		if stack.FullstackBackend {
			add("fullstack")
		}
	}

	return types
}

// SelectableStacks returns the v1 stacks that match the requested language
// and project type. For projectType "fullstack" the match includes api
// stacks flagged FullstackBackend, so the picker offers both the native
// fullstack stack and the api backends that can host a frontend fragment.
func SelectableStacks(language, projectType string) []Stack {
	language = normalize(language)
	projectType = normalize(projectType)

	var stacks []Stack
	for _, stack := range v1Stacks {
		if language != "" && stack.Language != language {
			continue
		}
		if projectType != "" && !matchesProjectType(stack, projectType) {
			continue
		}

		stacks = append(stacks, stack)
	}

	return stacks
}

// FrontendStacks returns the stacks usable as a standalone frontend repo or
// as a fullstack frontend fragment under web/.
func FrontendStacks() []Stack {
	return SelectableStacks("typescript", "frontend")
}

// AgentAddonStacks returns the keys of the stacks that offer the named agent
// add-on, in stable picker order.
func AgentAddonStacks(addon string) []string {
	addon = normalize(addon)

	var keys []string
	for _, stack := range v1Stacks {
		for _, candidate := range stack.AgentAddons {
			if candidate == addon {
				keys = append(keys, stack.Key)
			}
		}
	}

	return keys
}

// Get looks a stack up by key.
func Get(key string) (Stack, bool) {
	key = normalize(key)
	for _, stack := range v1Stacks {
		if stack.Key == key {
			return stack, true
		}
	}

	return Stack{}, false
}

func matchesProjectType(stack Stack, projectType string) bool {
	if stack.ProjectType == projectType {
		return true
	}

	return projectType == "fullstack" && stack.FullstackBackend
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
