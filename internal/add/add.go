package addcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"forge/internal/project"
	"forge/internal/scaffold"
)

// Options configures `forge add`.
type Options struct {
	Stack       string
	ProjectName string
	Path        string
	Solution    string
	AuthorName  string
}

// Run executes `forge add` in an existing C# monorepo Git worktree.
func Run(ctx context.Context, assets fs.FS, cwd string, opts Options, out io.Writer) error {
	return runWithRunner(ctx, assets, cwd, opts, out, execCmdRunner{}, nil)
}

// testHooks allows internal tests to inject faults or simulate concurrent edits
// at well-defined transaction boundaries. Always nil in production.
type testHooks struct {
	beforeSolutionWrite func(solutionAbs string) error
	afterMaterialize    func(serviceDirAbs string) error
}

func runWithRunner(
	ctx context.Context,
	assets fs.FS,
	cwd string,
	opts Options,
	out io.Writer,
	r cmdRunner,
	hooks *testHooks,
) error {
	if out == nil {
		out = io.Discard
	}

	// 1. Validate required flags and stack.
	stack := strings.TrimSpace(opts.Stack)
	if stack == "" {
		return fmt.Errorf("missing required flag: --stack")
	}
	switch stack {
	case "csharp-cli", "csharp-webapi", "csharp-blazor":
	default:
		return fmt.Errorf("unsupported --stack %q (supported: csharp-cli, csharp-webapi, csharp-blazor)", stack)
	}

	projectName := strings.TrimSpace(opts.ProjectName)
	if projectName == "" {
		return fmt.Errorf("missing required flag: --project-name")
	}

	rawPath := strings.TrimSpace(opts.Path)
	if rawPath == "" {
		return fmt.Errorf("missing required flag: --path")
	}

	// 2. Discover Git worktree root.
	worktreeRoot, err := resolveGitWorktreeRoot(ctx, r, cwd)
	if err != nil {
		return err
	}

	// 3. Validate destination path (relative, in-worktree, non-reserved, no symlinks, absent).
	serviceRel, serviceAbs, err := validateDestinationPath(worktreeRoot, rawPath)
	if err != nil {
		return err
	}

	// 4. Resolve and validate target solution (.sln/.slnx).
	solutionRel, solutionAbs, err := resolveSolutionPath(worktreeRoot, opts.Solution)
	if err != nil {
		return err
	}

	// 5. Resolve copyright author name (--author-name or host `git config user.name`).
	authorName := strings.TrimSpace(opts.AuthorName)
	if authorName == "" {
		res, gitErr := r.Run(ctx, worktreeRoot, "git", "config", "user.name")
		if gitErr == nil {
			authorName = strings.TrimSpace(res.Stdout)
		}
	}
	if authorName == "" {
		return fmt.Errorf("missing required flag: --author-name")
	}

	// 6. Derive C# service template variables and render fragment in memory.
	vars, err := project.ResolveCSharpServiceVariables(projectName, stack, authorName)
	if err != nil {
		return fmt.Errorf("invalid --project-name: %w", err)
	}

	files, err := scaffold.RenderCSharpService(assets, vars)
	if err != nil {
		return err
	}

	generatedProjRels := collectGeneratedProjectRels(serviceRel, files)
	if len(generatedProjRels) == 0 {
		return fmt.Errorf("internal error: rendered stack %q produced no .csproj files", stack)
	}

	// Append short service-local AGENTS.md with relative host-root link and exact commands.
	rootRelLink, err := filepath.Rel(serviceAbs, worktreeRoot)
	if err != nil {
		return fmt.Errorf("compute relative link to repository root: %w", err)
	}
	files = append(files, scaffold.File{
		Path: "AGENTS.md",
		Data: []byte(renderServiceAgentsMD(vars, serviceRel, solutionRel, filepath.ToSlash(rootRelLink), generatedProjRels)),
		Mode: 0o644,
	})

	// 7. Verify host dotnet SDK compatibility (must target net10.0 via host toolchain).
	if _, err := checkHostDotnetSDK(ctx, r, worktreeRoot); err != nil {
		return err
	}

	// 8. Discover existing in-worktree C# projects and verify solution projects.
	existingProjects, err := discoverCSProjects(worktreeRoot, serviceAbs)
	if err != nil {
		return err
	}
	if len(existingProjects) == 0 {
		return fmt.Errorf("host worktree %s contains no existing C# (.csproj) projects", worktreeRoot)
	}

	solutionProjects, err := listSolutionProjects(ctx, r, worktreeRoot, solutionRel, solutionAbs)
	if err != nil {
		return err
	}

	// 9. Pre-materialization baseline evaluation for EVERY discovered in-worktree .csproj.
	existingEvalsByCanon := make(map[string]*projectEval, len(existingProjects))
	baselines := make(map[string]sourceItemBaseline, len(existingProjects))
	for _, projAbs := range existingProjects {
		pe, err := evalProjectAllConfigs(ctx, r, worktreeRoot, projAbs)
		if err != nil {
			return fmt.Errorf("incompatible host project %s: %w", mustRel(worktreeRoot, projAbs), err)
		}
		canon := canonicalForComparison(projAbs)
		existingEvalsByCanon[canon] = pe
		baselines[projAbs] = captureSourceBaseline(pe)
	}

	solutionEvals := make([]*projectEval, 0, len(solutionProjects))
	for _, projAbs := range solutionProjects {
		canon := canonicalForComparison(projAbs)
		pe, ok := existingEvalsByCanon[canon]
		if !ok {
			var evalErr error
			pe, evalErr = evalProjectAllConfigs(ctx, r, worktreeRoot, projAbs)
			if evalErr != nil {
				return fmt.Errorf("evaluate solution project %s: %w", mustRel(worktreeRoot, projAbs), evalErr)
			}
			existingEvalsByCanon[canon] = pe
		}
		solutionEvals = append(solutionEvals, pe)
	}

	// 10. Begin bounded transaction: snapshot solution, exclusively claim service directory, materialize files.
	tx := newTransaction(worktreeRoot, serviceAbs)
	if err := tx.snapshotSharedFile(solutionAbs); err != nil {
		return err
	}

	if err := tx.claimAndMaterialize(files); err != nil {
		return tx.failWithRollback(err)
	}

	if hooks != nil && hooks.afterMaterialize != nil {
		if err := hooks.afterMaterialize(serviceAbs); err != nil {
			return tx.failWithRollback(err)
		}
	}

	serviceDirCanon, err := canonicalPath(serviceAbs)
	if err != nil {
		return tx.failWithRollback(err)
	}

	// 11. Evaluate generated projects in their final directory context.
	serviceProjAbs := make([]string, 0, len(generatedProjRels))
	for _, rel := range generatedProjRels {
		serviceProjAbs = append(serviceProjAbs, filepath.Join(worktreeRoot, filepath.FromSlash(rel)))
	}

	serviceEvals := make([]*projectEval, 0, len(serviceProjAbs))
	for _, projAbs := range serviceProjAbs {
		pe, err := evalProjectAllConfigs(ctx, r, worktreeRoot, projAbs)
		if err != nil {
			return tx.failWithRollback(err)
		}
		serviceEvals = append(serviceEvals, pe)
	}

	// 12. Enforce host compiler/framework policy (must target net10.0) and collision checks.
	if err := validateServiceTargetFrameworks(serviceEvals); err != nil {
		return tx.failWithRollback(err)
	}
	if err := checkProjectCollisions(serviceEvals, solutionEvals); err != nil {
		return tx.failWithRollback(err)
	}

	// 13. Reconcile CPM / global & common host package references.
	if err := reconcileServicePackages(worktreeRoot, serviceEvals, tx); err != nil {
		return tx.failWithRollback(err)
	}

	// 14. Isolate capturing host projects and verify baseline preservation.
	if err := isolateHostProjects(ctx, r, worktreeRoot, serviceRel, serviceAbs, serviceDirCanon, existingProjects, baselines, tx); err != nil {
		return tx.failWithRollback(err)
	}

	// 15. Register generated projects in the selected solution via temporary sibling copy.
	if hooks != nil && hooks.beforeSolutionWrite != nil {
		if err := hooks.beforeSolutionWrite(solutionAbs); err != nil {
			return tx.failWithRollback(err)
		}
	}
	if err := registerProjectsInSolution(ctx, r, worktreeRoot, solutionAbs, generatedProjRels, tx); err != nil {
		return tx.failWithRollback(err)
	}

	// 16. Prepare NuGet lockfile guard across solution projects before restore.
	lockGuard, err := prepareLockfileGuard(worktreeRoot, serviceDirCanon, solutionEvals, serviceEvals, tx)
	if err != nil {
		return tx.failWithRollback(err)
	}

	// 17. Verify Debug and Release restore, lockfile/source isolation, and build.
	for _, cfg := range buildConfigurations {
		res, err := r.Run(ctx, worktreeRoot, "dotnet", "restore", solutionRel, "--property:Configuration="+cfg)
		if err != nil {
			return tx.failWithRollback(formatCmdError(fmt.Sprintf("dotnet restore %s (%s)", solutionRel, cfg), res, err))
		}
		if err := lockGuard.verifyHostLockfilesUnchanged(tx); err != nil {
			return tx.failWithRollback(err)
		}
		if err := verifyAllHostProjectsIsolated(ctx, r, worktreeRoot, serviceDirCanon, existingProjects, baselines); err != nil {
			return tx.failWithRollback(err)
		}

		buildRes, buildErr := r.Run(ctx, worktreeRoot, "dotnet", "build", solutionRel, "--no-restore", "--configuration", cfg)
		if buildErr != nil {
			return tx.failWithRollback(formatCmdError(fmt.Sprintf("dotnet build %s (%s)", solutionRel, cfg), buildRes, buildErr))
		}
	}

	// 18. Report real integration summary.
	writeAddSummary(out, vars, stack, solutionRel, generatedProjRels, tx.modifiedSharedRels())
	return nil
}

