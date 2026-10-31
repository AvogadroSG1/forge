package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"forge/internal/project"

	"go.yaml.in/yaml/v3"
)

type CommandRunner interface {
	Run(ctx context.Context, dir string, step string, command string, args ...string) error
}

type GitRunner interface {
	Clone(ctx context.Context, repo string, dir string) error
	Checkout(ctx context.Context, dir string, ref string) error
}

type ExecGitRunner struct{}

type sourceRow struct {
	Kind      string          `yaml:"kind"`
	GitIgnore string          `yaml:"gitignore"`
	Steps     []sourceStep    `yaml:"steps"`
	Normalize []normalizeRule `yaml:"normalize"`
	Resolved  resolvedSource  `yaml:"resolved"`
}

type sourceStep struct {
	Run      string   `yaml:"run,omitempty"`
	Checkout string   `yaml:"checkout,omitempty"`
	Ref      string   `yaml:"ref,omitempty"`
	Strip    []string `yaml:"strip,omitempty"`
}

type normalizeRule struct {
	Type  string   `yaml:"type"`
	Paths []string `yaml:"paths,omitempty"`
}

type resolvedSource struct {
	Ref      string `yaml:"ref"`
	Captured string `yaml:"captured"`
}

type seamCheckReport struct {
	Collisions []string
}

type templateContract struct {
	templatePaths map[string]string
	sentinelFiles []sentinelFile
}

type sentinelFile struct {
	path string
	data []byte
}

var (
	guidPattern           = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	replaceVanillaWriter  = os.WriteFile
	replaceVanillaRenamer = os.Rename
)

func Run(ctx context.Context, assets fs.FS, stack string, runner CommandRunner, git GitRunner) error {
	if strings.TrimSpace(stack) == "" {
		return fmt.Errorf("update stack key is required")
	}
	if runner == nil {
		return fmt.Errorf("update command runner is required")
	}
	if git == nil {
		return fmt.Errorf("update git runner is required")
	}

	rows, err := loadSources(assets)
	if err != nil {
		return err
	}
	row, ok := rows[stack]
	if !ok {
		return fmt.Errorf("unknown update stack %q", stack)
	}
	if len(row.Steps) == 0 {
		return fmt.Errorf("stack %s has no update steps", stack)
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	vars, err := representativeVariables(stack, row)
	if err != nil {
		return err
	}
	if vars.Language != "go" {
		return fmt.Errorf("maintainer refresh currently supports Go stacks only; stack %s (%s) is captured manually — see docs/SPEC.md §15", stack, vars.Language)
	}

	workspaceRoot, err := os.MkdirTemp("", "forge-update-")
	if err != nil {
		return fmt.Errorf("create update workspace: %w", err)
	}
	defer os.RemoveAll(workspaceRoot)

	workspaceDir := filepath.Join(workspaceRoot, representativeWorkspaceName(vars))
	for index, step := range row.Steps {
		stepLabel := fmt.Sprintf("stack %s step %d", stack, index+1)
		switch {
		case step.Run != "" && step.Checkout != "":
			return fmt.Errorf("%s must choose exactly one of run or checkout", stepLabel)
		case step.Run != "":
			if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
				return fmt.Errorf("%s: create workspace: %w", stepLabel, err)
			}
			renderedCommand, err := renderString(step.Run, vars)
			if err != nil {
				return fmt.Errorf("%s: render run command %q: %w", stepLabel, step.Run, err)
			}
			command, args, err := splitCommandLine(renderedCommand)
			if err != nil {
				return fmt.Errorf("%s: parse run command %q: %w", stepLabel, renderedCommand, err)
			}
			if err := runner.Run(ctx, workspaceDir, stepLabel, command, args...); err != nil {
				return fmt.Errorf("%s command %q failed: %w", stepLabel, command, err)
			}
		case step.Checkout != "":
			repo, err := renderString(step.Checkout, vars)
			if err != nil {
				return fmt.Errorf("%s: render checkout repo %q: %w", stepLabel, step.Checkout, err)
			}
			ref, err := renderString(step.Ref, vars)
			if err != nil {
				return fmt.Errorf("%s: render checkout ref %q: %w", stepLabel, step.Ref, err)
			}
			if strings.TrimSpace(ref) == "" {
				return fmt.Errorf("%s checkout %q is missing ref", stepLabel, repo)
			}
			if _, err := os.Stat(workspaceDir); err == nil {
				entries, readErr := os.ReadDir(workspaceDir)
				if readErr != nil {
					return fmt.Errorf("%s: read workspace: %w", stepLabel, readErr)
				}
				if len(entries) > 0 {
					return fmt.Errorf("%s cannot checkout into non-empty workspace", stepLabel)
				}
			}
			if err := git.Clone(ctx, repo, workspaceDir); err != nil {
				return fmt.Errorf("%s clone %q failed: %w", stepLabel, repo, err)
			}
			if err := git.Checkout(ctx, workspaceDir, ref); err != nil {
				return fmt.Errorf("%s checkout ref %q failed: %w", stepLabel, ref, err)
			}
			if err := stripPaths(workspaceDir, step.Strip); err != nil {
				return fmt.Errorf("%s strip paths failed: %w", stepLabel, err)
			}
		default:
			return fmt.Errorf("%s must define run or checkout", stepLabel)
		}
	}

	stackRoot := filepath.Join(repoRoot, "templates", "golden", stack)
	refreshedVanilla := filepath.Join(workspaceRoot, "refreshed")
	if err := snapshotVanilla(workspaceDir, refreshedVanilla, stackRoot, row.Normalize, vars); err != nil {
		return err
	}
	vanillaChanged, err := vanillaLayerChanged(stackRoot, refreshedVanilla)
	if err != nil {
		return err
	}

	sourcesPlan, err := planMutableSourcesRepin(filepath.Join(repoRoot, "sources.yaml"), stack, row.Resolved.Ref, vanillaChanged)
	if err != nil {
		return err
	}

	seamReport, err := checkOverlaySeam(stackRoot, refreshedVanilla)
	if err != nil {
		return err
	}
	if err := sourcesPlan.apply(); err != nil {
		return err
	}
	sourcesApplied := sourcesPlan.needsWrite()
	if err := replaceVanillaLayer(stackRoot, refreshedVanilla); err != nil {
		if sourcesApplied {
			if rollbackErr := sourcesPlan.rollback(); rollbackErr != nil {
				return fmt.Errorf("replace vanilla layer: %v (rollback mutable sources: %v)", err, rollbackErr)
			}
		}
		return err
	}
	if len(seamReport.Collisions) > 0 {
		fmt.Fprintf(os.Stderr, "warning: overlay collision(s) for stack %s: %s\n", stack, strings.Join(seamReport.Collisions, ", "))
	}

	return nil
}

