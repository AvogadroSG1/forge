package upgrade

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"forge/internal/catalog"
	"forge/internal/project"
	"forge/internal/scaffold"
)

// Options configure RunWith.
type Options struct {
	// CheckOnly reports staleness without writing anything.
	CheckOnly bool
	// Agent opts the repo in to the named agent add-on (ADR-0028). Empty
	// means no request.
	Agent string
}

// addonOwnedFiles are the wholly-owned files of each agent add-on (ADR-0028),
// byte-copied like whollyOwnedFiles but only for repos whose manifest records
// the add-on. The list is hash-pinned by gate_test.go and checked against
// scaffold.RenderAddon, so it cannot drift from the template tree. Another
// add-on must declare its files here for upgrade to refresh them.
var addonOwnedFiles = map[string][]managedFile{
	"pi": {
		{src: "templates/addons/pi/common/Dockerfile.agent", dest: "Dockerfile.agent", mode: 0o644},
		{src: "templates/addons/pi/common/mise/conf.d/pi.toml", dest: ".config/mise/conf.d/pi.toml", mode: 0o644},
		{src: "templates/addons/pi/common/internal/piagent/client.go", dest: "internal/piagent/client.go", mode: 0o644},
		{src: "templates/addons/pi/common/internal/piagent/client_test.go", dest: "internal/piagent/client_test.go", mode: 0o644},
	},
}

// addonNotices maps add-on, then stack, to the notice printed at opt-in. It
// names the stack overlay content upgrade never edits (the stance of the
// container notice, ADR-0023).
var addonNotices = map[string]map[string]string{
	"pi": {
		"go-api-chi": "forge upgrade: added the Pi agent add-on (Dockerfile.agent, .config/mise/conf.d/pi.toml, internal/piagent, AGENT.md, internal/httpapi/agent.go); see AGENT.md.\n" +
			"  Not touched by upgrade (stack overlay content), to check by hand:\n" +
			"  - add `mountAgent(router, logger)` to internal/httpapi/router.go; until then the route is not mounted and lint reports `mountAgent` as unused\n" +
			"  - make sure the `image:lint` task in mise.toml runs `hadolint Dockerfile*` so Dockerfile.agent is linted\n" +
			"  - add the CI step `mise run image:agent:scan` to .github/workflows/ci.yml",
		"go-cli-cobra": "forge upgrade: added the Pi agent add-on (Dockerfile.agent, .config/mise/conf.d/pi.toml, internal/piagent, AGENT.md, cmd/agent.go); see AGENT.md.\n" +
			"  Not touched by upgrade (stack overlay content), to check by hand:\n" +
			"  - make sure the `image:lint` task in mise.toml runs `hadolint Dockerfile*` so Dockerfile.agent is linted\n" +
			"  - add the CI step `mise run image:agent:scan` to .github/workflows/ci.yml",
	},
}

// addonOptIn is a validated request to add an add-on the manifest does not
// record yet.
type addonOptIn struct {
	agent      string
	modulePath string
}

