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

// skipGitignoreBase keeps the base .gitignore section out of the copied
// tree: writeGitIgnore merges it into .gitignore, so copying it too would
// leave a stray gitignore.base at the repo root.
func skipGitignoreBase(path string) bool {
	return path == "gitignore.base"
}

func (w Writer) Write(targetDir string, vars project.Variables) error {
	if err := ensureWritableDir(targetDir); err != nil {
		return err
	}

	if err := w.copyTree("templates/common", targetDir, "", vars, skipGitignoreBase); err != nil {
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

	if vars.Agent != "" {
		// Composed last so it wins; RenderAddon owns the layering. See ADR-0027.
		files, err := RenderAddon(w.Assets, vars)
		if err != nil {
			return err
		}
		for _, f := range files {
			targetPath := filepath.Join(targetDir, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(targetPath, f.Data, f.Mode); err != nil {
				return err
			}
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

// addonsRoot holds the opt-in add-on trees, outside templates/golden so
// `forge update` (which replaces a stack's vanilla layer) never touches them.
const addonsRoot = "templates/addons"

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
	return walkTree(w.Assets, root, destPrefix, vars, skip,
		func(dir string) error {
			return os.MkdirAll(filepath.Join(targetDir, filepath.FromSlash(dir)), 0o755)
		},
		func(f File) error {
			targetPath := filepath.Join(targetDir, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			return os.WriteFile(targetPath, f.Data, f.Mode)
		})
}

// walkTree renders one template tree, reporting each directory and file with
// its destination path (slash-separated, relative to the repo root). It is
// the single rendering path for Writer and RenderAddon.
func walkTree(assets fs.FS, root string, destPrefix string, vars project.Variables, skip func(relPath string) bool, onDir func(path string) error, onFile func(File) error) error {
	sub, err := fs.Sub(assets, root)
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
		outPath := filepath.ToSlash(filepath.Join(destPrefix, mapOutputPath(renderedPath)))
		if d.IsDir() {
			return onDir(outPath)
		}

		mode := fs.FileMode(0o644)
		data, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}

		if strings.HasSuffix(path, ".tmpl") {
			rendered, renderErr := renderTemplate(path, data, vars)
			if renderErr != nil {
				return renderErr
			}
			return onFile(File{Path: strings.TrimSuffix(outPath, ".tmpl"), Data: rendered, Mode: mode})
		}

		if filepath.Base(path) == "guard" || filepath.Base(path) == "secret-scan.sh" {
			mode = 0o755
		}

		return onFile(File{Path: outPath, Data: data, Mode: mode})
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