func (ExecGitRunner) Clone(ctx context.Context, repo string, dir string) error {
	normalizedRepo := normalizeRepo(repo)
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", normalizedRepo, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (ExecGitRunner) Checkout(ctx context.Context, dir string, ref string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "checkout", "--quiet", ref)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func loadSources(assets fs.FS) (map[string]sourceRow, error) {
	data, err := fs.ReadFile(assets, "sources.yaml")
	if err != nil {
		return nil, fmt.Errorf("read embedded sources.yaml: %w", err)
	}

	rows := map[string]sourceRow{}
	if err := yaml.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decode embedded sources.yaml: %w", err)
	}

	return rows, nil
}

func representativeVariables(stack string, row sourceRow) (project.Variables, error) {
	language, err := languageForRow(stack, row)
	if err != nil {
		return project.Variables{}, err
	}

	return project.ResolveVariables(project.Input{
		ProjectName: "Forge Template Fixture",
		Language:    language,
		ProjectType: row.Kind,
		Stack:       stack,
		AuthorName:  "Forge Maintainer",
		AuthorEmail: "maintainer@example.com",
		Remote:      project.RemoteNone,
	})
}

func languageForRow(stack string, row sourceRow) (string, error) {
	switch strings.ToLower(strings.TrimSpace(row.GitIgnore)) {
	case "go":
		return "go", nil
	case "python":
		return "python", nil
	case "visualstudio":
		return "csharp", nil
	case "node":
		return "typescript", nil
	}

	parts := strings.SplitN(strings.TrimSpace(stack), "-", 2)
	if len(parts) > 0 && parts[0] != "" {
		return parts[0], nil
	}

	return "", fmt.Errorf("infer language for stack %q", stack)
}