func resolveGitWorktreeRoot(ctx context.Context, r cmdRunner, cwd string) (string, error) {
	res, err := r.Run(ctx, cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", formatCmdError("forge add must be run inside a Git worktree", res, err)
	}
	root := strings.TrimSpace(res.Stdout)
	if root == "" {
		return "", fmt.Errorf("forge add must be run inside a Git worktree")
	}
	canon, err := filepath.EvalSymlinks(root)
	if err == nil {
		root = canon
	}
	return filepath.Clean(root), nil
}

func validateDestinationPath(worktreeRoot, rawPath string) (string, string, error) {
	if strings.ContainsRune(rawPath, '\x00') {
		return "", "", fmt.Errorf("invalid --path %q: contains NUL byte", rawPath)
	}
	if filepath.IsAbs(rawPath) || strings.HasPrefix(rawPath, "/") || strings.HasPrefix(rawPath, "\\") || filepath.VolumeName(rawPath) != "" {
		return "", "", fmt.Errorf("invalid --path %q: must be relative to the Git worktree root", rawPath)
	}

	slash := filepath.ToSlash(rawPath)
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".." {
			return "", "", fmt.Errorf("invalid --path %q: path traversal is not allowed", rawPath)
		}
	}

	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(slash)))
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", "", fmt.Errorf("invalid --path %q: destination cannot be the repository root or escape the worktree", rawPath)
	}

	firstSeg, _, _ := strings.Cut(cleaned, "/")
	switch firstSeg {
	case ".git", ".beads", ".forge":
		return "", "", fmt.Errorf("invalid --path %q: cannot target reserved directory %q", rawPath, firstSeg)
	}

	// Walk every segment from worktreeRoot checking for symlinks and non-dir ancestors.
	parts := strings.Split(cleaned, "/")
	cur := worktreeRoot
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("inspect --path %q: %w", rawPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("invalid --path %q: symlink path component %q is not allowed", rawPath, part)
		}
		if i == len(parts)-1 {
			return "", "", fmt.Errorf("invalid --path %q: destination already exists", rawPath)
		}
		if !info.IsDir() {
			return "", "", fmt.Errorf("invalid --path %q: ancestor %q is not a directory", rawPath, part)
		}
	}

	return cleaned, filepath.Join(worktreeRoot, filepath.FromSlash(cleaned)), nil
}

