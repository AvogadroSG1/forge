package scaffold

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"forge/internal/project"
)

type Writer struct {
	Assets fs.FS
}

func (w Writer) Write(targetDir string, vars project.Variables) error {
	if err := ensureWritableDir(targetDir); err != nil {
		return err
	}

	if err := w.copyTree("templates/common", targetDir, "", vars, nil); err != nil {
		return err
	}

	stackRoot := filepath.ToSlash(filepath.Join("templates/golden", vars.Stack))
	if err := w.copyTree(stackRoot, targetDir, "", vars, nil); err != nil {
		return err
	}

	overlayRoot := filepath.ToSlash(filepath.Join(stackRoot, ".forge-overlay"))
	if err := w.copyTree(overlayRoot, targetDir, "", vars, nil); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	if vars.Frontend != "" {
		frontendRoot := filepath.ToSlash(filepath.Join("templates/golden", vars.Frontend))
		if err := w.copyTree(frontendRoot, targetDir, frontendFragmentDir, vars, nil); err != nil {
			return err
		}

		frontendOverlayRoot := filepath.ToSlash(filepath.Join(frontendRoot, ".forge-overlay"))
		if err := w.copyTree(frontendOverlayRoot, targetDir, frontendFragmentDir, vars, skipRootGateFiles); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	stem, err := gitignoreStem(vars.Language)
	if err != nil {
		return err
	}
	stems := []string{stem}
	if vars.Frontend != "" && stem != "Node" {
		stems = append(stems, "Node")
	}
	if err := w.writeGitIgnore(targetDir, stems); err != nil {
		return err
	}

	claudePath := filepath.Join(targetDir, "CLAUDE.md")
	if err := os.RemoveAll(claudePath); err != nil {
		return err
	}
	if err := os.Symlink("AGENTS.md", claudePath); err != nil {
		return err
	}

	return nil
}

func ensureWritableDir(targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".DS_Store" || entry.Name() == ".git" {
			continue
		}
		return fmt.Errorf("directory not empty: %s", targetDir)
	}

	return nil
}

// frontendFragmentDir is where a fullstack repo hosts its frontend tree.
const frontendFragmentDir = "web"

// skipRootGateFiles drops a frontend fragment's gate files: in fullstack
// mode the backend overlay's templated mise.toml/lefthook.yml/ci.yml own
// the whole repo, so the fragment must not ship competing copies.
func skipRootGateFiles(path string) bool {
	switch {
	case path == "mise.toml" || path == "mise.toml.tmpl":
		return true
	case path == "lefthook.yml":
		return true
	case path == ".github" || strings.HasPrefix(path, ".github/"):
		return true
	default:
		return false
	}
}

func (w Writer) copyTree(root string, targetDir string, destPrefix string, vars project.Variables, skip func(relPath string) bool) error {
	sub, err := fs.Sub(w.Assets, root)
	if err != nil {
		return err
	}

	return fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if path == ".forge-overlay" {
			return fs.SkipDir
		}
		if strings.HasPrefix(path, ".forge-overlay/") {
			return fs.SkipDir
		}
		if skip != nil && skip(path) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		renderedPath, err := renderPath(path, vars)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(targetDir, destPrefix, mapOutputPath(renderedPath))
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		mode := fs.FileMode(0o644)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}

		data, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}

		if strings.HasSuffix(path, ".tmpl") {
			rendered, renderErr := renderTemplate(path, data, vars)
			if renderErr != nil {
				return renderErr
			}
			targetPath = strings.TrimSuffix(targetPath, ".tmpl")
			return os.WriteFile(targetPath, rendered, mode)
		}

		if filepath.Base(path) == "guard" || filepath.Base(path) == "secret-scan.sh" {
			mode = 0o755
		}

		return os.WriteFile(targetPath, data, mode)
	})
}

func mapOutputPath(path string) string {
	switch {
	case path == "claude":
		return ".claude"
	case strings.HasPrefix(path, "claude/"):
		return filepath.Join(".claude", strings.TrimPrefix(path, "claude/"))
	case path == "codex":
		return ".codex"
	case strings.HasPrefix(path, "codex/"):
		return filepath.Join(".codex", strings.TrimPrefix(path, "codex/"))
	case path == "opencode":
		return ".opencode"
	case strings.HasPrefix(path, "opencode/"):
		return filepath.Join(".opencode", strings.TrimPrefix(path, "opencode/"))
	case path == "mise":
		return ".config/mise"
	case strings.HasPrefix(path, "mise/"):
		return filepath.Join(".config/mise", strings.TrimPrefix(path, "mise/"))
	case path == "otel":
		return ".otel"
	case strings.HasPrefix(path, "otel/"):
		return filepath.Join(".otel", strings.TrimPrefix(path, "otel/"))
	default:
		return path
	}
}

func renderPath(path string, vars project.Variables) (string, error) {
	if !strings.Contains(path, "{{") {
		return path, nil
	}
	tmpl, err := template.New("path:" + path).Option("missingkey=error").Parse(path)
	if err != nil {
		return "", fmt.Errorf("template path %q: parse: %w", path, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("template path %q: render: %w", path, err)
	}
	rendered := buf.String()
	for _, seg := range strings.Split(rendered, "/") {
		if seg == ".." {
			return "", fmt.Errorf("template path %q: rendered to unsafe value %q", path, rendered)
		}
	}
	return rendered, nil
}

func renderTemplate(name string, data []byte, vars project.Variables) ([]byte, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(data))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (w Writer) writeGitIgnore(targetDir string, stems []string) error {
	base, err := fs.ReadFile(w.Assets, "templates/common/gitignore.base")
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	buf.Write(base)
	if len(base) > 0 && base[len(base)-1] != '\n' {
		buf.WriteByte('\n')
	}
	for _, stem := range stems {
		lang, err := fs.ReadFile(w.Assets, filepath.ToSlash(filepath.Join("templates/gitignore", stem+".gitignore")))
		if err != nil {
			return err
		}
		fmt.Fprintf(&buf, "# ===== %s gitignore =====\n", strings.ToLower(stem))
		buf.Write(lang)
		if len(lang) > 0 && lang[len(lang)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}

	return os.WriteFile(filepath.Join(targetDir, ".gitignore"), buf.Bytes(), 0o644)
}

func gitignoreStem(language string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "go":
		return "Go", nil
	case "python":
		return "Python", nil
	case "csharp":
		return "VisualStudio", nil
	case "typescript":
		return "Node", nil
	default:
		return "", fmt.Errorf("unsupported language %q", language)
	}
}