func representativeWorkspaceName(vars project.Variables) string {
	switch vars.Language {
	case "csharp":
		return vars.CSharpNamespace
	case "python":
		return vars.PythonPackage
	default:
		return vars.BdPrefix
	}
}

func renderString(input string, vars project.Variables) (string, error) {
	tmpl, err := template.New("update").Option("missingkey=error").Parse(input)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func snapshotVanilla(workspaceDir string, targetDir string, committedStackDir string, rules []normalizeRule, vars project.Variables) error {
	contract, err := loadTemplateContract(committedStackDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create refreshed vanilla dir: %w", err)
	}

	writtenDirs := map[string]struct{}{".": {}}
	writtenFiles := map[string]struct{}{}
	if err := filepath.WalkDir(workspaceDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(workspaceDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		committedRelPath := filepath.ToSlash(relPath)
		committedRelPath = applyTemplatePlaceholdersToPath(committedRelPath, vars)
		if d.IsDir() {
			writtenDirs[committedRelPath] = struct{}{}
			return os.MkdirAll(filepath.Join(targetDir, filepath.FromSlash(committedRelPath)), 0o755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read refreshed file %q: %w", committedRelPath, err)
		}
		normalized, err := normalizeFile(committedRelPath, data, rules)
		if err != nil {
			return fmt.Errorf("normalize %q: %w", committedRelPath, err)
		}
		templated := applyTemplatePlaceholders(normalized, vars)
		if contractPath, ok := contract.templatePaths[renderedCatalogPath(committedRelPath)]; ok {
			committedRelPath = contractPath
		} else if !bytes.Equal(templated, normalized) {
			committedRelPath += ".tmpl"
		}

		targetPath := filepath.Join(targetDir, filepath.FromSlash(committedRelPath))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("prepare refreshed file %q: %w", committedRelPath, err)
		}
		if err := os.WriteFile(targetPath, templated, 0o644); err != nil {
			return fmt.Errorf("write refreshed file %q: %w", committedRelPath, err)
		}
		writtenFiles[committedRelPath] = struct{}{}
		markContractParents(writtenDirs, committedRelPath)
		return nil
	}); err != nil {
		return err
	}

	for _, sentinel := range contract.sentinelFiles {
		parent := filepath.ToSlash(filepath.Dir(sentinel.path))
		if parent == "" {
			parent = "."
		}
		if _, ok := writtenDirs[parent]; !ok {
			continue
		}
		if _, ok := writtenFiles[sentinel.path]; ok {
			continue
		}

		targetPath := filepath.Join(targetDir, filepath.FromSlash(sentinel.path))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("prepare refreshed sentinel %q: %w", sentinel.path, err)
		}
		if err := os.WriteFile(targetPath, sentinel.data, 0o644); err != nil {
			return fmt.Errorf("write refreshed sentinel %q: %w", sentinel.path, err)
		}
	}

	return nil
}

func loadTemplateContract(committedStackDir string) (templateContract, error) {
	contract := templateContract{templatePaths: map[string]string{}}
	if _, err := os.Stat(committedStackDir); errors.Is(err, fs.ErrNotExist) {
		return contract, nil
	} else if err != nil {
		return templateContract{}, fmt.Errorf("stat committed stack dir: %w", err)
	}

	err := filepath.WalkDir(committedStackDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(committedStackDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		relPath = filepath.ToSlash(relPath)
		if relPath == ".forge-overlay" {
			return fs.SkipDir
		}
		if strings.HasPrefix(relPath, ".forge-overlay/") || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(relPath, ".tmpl") {
			contract.templatePaths[renderedCatalogPath(relPath)] = relPath
			return nil
		}
		if !isDirectorySentinelPath(relPath) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read committed sentinel %q: %w", relPath, err)
		}
		contract.sentinelFiles = append(contract.sentinelFiles, sentinelFile{path: relPath, data: append([]byte(nil), data...)})
		return nil
	})
	if err != nil {
		return templateContract{}, fmt.Errorf("walk committed stack dir: %w", err)
	}

	return contract, nil
}

