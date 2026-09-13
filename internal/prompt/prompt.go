package prompt

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"forge/internal/catalog"
	"forge/internal/project"
)

type Prompter interface {
	Ask(name string, prompt string, choices []string, defaultValue string) (string, error)
}

type Inputs struct {
	ProjectName string
	Language    string
	ProjectType string
	Stack       string
	Frontend    string
	APIBaseURL  string
	AuthorName  string
	AuthorEmail string
	GitHubUser  string
	GitHubOrg   string
	Remote      string
	RemoteURL   string
	ModulePath  string
	BdPrefix    string
	IsTTY       bool

	PythonPackageOverride string
}

func Resolve(input Inputs, prompter Prompter) (project.Input, error) {
	projectName, err := resolveValue(input.ProjectName, input.IsTTY, prompter, "project-name", "Project name", nil, "")
	if err != nil {
		return project.Input{}, err
	}

	languageChoices := catalog.Languages()
	language, err := resolveValue(input.Language, input.IsTTY, prompter, "language", "Language", languageChoices, "")
	if err != nil {
		return project.Input{}, err
	}
	language = normalize(language)
	if !contains(languageChoices, language) {
		return project.Input{}, fmt.Errorf("invalid --language %q (valid choices: %s)", language, strings.Join(languageChoices, ", "))
	}

	projectTypeChoices := catalog.ProjectTypes(language)
	var projectType string
	if len(projectTypeChoices) == 1 && strings.TrimSpace(input.ProjectType) == "" {
		// A question with exactly one answer is never asked: the sole
		// project type auto-selects silently, even without a TTY.
		projectType = projectTypeChoices[0]
	} else {
		projectType, err = resolveValue(input.ProjectType, input.IsTTY, prompter, "project-type", "Project type", projectTypeChoices, "")
		if err != nil {
			return project.Input{}, err
		}
	}
	projectType = normalize(projectType)
	if !contains(projectTypeChoices, projectType) {
		return project.Input{}, fmt.Errorf("invalid --project-type %q (valid choices: %s)", projectType, strings.Join(projectTypeChoices, ", "))
	}

	stackChoices := make([]string, 0)
	for _, stack := range catalog.SelectableStacks(language, projectType) {
		stackChoices = append(stackChoices, stack.Key)
	}
	stack, err := resolveValue(input.Stack, input.IsTTY, prompter, "stack", "Stack", stackChoices, "")
	if err != nil {
		return project.Input{}, err
	}
	stack = strings.TrimSpace(stack)
	if !contains(stackChoices, stack) {
		return project.Input{}, fmt.Errorf("invalid --stack %q (valid choices: %s)", stack, strings.Join(stackChoices, ", "))
	}

	frontend, err := resolveFrontend(input, prompter, projectType, stack)
	if err != nil {
		return project.Input{}, err
	}

	apiBaseURL, err := resolveAPIBaseURL(input, prompter, projectType)
	if err != nil {
		return project.Input{}, err
	}

	authorName, err := resolveValue(input.AuthorName, input.IsTTY, prompter, "author-name", "Author name", nil, "")
	if err != nil {
		return project.Input{}, err
	}
	authorEmail, err := resolveValue(input.AuthorEmail, input.IsTTY, prompter, "author-email", "Author email", nil, "")
	if err != nil {
		return project.Input{}, err
	}

	remoteChoices := []string{"gh", "url", "none"}
	remoteValue, err := resolveValue(input.Remote, input.IsTTY, prompter, "remote", "Remote", remoteChoices, "gh")
	if err != nil {
		return project.Input{}, err
	}
	remoteValue = normalize(remoteValue)
	if !contains(remoteChoices, remoteValue) {
		return project.Input{}, fmt.Errorf("invalid --remote %q (valid choices: %s)", remoteValue, strings.Join(remoteChoices, ", "))
	}

	gitHubUser := strings.TrimSpace(input.GitHubUser)
	if gitHubUser == "" {
		gitHubUser = DefaultGitHubUser()
	}
	if remoteValue == string(project.RemoteGH) && gitHubUser == "" {
		gitHubUser, err = resolveValue(input.GitHubUser, input.IsTTY, prompter, "github-user", "GitHub user", nil, "")
		if err != nil {
			return project.Input{}, err
		}
		gitHubUser = strings.TrimSpace(gitHubUser)
	}
	if remoteValue != string(project.RemoteGH) {
		gitHubUser = ""
	}

	var gitHubOrg string
	if remoteValue == string(project.RemoteGH) {
		if strings.TrimSpace(input.GitHubOrg) != "" {
			gitHubOrg = strings.TrimSpace(input.GitHubOrg)
		} else if input.IsTTY {
			if prompter == nil {
				return project.Input{}, fmt.Errorf("interactive prompt requested for --github-org but no prompter is available")
			}
			personalChoice := "Personal"
			if gitHubUser != "" {
				personalChoice = fmt.Sprintf("Personal (%s)", gitHubUser)
			}
			orgChoices := []string{personalChoice, "StackEng"}
			seen := map[string]bool{"stackeng": true}
			for _, o := range ghUserOrgs() {
				lower := strings.ToLower(o)
				if !seen[lower] && o != "" {
					seen[lower] = true
					orgChoices = append(orgChoices, o)
				}
			}
			orgChoices = append(orgChoices, "Custom")

			selectedOrg, err := prompter.Ask("github-org", "GitHub organization", orgChoices, personalChoice)
			if err != nil {
				return project.Input{}, err
			}
			selectedOrg = strings.TrimSpace(selectedOrg)
			if selectedOrg == "Personal" || strings.HasPrefix(selectedOrg, "Personal (") {
				gitHubOrg = ""
			} else if selectedOrg == "Custom" {
				customOrg, err := resolveValue("", input.IsTTY, prompter, "github-org", "Custom GitHub organization", nil, "")
				if err != nil {
					return project.Input{}, err
				}
				customOrg = strings.TrimSpace(customOrg)
				if customOrg == "Personal" || strings.HasPrefix(customOrg, "Personal (") {
					gitHubOrg = ""
				} else {
					gitHubOrg = customOrg
				}
			} else {
				gitHubOrg = selectedOrg
			}
		}
	}

	resolved := project.Input{
		ProjectName: projectName,
		Language:    language,
		ProjectType: projectType,
		Stack:       stack,
		Frontend:    frontend,
		APIBaseURL:  apiBaseURL,
		AuthorName:  authorName,
		AuthorEmail: authorEmail,
		GitHubUser:  gitHubUser,
		GitHubOrg:   gitHubOrg,
		Remote:      project.RemoteKind(remoteValue),
		RemoteURL:   strings.TrimSpace(input.RemoteURL),
		ModulePath:  strings.TrimSpace(input.ModulePath),
		BdPrefix:    strings.TrimSpace(input.BdPrefix),

		PythonPackageOverride: strings.TrimSpace(input.PythonPackageOverride),
	}
	if resolved.Remote == project.RemoteURL && resolved.RemoteURL == "" {
		return project.Input{}, fmt.Errorf("missing required flag: --remote-url")
	}

	if language == "python" {
		pythonPkg := derivedPythonPackage(projectName)
		if err := project.ValidatePythonPackage(pythonPkg); err != nil {
			override, promptErr := resolveValue(input.PythonPackageOverride, input.IsTTY, prompter, "python-package", "Python package name (derived name is invalid)", nil, "")
			if promptErr != nil {
				return project.Input{}, promptErr
			}
			if err := project.ValidatePythonPackage(override); err != nil {
				return project.Input{}, fmt.Errorf("python package override %q: %w", override, err)
			}
			resolved.PythonPackageOverride = override
		}
	}

	return resolved, nil
}

