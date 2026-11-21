package upgrade

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
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

// addonRequirement is what forge's wiring needs from the repo's own code.
// It is checked only when at least one of the guarded files would be written:
// a repo that keeps its own wiring is not held to forge's.
type addonRequirement struct {
	guards []string // rendered paths that need it
	dir    string   // package directory that must declare symbol
	symbol string   // top-level name the wiring uses
	// paramPkg and paramType, when set, make symbol a function whose first
	// parameter is *paramPkg.paramType (paramPkg is an import path).
	paramPkg, paramType string
	guidance            string
}

// addonRequirements maps add-on, then stack, to what forge's wiring needs.
var addonRequirements = map[string]map[string][]addonRequirement{
	"pi": {
		"go-api-chi": {{
			guards:    []string{"internal/httpapi/agent.go", "internal/httpapi/agent_test.go"},
			dir:       "internal/httpapi",
			symbol:    "NewRouter",
			paramPkg:  "log/slog",
			paramType: "Logger",
			guidance: "this repo's router predates the move to log/slog; move `NewRouter` and its handlers to `*slog.Logger` " +
				"(the PORT / slog change named in the container notice and CONTAINER.md), then re-run `forge upgrade --agent pi`",
		}},
		"go-cli-cobra": {{
			guards:   []string{"cmd/agent.go", "cmd/agent_test.go"},
			dir:      "cmd",
			symbol:   "rootCmd",
			guidance: "name the root command `rootCmd`, or write your own cmd/agent.go",
		}},
	},
}

// addonStep is a manual step in the opt-in notice for stack overlay content
// upgrade never edits (the stance of the container notice, ADR-0023).
type addonStep struct {
	// needs, when set, is a rendered path that must be forge's (written now or
	// already forge's bytes) for the step to be forge's to prescribe.
	needs string
	text  string
}

// addonSteps maps add-on, then stack, to the manual steps of the opt-in notice.
var addonSteps = map[string]map[string][]addonStep{
	"pi": {
		"go-api-chi": {
			{needs: "internal/httpapi/agent.go", text: "add `mountAgent(router, logger)` to internal/httpapi/router.go; until then the route is not mounted and lint reports `mountAgent` as unused"},
			{text: "make sure the `image:lint` task in mise.toml runs `hadolint Dockerfile*` so Dockerfile.agent is linted"},
			{text: "add the CI step `mise run image:agent:scan` to .github/workflows/ci.yml"},
		},
		"go-cli-cobra": {
			{text: "make sure the `image:lint` task in mise.toml runs `hadolint Dockerfile*` so Dockerfile.agent is linted"},
			{text: "add the CI step `mise run image:agent:scan` to .github/workflows/ci.yml"},
		},
	},
}

// addonPlan is what an opt-in run will do, worked out before the first write.
type addonPlan struct {
	write     []scaffold.File // non-owned files to install
	kept      []keptGroup
	forgeHas  map[string]bool // non-owned paths that are forge's after the run
	ownedDest []string        // owned files, for the notice
}

// keptGroup is a wiring group left alone because a member is the user's.
type keptGroup struct {
	custom       []string // the user's files
	notInstalled []string // forge's files that were missing and stay so
}