func markContractParents(dirs map[string]struct{}, relPath string) {
	dir := filepath.ToSlash(filepath.Dir(relPath))
	for dir != "." && dir != "/" && dir != "" {
		dirs[dir] = struct{}{}
		dir = filepath.ToSlash(filepath.Dir(dir))
	}
	dirs["."] = struct{}{}
}

func isDirectorySentinelPath(relPath string) bool {
	switch filepath.Base(relPath) {
	case ".keep", ".gitkeep":
		return true
	default:
		return false
	}
}

func normalizeFile(relPath string, data []byte, rules []normalizeRule) ([]byte, error) {
	normalized := append([]byte(nil), data...)
	for _, rule := range rules {
		switch strings.TrimSpace(rule.Type) {
		case "", "sort_files":
			continue
		case "line_endings":
			normalized = bytes.ReplaceAll(normalized, []byte("\r\n"), []byte("\n"))
			normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
		case "trailing_newline":
			normalized = bytes.TrimRight(normalized, "\n")
			if len(normalized) > 0 {
				normalized = append(normalized, '\n')
			}
		case "replace_guid":
			if matchesAnyPath(relPath, rule.Paths) {
				normalized = guidPattern.ReplaceAll(normalized, []byte("00000000-0000-0000-0000-000000000000"))
			}
		default:
			return nil, fmt.Errorf("unsupported normalize rule %q", rule.Type)
		}
	}

	return normalized, nil
}

func matchesAnyPath(relPath string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, filepath.Base(relPath))
		if err == nil && matched {
			return true
		}
		matched, err = filepath.Match(pattern, relPath)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func applyTemplatePlaceholders(data []byte, vars project.Variables) []byte {
	type replacement struct {
		value       string
		placeholder string
	}

	seen := map[string]struct{}{}
	replacements := []replacement{
		{value: vars.ModulePath, placeholder: "{{.ModulePath}}"},
		{value: vars.ProjectName, placeholder: "{{.ProjectName}}"},
		{value: vars.BdPrefix, placeholder: "{{.BdPrefix}}"},
		{value: vars.PythonPackage, placeholder: "{{.PythonPackage}}"},
		{value: vars.CSharpNamespace, placeholder: "{{.CSharpNamespace}}"},
		{value: vars.AuthorName, placeholder: "{{.AuthorName}}"},
		{value: vars.AuthorEmail, placeholder: "{{.AuthorEmail}}"},
	}

	filtered := make([]replacement, 0, len(replacements))
	for _, candidate := range replacements {
		if candidate.value == "" {
			continue
		}
		if _, ok := seen[candidate.value]; ok {
			continue
		}
		seen[candidate.value] = struct{}{}
		filtered = append(filtered, candidate)
	}
	sort.Slice(filtered, func(i int, j int) bool {
		return len(filtered[i].value) > len(filtered[j].value)
	})

	templated := append([]byte(nil), data...)
	for _, candidate := range filtered {
		templated = bytes.ReplaceAll(templated, []byte(candidate.value), []byte(candidate.placeholder))
	}
	return templated
}

func applyTemplatePlaceholdersToPath(relPath string, vars project.Variables) string {
	type replacement struct {
		value       string
		placeholder string
	}
	replacements := []replacement{
		{value: vars.PythonPackage, placeholder: "{{.PythonPackage}}"},
		{value: vars.CSharpNamespace, placeholder: "{{.CSharpNamespace}}"},
	}
	segments := strings.Split(relPath, "/")
	for i, seg := range segments {
		for _, r := range replacements {
			if r.value != "" && seg == r.value {
				segments[i] = r.placeholder
				break
			}
		}
	}
	return strings.Join(segments, "/")
}