// nonAlphaNum matches runs of characters outside [a-z0-9], used to slugify
// project names into python package names. Compiled once at package init
// rather than on every derivedPythonPackage call.
var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

// derivedPythonPackage mirrors the slugification ResolveVariables performs
// when no override is supplied, so Resolve can validate the name that would
// actually be used before it reaches ResolveVariables.
func derivedPythonPackage(projectName string) string {
	words := strings.Fields(nonAlphaNum.ReplaceAllString(strings.ToLower(projectName), " "))
	return strings.Join(words, "_")
}

// resolveFrontend asks the frontend question only when a fullstack project
// picked an api backend; a native fullstack stack IS the fullstack, so the
// question is skipped. --frontend anywhere else fails loudly.
func resolveFrontend(input Inputs, prompter Prompter, projectType, stackKey string) (string, error) {
	stack, _ := catalog.Get(stackKey)
	wantsFragment := projectType == "fullstack" && stack.FullstackBackend

	frontendChoices := make([]string, 0)
	for _, frontend := range catalog.FrontendStacks() {
		frontendChoices = append(frontendChoices, frontend.Key)
	}

	if !wantsFragment {
		if strings.TrimSpace(input.Frontend) != "" {
			return "", fmt.Errorf("--frontend only applies to fullstack projects on an api backend stack (valid backends: go-api-chi, python-fastapi, csharp-webapi)")
		}
		return "", nil
	}

	frontend, err := resolveValue(input.Frontend, input.IsTTY, prompter, "frontend", "Frontend", frontendChoices, "")
	if err != nil {
		return "", err
	}
	frontend = normalize(frontend)
	if !contains(frontendChoices, frontend) {
		return "", fmt.Errorf("invalid --frontend %q (valid choices: %s)", frontend, strings.Join(frontendChoices, ", "))
	}

	return frontend, nil
}