func resolveSolutionPath(worktreeRoot, rawSolution string) (string, string, error) {
	trimmed := strings.TrimSpace(rawSolution)
	if trimmed == "" {
		entries, err := os.ReadDir(worktreeRoot)
		if err != nil {
			return "", "", fmt.Errorf("read worktree root %s: %w", worktreeRoot, err)
		}
		var matches []string
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".sln" && ext != ".slnx" {
				continue
			}
			full := filepath.Join(worktreeRoot, e.Name())
			info, statErr := os.Lstat(full)
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}
			matches = append(matches, e.Name())
		}
		sort.Strings(matches)
		switch len(matches) {
		case 0:
			return "", "", fmt.Errorf("missing required flag: --solution (no .sln or .slnx found at worktree root %s)", worktreeRoot)
		case 1:
			return matches[0], filepath.Join(worktreeRoot, matches[0]), nil
		default:
			return "", "", fmt.Errorf("ambiguous solution at worktree root (%s); pass --solution explicitly", strings.Join(matches, ", "))
		}
	}

	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") || filepath.VolumeName(trimmed) != "" {
		return "", "", fmt.Errorf("invalid --solution %q: must be relative to the Git worktree root", rawSolution)
	}
	slash := filepath.ToSlash(trimmed)
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".." {
			return "", "", fmt.Errorf("invalid --solution %q: path traversal is not allowed", rawSolution)
		}
	}

	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(slash)))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "../") {
		return "", "", fmt.Errorf("invalid --solution %q: must point to a solution file inside the worktree", rawSolution)
	}

	ext := strings.ToLower(filepath.Ext(cleaned))
	if ext == ".slnf" {
		return "", "", fmt.Errorf("invalid --solution %q: .slnf solution filter files are not supported integration targets; specify a .sln or .slnx file", rawSolution)
	}
	if ext != ".sln" && ext != ".slnx" {
		return "", "", fmt.Errorf("invalid --solution %q: expected a .sln or .slnx file", rawSolution)
	}

	parts := strings.Split(cleaned, "/")
	cur := worktreeRoot
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return "", "", fmt.Errorf("invalid --solution %q: %w", rawSolution, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("invalid --solution %q: symlink component %q is not allowed", rawSolution, part)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("invalid --solution %q: must be an existing regular file", rawSolution)
		}
	}

	return cleaned, cur, nil
}

