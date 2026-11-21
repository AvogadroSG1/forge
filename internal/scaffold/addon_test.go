package scaffold

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"forge"
	"forge/internal/project"
)

func addonVars(t *testing.T, stack, agent string) project.Variables {
	t.Helper()
	language, ptype := "go", "api"
	if stack == "go-cli-cobra" {
		ptype = "cli"
	}
	vars, err := project.ResolveVariables(project.Input{
		ProjectName: "Agent App",
		Language:    language,
		ProjectType: ptype,
		Stack:       stack,
		Agent:       agent,
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
		Remote:      project.RemoteNone,
		ModulePath:  "example.com/agent-app",
	})
	if err != nil {
		t.Fatalf("ResolveVariables() error = %v", err)
	}
	return vars
}

func TestRenderAddonComposesCommonThenStack(t *testing.T) {
	t.Parallel()

	assets := fstest.MapFS{
		"templates/addons/pi/common/Dockerfile.agent":    {Data: []byte("FROM scratch\n")},
		"templates/addons/pi/common/NOTE.md":             {Data: []byte("common\n")},
		"templates/addons/pi/common/mise/conf.d/x.toml":  {Data: []byte("[tasks]\n")},
		"templates/addons/pi/common/bin/guard":           {Data: []byte("#!/bin/sh\n")},
		"templates/addons/pi/go-api-chi/NOTE.md":         {Data: []byte("stack\n")},
		"templates/addons/pi/go-api-chi/agent.go.tmpl":   {Data: []byte("// {{.ModulePath}} {{.Stack}}\n")},
		"templates/addons/pi/go-cli-cobra/cmd/other.txt": {Data: []byte("other\n")},
		"templates/addons/other/common/NO.md":            {Data: []byte("no\n")},
	}

	files, err := RenderAddon(assets, addonVars(t, "go-api-chi", "pi"))
	if err != nil {
		t.Fatalf("RenderAddon() error = %v", err)
	}

	got := map[string]File{}
	for _, f := range files {
		if _, dup := got[f.Path]; dup {
			t.Fatalf("duplicate path %s", f.Path)
		}
		got[f.Path] = f
	}
	want := map[string]string{
		"Dockerfile.agent":           "FROM scratch\n",
		"NOTE.md":                    "stack\n",
		".config/mise/conf.d/x.toml": "[tasks]\n",
		"bin/guard":                  "#!/bin/sh\n",
		"agent.go":                   "// example.com/agent-app go-api-chi\n",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d files %v, want %d", len(got), files, len(want))
	}
	for p, data := range want {
		if string(got[p].Data) != data {
			t.Errorf("%s = %q, want %q", p, got[p].Data, data)
		}
	}
	if got["bin/guard"].Mode != 0o755 || got["NOTE.md"].Mode != 0o644 {
		t.Errorf("modes: guard=%v note=%v", got["bin/guard"].Mode, got["NOTE.md"].Mode)
	}
	if files[len(files)-1].Path != "agent.go" && files[len(files)-1].Path != "NOTE.md" {
		t.Errorf("stack files must come last, got order %v", files)
	}
}

func TestRenderAddonEdgeCases(t *testing.T) {
	t.Parallel()

	assets := fstest.MapFS{
		"templates/addons/pi/common/a.txt": {Data: []byte("a\n")},
	}

	files, err := RenderAddon(assets, addonVars(t, "go-api-chi", ""))
	if err != nil || files != nil {
		t.Fatalf("empty agent = %v, %v; want nil, nil", files, err)
	}

	files, err = RenderAddon(assets, addonVars(t, "go-api-chi", "pi"))
	if err != nil || len(files) != 1 || files[0].Path != "a.txt" {
		t.Fatalf("no stack tree = %v, %v; want only a.txt", files, err)
	}

	if _, err := RenderAddon(fstest.MapFS{}, addonVars(t, "go-api-chi", "pi")); err == nil {
		t.Fatal("missing common tree must be an error")
	}
}

func TestRenderAddonMatchesWriterOutput(t *testing.T) {
	t.Parallel()

	for _, stack := range []string{"go-api-chi", "go-cli-cobra"} {
		t.Run(stack, func(t *testing.T) {
			t.Parallel()

			vars := addonVars(t, stack, "pi")
			dir := t.TempDir()
			if err := (Writer{Assets: forge.Assets()}).Write(dir, vars); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			files, err := RenderAddon(forge.Assets(), vars)
			if err != nil {
				t.Fatalf("RenderAddon() error = %v", err)
			}
			if len(files) == 0 {
				t.Fatal("expected add-on files")
			}
			for _, f := range files {
				data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
				if err != nil {
					t.Fatalf("%s not written: %v", f.Path, err)
				}
				if string(data) != string(f.Data) {
					t.Errorf("%s differs from Writer output", f.Path)
				}
			}
		})
	}
}