// resolveAPIBaseURL asks for the API base URL only on frontend-only
// projects, where the generated client must point at an external API. For
// fullstack projects the URL is derived from the backend; --api-base-url is
// accepted silently as an override. Anywhere else the flag fails loudly.
func resolveAPIBaseURL(input Inputs, prompter Prompter, projectType string) (string, error) {
	switch projectType {
	case "frontend":
		value, err := resolveValue(input.APIBaseURL, input.IsTTY, prompter, "api-base-url", "API base URL", nil, "http://localhost:8080")
		if err != nil {
			return "", err
		}
		return validateAPIBaseURL(value)
	case "fullstack":
		value := strings.TrimSpace(input.APIBaseURL)
		if value == "" {
			return "", nil
		}
		return validateAPIBaseURL(value)
	default:
		if strings.TrimSpace(input.APIBaseURL) != "" {
			return "", fmt.Errorf("--api-base-url only applies to frontend and fullstack projects")
		}
		return "", nil
	}
}

// validateAPIBaseURL rejects malformed input at the boundary so the value is
// safe by construction when rendered into a generated TS string literal,
// then trims trailing slashes (the clients append rooted paths).
func validateAPIBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "'\"\\`") {
		return "", fmt.Errorf("invalid --api-base-url %q (must be an absolute http(s) URL)", value)
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid --api-base-url %q (must be an absolute http(s) URL)", value)
	}

	return strings.TrimRight(value, "/"), nil
}

func resolveValue(current string, isTTY bool, prompter Prompter, flagName string, label string, choices []string, defaultValue string) (string, error) {
	current = strings.TrimSpace(current)
	if current != "" {
		return current, nil
	}
	if !isTTY {
		return "", fmt.Errorf("missing required flag: --%s", flagName)
	}
	if prompter == nil {
		return "", fmt.Errorf("interactive prompt requested for --%s but no prompter is available", flagName)
	}

	return prompter.Ask(flagName, label, choices, defaultValue)
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}

	return false
}

// DefaultGitHubUser resolves the GitHub username from environment variables,
// git configuration, or gh CLI authentication.
func DefaultGitHubUser() string {
	if u := strings.TrimSpace(os.Getenv("GITHUB_USER")); u != "" {
		return u
	}
	if u := strings.TrimSpace(os.Getenv("GH_USER")); u != "" {
		return u
	}
	if out, err := exec.Command("git", "config", "--global", "github.user").Output(); err == nil {
		if u := strings.TrimSpace(string(out)); u != "" {
			return u
		}
	}
	if u := ghAuthUser(); u != "" {
		return u
	}
	return ""
}

var ghAuthUser = defaultGHAuthUser

func defaultGHAuthUser() string {
	if testing.Testing() {
		return ""
	}
	out, err := exec.Command("gh", "api", "user", "-q", ".login").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var ghUserOrgs = defaultGHUserOrgs

func defaultGHUserOrgs() []string {
	if testing.Testing() {
		return nil
	}
	out, err := exec.Command("gh", "api", "user/orgs", "-q", ".[].login").Output()
	if err != nil {
		return nil
	}
	var orgs []string
	for _, line := range strings.Split(string(out), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			orgs = append(orgs, trimmed)
		}
	}
	return orgs
}