func collectGeneratedProjectRels(serviceRel string, files []scaffold.File) []string {
	var app, client, tests []string
	for _, f := range files {
		if !strings.EqualFold(filepath.Ext(f.Path), ".csproj") {
			continue
		}
		fullRel := filepath.ToSlash(filepath.Join(serviceRel, filepath.FromSlash(f.Path)))
		switch {
		case strings.HasPrefix(f.Path, "tests/"):
			tests = append(tests, fullRel)
		case strings.Contains(f.Path, ".Client/"):
			client = append(client, fullRel)
		default:
			app = append(app, fullRel)
		}
	}
	sort.Strings(app)
	sort.Strings(tests)
	sort.Strings(client)
	out := make([]string, 0, len(app)+len(tests)+len(client))
	out = append(out, app...)
	out = append(out, tests...)
	out = append(out, client...)
	return out
}

func renderServiceAgentsMD(vars project.Variables, serviceRel, solutionRel, rootRelLink string, projRels []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", vars.ProjectName)
	fmt.Fprintf(&b, "Shared repository instructions and issue tracking live at the [repository root](%s).\n\n", rootRelLink)
	b.WriteString("## Service\n\n")
	fmt.Fprintf(&b, "- Stack: `%s`\n", vars.Stack)
	fmt.Fprintf(&b, "- Namespace: `%s`\n", vars.CSharpNamespace)
	fmt.Fprintf(&b, "- Path: `%s`\n", serviceRel)
	fmt.Fprintf(&b, "- Solution: `%s`\n", solutionRel)
	b.WriteString("- Projects:\n")
	for _, p := range projRels {
		fmt.Fprintf(&b, "  - `%s`\n", p)
	}
	b.WriteString("\n## Commands (from repository root)\n\n")
	b.WriteString("```sh\n")
	fmt.Fprintf(&b, "dotnet build %s\n", solutionRel)
	fmt.Fprintf(&b, "dotnet test %s\n", solutionRel)
	fmt.Fprintf(&b, "dotnet run --project %s\n", projRels[0])
	b.WriteString("```\n")
	return b.String()
}