// addonNames returns the add-ons upgrade knows how to deliver, sorted.
func addonNames() []string {
	names := make([]string, 0, len(addonOwnedFiles))
	for name := range addonOwnedFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// prepareAddonOptIn validates an --agent request before anything is written.
// It returns nil, nil when the manifest already records the add-on: that run
// is a plain upgrade.
func prepareAddonOptIn(targetDir string, opts Options) (*addonOptIn, error) {
	if opts.CheckOnly {
		return nil, fmt.Errorf("--agent cannot be combined with --check")
	}

	agent := strings.ToLower(strings.TrimSpace(opts.Agent))
	if agent == "none" {
		return nil, fmt.Errorf("--agent none is not supported by upgrade: removing an add-on is out of scope")
	}
	if _, ok := addonOwnedFiles[agent]; !ok {
		return nil, fmt.Errorf("invalid --agent %q (valid choices: %s)", agent, strings.Join(addonNames(), ", "))
	}

	// The stack is never guessed (ADR-0017).
	m, err := ReadManifest(targetDir)
	if err != nil {
		return nil, fmt.Errorf("--agent %s needs .forge/manifest.json to know the stack: %w", agent, err)
	}
	if !slices.Contains(catalog.AgentAddonStacks(agent), m.Stack) {
		return nil, fmt.Errorf("--agent %s only applies to stacks that offer it (valid stacks: %s); this repo's stack is %q",
			agent, strings.Join(catalog.AgentAddonStacks(agent), ", "), m.Stack)
	}
	if m.Agent != "" && m.Agent != agent {
		return nil, fmt.Errorf("--agent %s: this repo already records the %q add-on; upgrade cannot switch or remove add-ons", agent, m.Agent)
	}

	// Already recorded: a plain upgrade, which needs no module path.
	if m.Agent == agent {
		return nil, nil
	}

	data, err := os.ReadFile(filepath.Join(targetDir, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("--agent %s needs go.mod to read the module path: %w", agent, err)
	}
	module, ok := modulePath(data)
	if !ok {
		return nil, fmt.Errorf("--agent %s needs a `module` line in go.mod to read the module path", agent)
	}

	return &addonOptIn{agent: agent, modulePath: module}, nil
}

// modulePath returns the path of the first `module` directive in a go.mod.
// It handles the bare, quoted and raw-quoted forms and a trailing comment.
func modulePath(goMod []byte) (string, bool) {
	for _, line := range strings.Split(string(goMod), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "module")
		if !ok {
			continue
		}
		if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
			continue // some other keyword that starts with "module"
		}
		rest = strings.TrimSpace(rest)
		if rest != "" && (rest[0] == '"' || rest[0] == '`') {
			path, _, found := strings.Cut(rest[1:], rest[:1])
			return path, found && path != ""
		}
		rest, _, _ = strings.Cut(rest, "//")
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return "", false
		}
		return fields[0], true
	}
	return "", false
}

// applyAddon delivers the add-on for a run that reached its add-on step
// (stale, or an opt-in): the wholly-owned files always, and on an opt-in run
// the rest of the add-on, add-if-missing, plus the notice. A manifest that
// records an add-on this forge does not know, or whose stack no longer offers
// it, is skipped silently so a routine upgrade never fails on it.
func applyAddon(assets fs.FS, targetDir string, m Manifest, optIn *addonOptIn, status *Status) error {
	agent := m.Agent
	if optIn != nil {
		agent = optIn.agent
	}
	owned, ok := addonOwnedFiles[agent]
	if !ok || !slices.Contains(catalog.AgentAddonStacks(agent), m.Stack) {
		return nil
	}

	// Read and render everything first so a missing embedded file fails
	// before any write.
	for _, mf := range owned {
		if _, err := fs.ReadFile(assets, mf.src); err != nil {
			return fmt.Errorf("read embedded %s: %w", mf.src, err)
		}
	}
	var extras []scaffold.File
	if optIn != nil {
		rendered, err := scaffold.RenderAddon(assets, project.Variables{
			Agent:      agent,
			Stack:      m.Stack,
			Language:   m.Language,
			ModulePath: optIn.modulePath,
			GoModule:   optIn.modulePath,
		})
		if err != nil {
			return fmt.Errorf("render %s add-on: %w", agent, err)
		}
		for _, f := range rendered {
			if !slices.ContainsFunc(owned, func(mf managedFile) bool { return mf.dest == f.Path }) {
				extras = append(extras, f)
			}
		}
	}

	for _, mf := range owned {
		if err := writeManagedFile(assets, targetDir, mf); err != nil {
			return err
		}
		status.Updated = append(status.Updated, mf.dest)
	}

	if optIn == nil {
		return nil
	}

	// After opt-in these files are the user's code: only write what is
	// missing, and keep anything else at the path (file, symlink, directory).
	for _, f := range extras {
		dest := filepath.Join(targetDir, filepath.FromSlash(f.Path))
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect %s: %w", f.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", f.Path, err)
		}
		if err := writeFileViaRename(dest, f.Data, f.Mode); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
		status.Updated = append(status.Updated, f.Path)
	}

	if notice, ok := addonNotices[agent][m.Stack]; ok {
		fmt.Fprintln(os.Stderr, notice)
	}
	return nil
}