func checkOverlaySeam(committedStackDir string, refreshedVanilla string) (seamCheckReport, error) {
	overlayDir := filepath.Join(committedStackDir, ".forge-overlay")
	if _, err := os.Stat(overlayDir); errors.Is(err, fs.ErrNotExist) {
		return seamCheckReport{}, nil
	} else if err != nil {
		return seamCheckReport{}, fmt.Errorf("stat overlay dir: %w", err)
	}

	fileOutputs := map[string]struct{}{}
	dirOutputs := map[string]struct{}{".": {}}
	if err := filepath.WalkDir(refreshedVanilla, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(refreshedVanilla, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		rendered := renderedCatalogPath(filepath.ToSlash(relPath))
		if d.IsDir() {
			dirOutputs[rendered] = struct{}{}
			return nil
		}

		fileOutputs[rendered] = struct{}{}
		for parent := filepath.ToSlash(filepath.Dir(rendered)); parent != "." && parent != "/"; parent = filepath.ToSlash(filepath.Dir(parent)) {
			dirOutputs[parent] = struct{}{}
		}
		return nil
	}); err != nil {
		return seamCheckReport{}, fmt.Errorf("walk refreshed vanilla: %w", err)
	}

	var orphans []string
	var collisions []string
	err := filepath.WalkDir(overlayDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(overlayDir, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		rendered := renderedCatalogPath(relPath)
		parent := filepath.ToSlash(filepath.Dir(rendered))
		if parent == "" {
			parent = "."
		}
		if _, ok := dirOutputs[parent]; !ok {
			orphans = append(orphans, relPath)
		}
		if _, ok := fileOutputs[rendered]; ok {
			collisions = append(collisions, relPath)
		}
		return nil
	})
	if err != nil {
		return seamCheckReport{}, fmt.Errorf("walk overlay dir: %w", err)
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return seamCheckReport{}, fmt.Errorf("orphan overlay path(s): %s", strings.Join(orphans, ", "))
	}

	sort.Strings(collisions)
	return seamCheckReport{Collisions: collisions}, nil
}

func vanillaLayerChanged(committedStackDir string, refreshedVanilla string) (bool, error) {
	committedSnapshot, err := captureVanillaSnapshot(committedStackDir)
	if err != nil {
		return false, err
	}
	refreshedSnapshot, err := captureVanillaSnapshot(refreshedVanilla)
	if err != nil {
		return false, err
	}
	if len(committedSnapshot) != len(refreshedSnapshot) {
		return true, nil
	}
	for path, contents := range committedSnapshot {
		if refreshedSnapshot[path] != contents {
			return true, nil
		}
	}
	return false, nil
}

func captureVanillaSnapshot(root string) (map[string]string, error) {
	snapshot := map[string]string{}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return snapshot, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat vanilla root: %w", err)
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		if relPath == ".forge-overlay" {
			return fs.SkipDir
		}
		if strings.HasPrefix(relPath, ".forge-overlay/") || d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read snapshot path %q: %w", relPath, err)
		}
		snapshot[relPath] = string(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk vanilla root: %w", err)
	}
	return snapshot, nil
}

type sourcesRepinPlan struct {
	path     string
	original []byte
	updated  []byte
	mode     os.FileMode
}

func planMutableSourcesRepin(path string, stack string, resolvedRef string, vanillaChanged bool) (sourcesRepinPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sourcesRepinPlan{}, fmt.Errorf("read mutable sources.yaml: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return sourcesRepinPlan{}, fmt.Errorf("stat mutable sources.yaml: %w", err)
	}
	plan := sourcesRepinPlan{
		path:     path,
		original: append([]byte(nil), data...),
		mode:     info.Mode().Perm(),
	}

	var captured *string
	if vanillaChanged {
		value := currentDateString()
		captured = &value
	}
	updated, err := updateResolvedFields(data, stack, resolvedRef, captured)
	if err != nil {
		return sourcesRepinPlan{}, err
	}
	if bytes.Equal(data, updated) {
		return plan, nil
	}
	plan.updated = updated
	return plan, nil
}

func (p sourcesRepinPlan) needsWrite() bool {
	return p.updated != nil
}

func (p sourcesRepinPlan) apply() error {
	if !p.needsWrite() {
		return nil
	}
	if err := writeFileAtomically(p.path, p.updated, p.mode); err != nil {
		return fmt.Errorf("write mutable sources.yaml: %w", err)
	}
	return nil
}

func (p sourcesRepinPlan) rollback() error {
	if !p.needsWrite() {
		return nil
	}
	if err := writeFileAtomically(p.path, p.original, p.mode); err != nil {
		return fmt.Errorf("restore mutable sources.yaml: %w", err)
	}
	return nil
}