func writeAddSummary(out io.Writer, vars project.Variables, stack, solutionRel string, projRels, modifiedShared []string) {
	fmt.Fprintf(out, "Added C# service %s (%s)\n", vars.ProjectName, stack)
	fmt.Fprintf(out, "Solution: %s\n", solutionRel)
	fmt.Fprintln(out, "Generated projects:")
	for _, p := range projRels {
		fmt.Fprintf(out, "  %s\n", p)
	}
	if len(modifiedShared) > 0 {
		fmt.Fprintln(out, "Updated shared files:")
		for _, p := range modifiedShared {
			fmt.Fprintf(out, "  %s\n", p)
		}
	}
	fmt.Fprintln(out, "Next steps (from repository root):")
	fmt.Fprintf(out, "  dotnet run --project %s\n", projRels[0])
	fmt.Fprintf(out, "  dotnet test %s\n", solutionRel)
}

// transaction tracks all created directories, materialized service files,
// edited shared files, and protective host lockfile snapshots for bounded rollback.
type transaction struct {
	worktreeRoot        string
	serviceDirAbs       string
	createdDirs         []string
	writtenServiceFiles map[string][]byte
	sharedSnapshots     map[string]*fileSnapshot
	protectiveSnapshots map[string]*fileSnapshot
	unexpectedHostFiles []string
}

type fileSnapshot struct {
	absPath          string
	originalBytes    []byte
	mode             os.FileMode
	lastWrittenBytes []byte
	modified         bool
}

func newTransaction(worktreeRoot, serviceDirAbs string) *transaction {
	return &transaction{
		worktreeRoot:        worktreeRoot,
		serviceDirAbs:       serviceDirAbs,
		writtenServiceFiles: map[string][]byte{},
		sharedSnapshots:     map[string]*fileSnapshot{},
		protectiveSnapshots: map[string]*fileSnapshot{},
	}
}