// addonOptIn is a validated request to add an add-on the manifest does not
// record yet.
type addonOptIn struct {
	agent      string
	modulePath string
	plan       addonPlan
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

// prepareAddonOptIn validates an --agent request and plans the add-on step
// before anything is written, so a refusal leaves the repo untouched. It
// returns nil, nil when the manifest already records the add-on: that run is a
// plain upgrade.
func prepareAddonOptIn(assets fs.FS, targetDir string, opts Options) (*addonOptIn, error) {
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

	plan, err := planAddon(assets, targetDir, m, agent, module)
	if err != nil {
		return nil, err
	}
	return &addonOptIn{agent: agent, modulePath: module, plan: plan}, nil
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

// planAddon is the read-only half of an opt-in: it renders the add-on, decides
// per wiring group what to install, and checks that the repo can take forge's
// wiring. Nothing is written.
//
// Files forge does not own are decided in groups, like the Dockerfile pair in
// addContainerFilesIfMissing: a Go source file and its _test.go sibling stand
// or fall together, because the test exercises forge's implementation. If any
// member of a group is the user's, none of it is installed; otherwise the
// missing members are. A member that is already forge's bytes counts as
// delivered, so an interrupted opt-in completes on re-run.
func planAddon(assets fs.FS, targetDir string, m Manifest, agent, module string) (addonPlan, error) {
	var plan addonPlan
	owned := addonOwnedFiles[agent]
	for _, mf := range owned {
		if _, err := fs.ReadFile(assets, mf.src); err != nil {
			return plan, fmt.Errorf("read embedded %s: %w", mf.src, err)
		}
		plan.ownedDest = append(plan.ownedDest, mf.dest)
	}
	rendered, err := scaffold.RenderAddon(assets, project.Variables{
		Agent:      agent,
		Stack:      m.Stack,
		Language:   m.Language,
		ModulePath: module,
		GoModule:   module,
	})
	if err != nil {
		return plan, fmt.Errorf("render %s add-on: %w", agent, err)
	}

	var extras []scaffold.File
	for _, f := range rendered {
		if !slices.ContainsFunc(owned, func(mf managedFile) bool { return mf.dest == f.Path }) {
			extras = append(extras, f)
		}
	}
	var groups [][]scaffold.File
	index := map[string]int{}
	for _, f := range extras {
		key := groupKey(f.Path, extras)
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], f)
	}

	plan.forgeHas = map[string]bool{}
	for _, group := range groups {
		states := make([]fileKind, len(group))
		anyCustom := false
		for i, f := range group {
			state, err := classifyFile(filepath.Join(targetDir, filepath.FromSlash(f.Path)), f.Data)
			if err != nil {
				return plan, fmt.Errorf("inspect %s: %w", f.Path, err)
			}
			states[i] = state
			anyCustom = anyCustom || state == fileCustom
		}
		var kept keptGroup
		for i, f := range group {
			switch {
			case states[i] == fileCustom:
				kept.custom = append(kept.custom, f.Path)
			case anyCustom && states[i] == fileMissing:
				kept.notInstalled = append(kept.notInstalled, f.Path)
			case states[i] == fileMissing:
				plan.write = append(plan.write, f)
				plan.forgeHas[f.Path] = true
			default: // already forge's bytes
				plan.forgeHas[f.Path] = true
			}
		}
		if anyCustom {
			plan.kept = append(plan.kept, kept)
		}
	}

	for _, req := range addonRequirements[agent][m.Stack] {
		if !slices.ContainsFunc(plan.write, func(f scaffold.File) bool { return slices.Contains(req.guards, f.Path) }) {
			continue
		}
		if err := checkRequirement(targetDir, agent, req); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// groupKey names the group of a rendered file: a x_test.go with a rendered
// x.go sibling takes the source's path, anything else stands alone.
func groupKey(p string, all []scaffold.File) string {
	if src, ok := strings.CutSuffix(p, "_test.go"); ok {
		src += ".go"
		if slices.ContainsFunc(all, func(f scaffold.File) bool { return f.Path == src }) {
			return src
		}
	}
	return p
}

// checkRequirement parses the non-test Go files of req.dir and reports why
// forge's wiring would not compile there, with the guidance for the user.
func checkRequirement(targetDir, agent string, req addonRequirement) error {
	reason := unmetRequirement(targetDir, req)
	if reason == "" {
		return nil
	}
	return fmt.Errorf("--agent %s: forge's %s need %s to declare %s, but %s; %s",
		agent, strings.Join(req.guards, " and "), req.dir, describeSymbol(req), reason, req.guidance)
}

func describeSymbol(req addonRequirement) string {
	if req.paramPkg == "" {
		return "`" + req.symbol + "`"
	}
	return "`func " + req.symbol + "` whose first parameter is `*" + path.Base(req.paramPkg) + "." + req.paramType + "`"
}

// unmetRequirement returns why req fails, or "" when it holds.
func unmetRequirement(targetDir string, req addonRequirement) string {
	dir := filepath.Join(targetDir, filepath.FromSlash(req.dir))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return req.dir + " does not exist"
	} else if err != nil {
		return "it cannot read " + req.dir
	}
	fset := token.NewFileSet()
	var parseErrs []string
	found, typed := false, false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Parsed under its repo-relative name so errors do not leak the checkout path.
		file, err := parseGoFile(fset, filepath.Join(dir, name), req.dir+"/"+name)
		if err != nil {
			parseErrs = append(parseErrs, "cannot parse "+err.Error())
			continue
		}
		if f, t := declares(file, req); f {
			found = true
			typed = typed || t
		}
	}
	switch {
	case len(parseErrs) > 0:
		return strings.Join(parseErrs, "; ")
	case !found:
		return "no top-level " + req.symbol + " is declared there"
	case !typed:
		return "the first parameter of " + req.symbol + " has another type"
	}
	return ""
}