func updateResolvedFields(data []byte, stack string, resolvedRef string, captured *string) ([]byte, error) {
	lines := strings.SplitAfter(string(data), "\n")
	stackIndex, err := findTopLevelKeyLine(lines, stack)
	if err != nil {
		return nil, err
	}
	stackEnd := findBlockEnd(lines, stackIndex+1, 0)
	resolvedIndex, resolvedIndent, err := findChildKeyLine(lines, stackIndex+1, stackEnd, 0, "resolved")
	if err != nil {
		return nil, err
	}
	resolvedLine := lines[resolvedIndex]
	resolvedTrimmed := trimLine(resolvedLine)
	if strings.Contains(resolvedTrimmed, "{") && strings.Contains(resolvedTrimmed, "}") {
		updatedLine, err := rewriteFlowResolvedLine(resolvedLine, resolvedRef, captured)
		if err != nil {
			return nil, err
		}
		lines[resolvedIndex] = updatedLine
		return []byte(strings.Join(lines, "")), nil
	}

	resolvedEnd := findBlockEnd(lines, resolvedIndex+1, resolvedIndent)
	refIndex, _, err := findChildKeyLine(lines, resolvedIndex+1, resolvedEnd, resolvedIndent, "ref")
	if err != nil {
		return nil, err
	}
	updatedRefLine, err := rewriteScalarLine(lines[refIndex], "ref", resolvedRef)
	if err != nil {
		return nil, err
	}
	lines[refIndex] = updatedRefLine
	if captured != nil {
		capturedIndex, _, err := findChildKeyLine(lines, resolvedIndex+1, resolvedEnd, resolvedIndent, "captured")
		if err != nil {
			return nil, err
		}
		updatedCapturedLine, err := rewriteScalarLine(lines[capturedIndex], "captured", *captured)
		if err != nil {
			return nil, err
		}
		lines[capturedIndex] = updatedCapturedLine
	}
	return []byte(strings.Join(lines, "")), nil
}

func rewriteFlowResolvedLine(line string, resolvedRef string, captured *string) (string, error) {
	body, newline := splitLineEnding(line)
	trimmedLeft := strings.TrimLeft(body, " \t")
	if !strings.HasPrefix(trimmedLeft, "resolved:") {
		return "", fmt.Errorf("line %q does not contain key %q", line, "resolved")
	}
	prefixEnd := len(body) - len(trimmedLeft) + len("resolved:")
	for prefixEnd < len(body) && (body[prefixEnd] == ' ' || body[prefixEnd] == '\t') {
		prefixEnd++
	}
	prefix := body[:prefixEnd]
	rest := body[prefixEnd:]
	openIndex := strings.Index(rest, "{")
	closeIndex := strings.LastIndex(rest, "}")
	if openIndex < 0 || closeIndex < openIndex {
		return "", fmt.Errorf("resolved flow mapping missing braces")
	}
	before := rest[:openIndex+1]
	content := rest[openIndex+1 : closeIndex]
	after := rest[closeIndex:]
	updatedContent, err := rewriteFlowResolvedContent(content, resolvedRef, captured)
	if err != nil {
		return "", err
	}
	return prefix + before + updatedContent + after + newline, nil
}

func rewriteFlowResolvedContent(content string, resolvedRef string, captured *string) (string, error) {
	parts := strings.Split(content, ",")
	foundRef := false
	foundCaptured := false
	for index, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		colonIndex := strings.Index(trimmed, ":")
		if colonIndex < 0 {
			return "", fmt.Errorf("resolved flow field %q is missing ':'", trimmed)
		}
		key := strings.TrimSpace(trimmed[:colonIndex])
		switch key {
		case "ref":
			parts[index] = rewriteFlowField(part, resolvedRef)
			foundRef = true
		case "captured":
			foundCaptured = true
			if captured != nil {
				parts[index] = rewriteFlowField(part, *captured)
			}
		}
	}
	if !foundRef {
		return "", fmt.Errorf("ref missing from mutable sources.yaml stack row")
	}
	if captured != nil && !foundCaptured {
		return "", fmt.Errorf("captured missing from mutable sources.yaml stack row")
	}
	return strings.Join(parts, ","), nil
}