func (tx *transaction) snapshotSharedFile(absPath string) error {
	canon, err := canonicalPath(absPath)
	if err != nil {
		return err
	}
	if _, exists := tx.sharedSnapshots[canon]; exists {
		return nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("stat shared file %s: %w", absPath, err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read shared file %s: %w", absPath, err)
	}
	tx.sharedSnapshots[canon] = &fileSnapshot{
		absPath:       absPath,
		originalBytes: data,
		mode:          info.Mode().Perm(),
	}
	return nil
}

func (tx *transaction) snapshotProtectiveFile(absPath string) error {
	canon := canonicalForComparison(absPath)
	if _, exists := tx.protectiveSnapshots[canon]; exists {
		return nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("stat protective file %s: %w", absPath, err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read protective file %s: %w", absPath, err)
	}
	tx.protectiveSnapshots[canon] = &fileSnapshot{
		absPath:       absPath,
		originalBytes: data,
		mode:          info.Mode().Perm(),
	}
	return nil
}

func (tx *transaction) writeSharedFile(absPath string, newBytes []byte) error {
	canon, err := canonicalPath(absPath)
	if err != nil {
		return err
	}
	snap, ok := tx.sharedSnapshots[canon]
	if !ok {
		return fmt.Errorf("internal error: shared file %s was not snapshotted before write", absPath)
	}

	expected := snap.originalBytes
	if snap.modified {
		expected = snap.lastWrittenBytes
	}
	current, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("verify snapshot for %s before write: %w", mustRel(tx.worktreeRoot, absPath), err)
	}
	if !bytes.Equal(current, expected) {
		return fmt.Errorf("shared file %s changed on disk during forge add; refusing to overwrite concurrent edits", mustRel(tx.worktreeRoot, absPath))
	}

	if err := atomicWriteFile(absPath, newBytes, snap.mode); err != nil {
		return err
	}
	snap.lastWrittenBytes = append([]byte(nil), newBytes...)
	snap.modified = true
	return nil
}

func (tx *transaction) claimAndMaterialize(files []scaffold.File) error {
	rel, err := filepath.Rel(tx.worktreeRoot, tx.serviceDirAbs)
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	cur := tx.worktreeRoot
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		if i == len(parts)-1 {
			// Exclusive claim of the absent destination directory.
			if err := os.Mkdir(cur, 0o755); err != nil {
				if errors.Is(err, os.ErrExist) {
					return fmt.Errorf("destination %s already exists", filepath.ToSlash(rel))
				}
				return fmt.Errorf("create service directory %s: %w", filepath.ToSlash(rel), err)
			}
			tx.createdDirs = append(tx.createdDirs, cur)
			continue
		}
		if _, err := os.Lstat(cur); errors.Is(err, os.ErrNotExist) {
			if mkErr := os.Mkdir(cur, 0o755); mkErr != nil {
				return fmt.Errorf("create parent directory %s: %w", cur, mkErr)
			}
			tx.createdDirs = append(tx.createdDirs, cur)
		}
	}

	for _, f := range files {
		absPath := filepath.Join(tx.serviceDirAbs, filepath.FromSlash(f.Path))
		if err := tx.ensureServiceSubdirs(filepath.Dir(absPath)); err != nil {
			return err
		}
		if err := tx.writeServiceFile(absPath, f.Data, f.Mode); err != nil {
			return err
		}
	}
	return nil
}

func (tx *transaction) ensureServiceSubdirs(dir string) error {
	if dir == tx.serviceDirAbs {
		return nil
	}
	rel, err := filepath.Rel(tx.serviceDirAbs, dir)
	if err != nil {
		return err
	}
	cur := tx.serviceDirAbs
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		if _, err := os.Lstat(cur); errors.Is(err, os.ErrNotExist) {
			if mkErr := os.Mkdir(cur, 0o755); mkErr != nil {
				return fmt.Errorf("create service subdirectory %s: %w", cur, mkErr)
			}
			tx.createdDirs = append(tx.createdDirs, cur)
		}
	}
	return nil
}

func (tx *transaction) writeServiceFile(absPath string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := atomicWriteFile(absPath, data, mode); err != nil {
		return err
	}
	tx.writtenServiceFiles[absPath] = append([]byte(nil), data...)
	return nil
}

func (tx *transaction) modifiedSharedRels() []string {
	var rels []string
	for _, snap := range tx.sharedSnapshots {
		if !snap.modified {
			continue
		}
		rels = append(rels, mustRel(tx.worktreeRoot, snap.absPath))
	}
	sort.Strings(rels)
	return rels
}

func (tx *transaction) failWithRollback(origErr error) error {
	retained := tx.rollback()
	if len(retained) == 0 {
		return origErr
	}
	return fmt.Errorf("%w\nrollback retained paths (preserved to avoid deleting modified or generated artifacts):\n  %s",
		origErr, strings.Join(retained, "\n  "))
}

