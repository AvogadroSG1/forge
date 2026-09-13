package project

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type RemoteKind string

const (
	RemoteGH   RemoteKind = "gh"
	RemoteURL  RemoteKind = "url"
	RemoteNone RemoteKind = "none"
)

type Input struct {
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
	Remote      RemoteKind
	RemoteURL   string
	ModulePath  string
	BdPrefix    string

	PythonPackageOverride string
}

type Variables struct {
	ProjectName     string
	Language        string
	ProjectType     string
	Stack           string
	Frontend        string
	APIBaseURL      string
	BackendPort     string
	NpmPackage      string
	AuthorName      string
	AuthorEmail     string
	GitHubOrg       string
	Remote          RemoteKind
	RemoteURL       string
	BdPrefix        string
	ModulePath      string
	GoModule        string
	PythonPackage   string
	CSharpNamespace string
	RepoSlug        string
	IncludePersonal bool
}

// backendPorts maps each stack that serves HTTP to the port its walking
// skeleton listens on, so the frontend API client default can point at it.
var backendPorts = map[string]string{
	"go-api-chi":       "8080",
	"go-web-templ":     "8080",
	"python-fastapi":   "8000",
	"python-web-jinja": "8000",
	"csharp-webapi":    "5000",
	"csharp-blazor":    "5000",
}

var nonAlphaNumeric = regexp.MustCompile(`[^a-z0-9]+`)

var validPythonIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func ValidatePythonPackage(name string) error {
	if name == "" {
		return fmt.Errorf("python package name is empty")
	}
	if !validPythonIdentifier.MatchString(name) {
		return fmt.Errorf("python package name %q is not a valid Python identifier (must match [a-z_][a-z0-9_]*)", name)
	}
	return nil
}

func ResolveVariables(input Input) (Variables, error) {
	projectName := strings.TrimSpace(input.ProjectName)
	if projectName == "" {
		return Variables{}, fmt.Errorf("project name is required")
	}
	if strings.TrimSpace(input.Language) == "" {
		return Variables{}, fmt.Errorf("language is required")
	}
	if strings.TrimSpace(input.ProjectType) == "" {
		return Variables{}, fmt.Errorf("project type is required")
	}
	if strings.TrimSpace(input.Stack) == "" {
		return Variables{}, fmt.Errorf("stack is required")
	}
	if strings.TrimSpace(input.AuthorName) == "" || strings.TrimSpace(input.AuthorEmail) == "" {
		return Variables{}, fmt.Errorf("author identity is required")
	}

	slugWords := slugWords(projectName)
	if len(slugWords) == 0 {
		return Variables{}, fmt.Errorf("project name %q does not contain usable characters", projectName)
	}

	bdPrefix := strings.TrimSpace(input.BdPrefix)
	if bdPrefix == "" {
		bdPrefix = strings.Join(slugWords, "")
	}

	modulePath := strings.TrimSpace(input.ModulePath)
	if modulePath == "" {
		if strings.TrimSpace(input.GitHubOrg) != "" {
			modulePath = fmt.Sprintf("github.com/%s/%s", strings.TrimSpace(input.GitHubOrg), slugKebab(slugWords))
		} else if input.Remote == RemoteGH && strings.TrimSpace(input.GitHubUser) != "" {
			modulePath = fmt.Sprintf("github.com/%s/%s", strings.TrimSpace(input.GitHubUser), slugKebab(slugWords))
		} else {
			modulePath = fmt.Sprintf("github.com/your-org/%s", slugKebab(slugWords))
		}
	}

	stack := strings.TrimSpace(input.Stack)
	backendPort := backendPorts[stack]
	apiBaseURL := strings.TrimSpace(input.APIBaseURL)
	if apiBaseURL == "" && strings.TrimSpace(input.Frontend) != "" && backendPort != "" {
		apiBaseURL = fmt.Sprintf("http://localhost:%s", backendPort)
	}

	return Variables{
		ProjectName:     projectName,
		Language:        normalize(input.Language),
		ProjectType:     normalize(input.ProjectType),
		Stack:           stack,
		Frontend:        strings.TrimSpace(input.Frontend),
		APIBaseURL:      apiBaseURL,
		BackendPort:     backendPort,
		NpmPackage:      slugKebab(slugWords),
		AuthorName:      strings.TrimSpace(input.AuthorName),
		AuthorEmail:     strings.TrimSpace(input.AuthorEmail),
		GitHubOrg:       strings.TrimSpace(input.GitHubOrg),
		Remote:          input.Remote,
		RemoteURL:       strings.TrimSpace(input.RemoteURL),
		BdPrefix:        bdPrefix,
		ModulePath:      modulePath,
		GoModule:        modulePath,
		PythonPackage:   derivePythonPackage(slugWords, input.PythonPackageOverride),
		CSharpNamespace: pascalCase(slugWords),
		RepoSlug:        slugKebab(slugWords),
		IncludePersonal: false,
	}, nil
}

func derivePythonPackage(slugWords []string, override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	return strings.Join(slugWords, "_")
}

func slugWords(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonAlphaNumeric.ReplaceAllString(value, " ")
	parts := strings.Fields(value)
	if len(parts) == 0 {
		return nil
	}

	return parts
}

func slugKebab(words []string) string {
	return strings.Join(words, "-")
}

func pascalCase(words []string) string {
	var b strings.Builder
	for _, word := range words {
		if word == "" {
			continue
		}
		runes := []rune(word)
		b.WriteRune(unicode.ToUpper(runes[0]))
		for _, r := range runes[1:] {
			b.WriteRune(unicode.ToLower(r))
		}
	}

	return b.String()
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