func rewriteFlowField(field string, value string) string {
	leadingLen := len(field) - len(strings.TrimLeft(field, " \t"))
	trailingLen := len(field) - len(strings.TrimRight(field, " \t"))
	leading := field[:leadingLen]
	trailing := field[len(field)-trailingLen:]
	trimmed := strings.TrimSpace(field)
	colonIndex := strings.Index(trimmed, ":")
	prefix := trimmed[:colonIndex+1]
	valuePart := trimmed[colonIndex+1:]
	spacingLen := len(valuePart) - len(strings.TrimLeft(valuePart, " \t"))
	spacing := valuePart[:spacingLen]
	valueToken := strings.TrimSpace(valuePart)
	if valueToken != "" && (valueToken[0] == '\'' || valueToken[0] == '"') && valueToken[len(valueToken)-1] == valueToken[0] {
		quote := string(valueToken[0])
		return leading + prefix + spacing + quote + value + quote + trailing
	}
	return leading + prefix + spacing + value + trailing
}

func findTopLevelKeyLine(lines []string, key string) (int, error) {
	for index, line := range lines {
		trimmed := trimLine(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if lineIndent(line) == 0 && trimmed == key+":" {
			return index, nil
		}
	}
	return -1, fmt.Errorf("stack %q missing from mutable sources.yaml", key)
}

func findChildKeyLine(lines []string, start int, end int, parentIndent int, key string) (int, int, error) {
	for index := start; index < end; index++ {
		trimmed := trimLine(lines[index])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := lineIndent(lines[index])
		if indent <= parentIndent {
			continue
		}
		if strings.HasPrefix(trimmed, key+":") {
			return index, indent, nil
		}
	}
	return -1, 0, fmt.Errorf("%s missing from mutable sources.yaml stack row", key)
}

func findBlockEnd(lines []string, start int, parentIndent int) int {
	for index := start; index < len(lines); index++ {
		trimmed := trimLine(lines[index])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if lineIndent(lines[index]) <= parentIndent {
			return index
		}
	}
	return len(lines)
}

func rewriteScalarLine(line string, key string, value string) (string, error) {
	body, newline := splitLineEnding(line)
	trimmedLeft := strings.TrimLeft(body, " \t")
	if !strings.HasPrefix(trimmedLeft, key+":") {
		return "", fmt.Errorf("line %q does not contain key %q", line, key)
	}

	prefixEnd := len(body) - len(trimmedLeft) + len(key) + 1
	for prefixEnd < len(body) && (body[prefixEnd] == ' ' || body[prefixEnd] == '\t') {
		prefixEnd++
	}
	prefix := body[:prefixEnd]
	rest := body[prefixEnd:]
	if rest == "" {
		return prefix + value + newline, nil
	}
	if rest[0] == '\'' || rest[0] == '"' {
		quote := rest[0]
		closing := strings.LastIndexByte(rest[1:], quote)
		if closing < 0 {
			return "", fmt.Errorf("unterminated quoted scalar for %s", key)
		}
		closing++
		suffix := rest[closing+1:]
		return prefix + string(quote) + value + string(quote) + suffix + newline, nil
	}

	suffixIndex := len(rest)
	for index, r := range rest {
		if r == ' ' || r == '\t' {
			suffixIndex = index
			break
		}
	}
	suffix := rest[suffixIndex:]
	return prefix + value + suffix + newline, nil
}

func splitLineEnding(line string) (string, string) {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return strings.TrimSuffix(line, "\r\n"), "\r\n"
	case strings.HasSuffix(line, "\n"):
		return strings.TrimSuffix(line, "\n"), "\n"
	default:
		return line, ""
	}
}

func trimLine(line string) string {
	body, _ := splitLineEnding(line)
	return strings.TrimSpace(body)
}