func (tx *transaction) rollback() []string {
	retainedSet := map[string]struct{}{}
	addRetained := func(absPath string) {
		retainedSet[mustRel(tx.worktreeRoot, absPath)] = struct{}{}
	}

	// 1. Restore modified shared files only if current bytes match our last write.
	for _, snap := range tx.sharedSnapshots {
		if !snap.modified {
			continue
		}
		current, err := os.ReadFile(snap.absPath)
		if err != nil || !bytes.Equal(current, snap.lastWrittenBytes) {
			addRetained(snap.absPath)
			continue
		}
		if err := atomicWriteFile(snap.absPath, snap.originalBytes, snap.mode); err != nil {
			addRetained(snap.absPath)
		}
	}

	// 2. Restore protective host lockfiles if restore drifted their bytes.
	for _, snap := range tx.protectiveSnapshots {
		current, err := os.ReadFile(snap.absPath)
		if err == nil && bytes.Equal(current, snap.originalBytes) {
			continue
		}
		if err := atomicWriteFile(snap.absPath, snap.originalBytes, snap.mode); err != nil {
			addRetained(snap.absPath)
		}
	}

	// 3. Remove unexpected host lockfiles created during restore.
	for _, absPath := range tx.unexpectedHostFiles {
		if err := os.Remove(absPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			addRetained(absPath)
		}
	}

	// 4. Remove generated service files only when their bytes still match our output.
	serviceFiles := make([]string, 0, len(tx.writtenServiceFiles))
	for p := range tx.writtenServiceFiles {
		serviceFiles = append(serviceFiles, p)
	}
	sort.Strings(serviceFiles)
	for _, absPath := range serviceFiles {
		expected := tx.writtenServiceFiles[absPath]
		current, err := os.ReadFile(absPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !bytes.Equal(current, expected) {
			addRetained(absPath)
			continue
		}
		if err := os.Remove(absPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			addRetained(absPath)
		}
	}

	// 5. Prune only empty newly created directories in reverse creation order.
	for i := len(tx.createdDirs) - 1; i >= 0; i-- {
		dir := tx.createdDirs[i]
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			addRetained(dir)
		}
	}

	retained := make([]string, 0, len(retainedSet))
	for p := range retainedSet {
		retained = append(retained, p)
	}
	sort.Strings(retained)
	return retained
}

func atomicWriteFile(targetPath string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".forge-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", targetPath, err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file for %s: %w", targetPath, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file for %s: %w", targetPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", targetPath, err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("replace %s: %w", targetPath, err)
	}
	success = true
	return nil
}

func canonicalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	// If path itself doesn't exist yet, resolve existing ancestor prefix.
	return canonicalForComparison(abs), nil
}

func canonicalForComparison(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	// Walk upward until we find an existing ancestor and resolve symlinks on it.
	var suffix []string
	cur := abs
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(abs)
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Clean(filepath.Join(parts...))
		}
	}
}

func isWithinRoot(targetPath, rootPath string) (bool, error) {
	rootCanon := canonicalForComparison(rootPath)
	targetCanon := canonicalForComparison(targetPath)
	return pathHasPrefixDir(targetCanon, rootCanon), nil
}

func pathHasPrefixDir(targetCanon, rootCanon string) bool {
	if targetCanon == rootCanon {
		return true
	}
	prefix := rootCanon
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(targetCanon, prefix)
}

func relToWorktree(worktreeRoot, targetPath string) (string, error) {
	rootCanon := canonicalForComparison(worktreeRoot)
	targetCanon := canonicalForComparison(targetPath)
	rel, err := filepath.Rel(rootCanon, targetCanon)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func mustRel(worktreeRoot, targetPath string) string {
	rel, err := relToWorktree(worktreeRoot, targetPath)
	if err != nil {
		return targetPath
	}
	return rel
}