func parseGoFile(fset *token.FileSet, abs, name string) (*ast.File, error) {
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return parser.ParseFile(fset, name, src, 0)
}

// declares reports whether file declares req.symbol at top level, and whether
// that declaration has the required first parameter (true when none is required).
func declares(file *ast.File, req addonRequirement) (found, typed bool) {
	// The file's name for the parameter's package, from its own import.
	pkgName := ""
	for _, imp := range file.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == req.paramPkg {
			pkgName = path.Base(p)
			if imp.Name != nil {
				pkgName = imp.Name.Name
			}
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if req.paramPkg != "" {
				continue
			}
			for _, spec := range d.Specs {
				if v, ok := spec.(*ast.ValueSpec); ok && d.Tok == token.VAR && slices.ContainsFunc(v.Names, func(n *ast.Ident) bool { return n.Name == req.symbol }) {
					return true, true
				}
			}
		case *ast.FuncDecl:
			if req.paramPkg == "" || d.Recv != nil || d.Name.Name != req.symbol {
				continue
			}
			found = true
			if params := d.Type.Params.List; len(params) > 0 && pkgName != "" {
				if star, ok := params[0].Type.(*ast.StarExpr); ok {
					if sel, ok := star.X.(*ast.SelectorExpr); ok && sel.Sel.Name == req.paramType {
						if x, ok := sel.X.(*ast.Ident); ok && x.Name == pkgName {
							return true, true
						}
					}
				}
			}
		}
	}
	return found, false
}

// applyAddon delivers the add-on for a run that reached its add-on step
// (stale, or an opt-in): the wholly-owned files always, and on an opt-in run
// the planned files plus the notice. A manifest that records an add-on this
// forge does not know, or whose stack no longer offers it, is skipped silently
// so a routine upgrade never fails on it.
func applyAddon(assets fs.FS, targetDir string, m Manifest, optIn *addonOptIn, status *Status) error {
	agent := m.Agent
	if optIn != nil {
		agent = optIn.agent
	}
	owned, ok := addonOwnedFiles[agent]
	if !ok || !slices.Contains(catalog.AgentAddonStacks(agent), m.Stack) {
		return nil
	}

	// An opt-in read its embedded files while planning; a plain refresh reads
	// them all first so a missing one fails before any write.
	if optIn == nil {
		for _, mf := range owned {
			if _, err := fs.ReadFile(assets, mf.src); err != nil {
				return fmt.Errorf("read embedded %s: %w", mf.src, err)
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

	// After opt-in these files are the user's code: write only what the plan
	// found missing (via a temporary name, so a partial write is never taken
	// for a hand-written file).
	for _, f := range optIn.plan.write {
		dest := filepath.Join(targetDir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", f.Path, err)
		}
		if err := writeFileViaRename(dest, f.Data, f.Mode); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
		status.Updated = append(status.Updated, f.Path)
	}

	fmt.Fprintln(os.Stderr, optInNotice(agent, m.Stack, optIn.plan))
	return nil
}

// optInNotice describes what the opt-in did: what was added, what was kept,
// and the manual steps for stack overlay content upgrade never edits.
func optInNotice(agent, stack string, plan addonPlan) string {
	added := []string{}
	for _, dest := range plan.ownedDest {
		// Summarise internal/piagent/* as the directory.
		if dir := path.Dir(dest); strings.HasPrefix(dest, "internal/") && dir != "internal" {
			dest = dir
		}
		if !slices.Contains(added, dest) {
			added = append(added, dest)
		}
	}
	for _, f := range plan.write {
		added = append(added, f.Path)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "forge upgrade: added the Pi agent add-on (%s)", strings.Join(added, ", "))
	if slices.Contains(added, "AGENT.md") {
		b.WriteString("; see AGENT.md")
	}
	b.WriteString(".\n")

	for _, k := range plan.kept {
		line := "kept your " + quoteAll(k.custom)
		for _, p := range k.notInstalled {
			if strings.HasSuffix(p, "_test.go") {
				line += "; `" + p + "` was not installed because it tests forge's implementation"
			} else {
				line += "; `" + p + "` was not installed because your tests do not match it"
			}
		}
		b.WriteString("  " + line + "\n")
	}

	b.WriteString("  Not touched by upgrade (stack overlay content), to check by hand:")
	for _, step := range addonSteps[agent][stack] {
		if step.needs == "" || plan.forgeHas[step.needs] {
			b.WriteString("\n  - " + step.text)
		}
	}
	return b.String()
}

func quoteAll(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = "`" + p + "`"
	}
	return strings.Join(quoted, ", ")
}