func lineIndent(line string) int {
	body, _ := splitLineEnding(line)
	return len(body) - len(strings.TrimLeft(body, " "))
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	tempFile, err := os.CreateTemp(filepath.Dir(path), ".forge-sources-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if mode == 0 {
		mode = 0o644
	}
	if err := tempFile.Chmod(mode); err != nil {
		_ = tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

var nowUTC = func() time.Time { return time.Now().UTC() }

func currentDateString() string {
	return currentDateStringAt(nowUTC())
}

func currentDateStringAt(now time.Time) string {
	return now.UTC().Format("2006-01-02")
}

func replaceVanillaLayer(committedStackDir string, refreshedVanilla string) error {
	parentDir := filepath.Dir(committedStackDir)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("ensure committed stack parent: %w", err)
	}

	stagedDir, err := os.MkdirTemp(parentDir, ".forge-vanilla-*")
	if err != nil {
		return fmt.Errorf("create staged vanilla dir: %w", err)
	}
	stagedActive := true
	defer func() {
		if stagedActive {
			_ = os.RemoveAll(stagedDir)
		}
	}()

	if err := copyTree(refreshedVanilla, stagedDir); err != nil {
		return err
	}
	if err := copyOverlay(filepath.Join(committedStackDir, ".forge-overlay"), filepath.Join(stagedDir, ".forge-overlay")); err != nil {
		return err
	}

	if _, err := os.Stat(committedStackDir); errors.Is(err, fs.ErrNotExist) {
		if err := replaceVanillaRenamer(stagedDir, committedStackDir); err != nil {
			return fmt.Errorf("activate staged vanilla dir: %w", err)
		}
		stagedActive = false
		return nil
	} else if err != nil {
		return fmt.Errorf("stat committed stack dir: %w", err)
	}

	backupDir := committedStackDir + ".forge-backup"
	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("clear vanilla backup dir: %w", err)
	}
	if err := replaceVanillaRenamer(committedStackDir, backupDir); err != nil {
		return fmt.Errorf("stage committed stack dir: %w", err)
	}
	backupActive := true
	defer func() {
		if backupActive {
			_ = os.RemoveAll(backupDir)
		}
	}()

	if err := replaceVanillaRenamer(stagedDir, committedStackDir); err != nil {
		if rollbackErr := replaceVanillaRenamer(backupDir, committedStackDir); rollbackErr != nil {
			return fmt.Errorf("activate staged vanilla dir: %v (restore committed stack dir: %v)", err, rollbackErr)
		}
		backupActive = false
		return fmt.Errorf("activate staged vanilla dir: %w", err)
	}
	stagedActive = false
	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("remove vanilla backup dir: %w", err)
	}
	backupActive = false
	return nil
}

func copyTree(src string, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("prepare staged vanilla dir: %w", err)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}
		targetPath := filepath.Join(dst, relPath)
		if d.IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("prepare staged dir %q: %w", relPath, err)
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read refreshed path %q: %w", relPath, err)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("prepare staged path %q: %w", relPath, err)
		}
		if err := replaceVanillaWriter(targetPath, data, 0o644); err != nil {
			return fmt.Errorf("write staged path %q: %w", relPath, err)
		}
		return nil
	})
}

func copyOverlay(src string, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat overlay dir: %w", err)
	}
	if err := copyTree(src, dst); err != nil {
		return fmt.Errorf("stage overlay dir: %w", err)
	}
	return nil
}

func renderedCatalogPath(path string) string {
	path = strings.TrimSuffix(filepath.ToSlash(path), ".tmpl")
	switch {
	case strings.HasPrefix(path, "claude/"):
		return ".claude/" + strings.TrimPrefix(path, "claude/")
	case strings.HasPrefix(path, "codex/"):
		return ".codex/" + strings.TrimPrefix(path, "codex/")
	default:
		return path
	}
}

func stripPaths(root string, patterns []string) error {
	for _, pattern := range patterns {
		fullPattern := filepath.Join(root, filepath.Clean(pattern))
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return fmt.Errorf("glob %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			continue
		}
		for _, match := range matches {
			if err := os.RemoveAll(match); err != nil {
				return fmt.Errorf("remove %q: %w", match, err)
			}
		}
	}
	return nil
}

func splitCommandLine(commandLine string) (string, []string, error) {
	var tokens []string
	var current strings.Builder
	var quote rune

	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}

	for _, r := range strings.TrimSpace(commandLine) {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return "", nil, fmt.Errorf("unterminated quote")
	}
	flush()
	if len(tokens) == 0 {
		return "", nil, fmt.Errorf("empty command")
	}

	return tokens[0], tokens[1:], nil
}

func normalizeRepo(repo string) string {
	if parsed, err := url.Parse(repo); err == nil && parsed.Scheme != "" {
		return repo
	}
	return "https://" + strings.TrimPrefix(repo, "https://")
}
