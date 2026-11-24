package addcmd

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoverCSProjects finds all existing .csproj files within worktreeRoot,
// pruning .git, .beads, bin, obj, node_modules, the destination directory, and
// symlink directories.
func discoverCSProjects(worktreeRoot, excludeAbs string) ([]string, error) {
	excludeCanon := ""
	if excludeAbs != "" {
		excludeCanon = canonicalForComparison(excludeAbs)
	}

	var projects []string
	err := filepath.WalkDir(worktreeRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == worktreeRoot {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".beads", "bin", "obj", "node_modules":
				return fs.SkipDir
			}
			if excludeCanon != "" && canonicalForComparison(p) == excludeCanon {
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			projects = append(projects, filepath.Clean(p))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover C# projects under %s: %w", worktreeRoot, err)
	}
	sort.Strings(projects)
	return projects, nil
}

// listSolutionProjects runs `dotnet sln <solution> list`, resolves listed C#
// projects relative to the solution directory, and requires every existing
// .csproj listed by the solution to resolve inside worktreeRoot.
func listSolutionProjects(ctx context.Context, r cmdRunner, worktreeRoot, solutionRel, solutionAbs string) ([]string, error) {
	res, err := r.Run(ctx, worktreeRoot, "dotnet", "sln", solutionRel, "list")
	if err != nil {
		return nil, formatCmdError(fmt.Sprintf("list projects in solution %s", solutionRel), res, err)
	}

	lines := strings.Split(strings.ReplaceAll(res.Stdout, "\r\n", "\n"), "\n")
	if strings.Contains(res.Stdout, "No projects found") {
		return nil, nil
	}

	sepIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 3 && strings.Trim(trimmed, "-") == "" {
			sepIdx = i
			break
		}
	}
	if sepIdx < 0 {
		return nil, nil
	}

	slnDir := filepath.Dir(solutionAbs)
	var csProjects []string
	for _, line := range lines[sepIdx+1:] {
		entry := strings.TrimSpace(line)
		if entry == "" {
			continue
		}
		norm := filepath.FromSlash(strings.ReplaceAll(entry, "\\", "/"))
		abs := norm
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(slnDir, norm)
		}
		abs = filepath.Clean(abs)
		if !strings.EqualFold(filepath.Ext(abs), ".csproj") {
			continue
		}
		inside, err := isWithinRoot(abs, worktreeRoot)
		if err != nil || !inside {
			return nil, fmt.Errorf(
				"solution %s references C# project %q outside the Git worktree %s",
				solutionRel, entry, worktreeRoot,
			)
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			return nil, fmt.Errorf(
				"solution %s references missing C# project %q (%s)",
				solutionRel, entry, abs,
			)
		}
		csProjects = append(csProjects, abs)
	}
	return csProjects, nil
}

// sourceItemBaseline maps (variantKey -> itemType -> set of canonical full paths).
type sourceItemBaseline map[string]map[string]map[string]struct{}

func variantKey(v evalVariant) string {
	return v.Configuration + "|" + strings.ToLower(v.TFM)
}

func captureSourceBaseline(pe *projectEval) sourceItemBaseline {
	base := make(sourceItemBaseline, len(pe.Variants))
	projDir := filepath.Dir(pe.ProjectAbs)
	for _, v := range pe.Variants {
		vk := variantKey(v)
		perType := make(map[string]map[string]struct{}, len(sourceIsolationItemTypes))
		for _, typ := range sourceIsolationItemTypes {
			set := map[string]struct{}{}
			for _, it := range v.Items[typ] {
				fp := resolveEvalItemPath(projDir, it)
				if fp != "" {
					set[canonicalForComparison(fp)] = struct{}{}
				}
			}
			perType[typ] = set
		}
		base[vk] = perType
	}
	return base
}

func resolveEvalItemPath(projDir string, it msbuildItem) string {
	if p := strings.TrimSpace(it.FullPath); p != "" {
		return normalizeMSBuildPath(projDir, p)
	}
	if p := strings.TrimSpace(it.Identity); p != "" {
		return normalizeMSBuildPath(projDir, p)
	}
	return ""
}

type leakedItem struct {
	ItemType        string
	FullPath        string
	DefiningProject string
	Configuration   string
	TFM             string
}

func findLeakedServiceItems(pe *projectEval, baseline sourceItemBaseline, serviceDirCanon string) []leakedItem {
	projDir := filepath.Dir(pe.ProjectAbs)
	var leaks []leakedItem
	for _, v := range pe.Variants {
		vk := variantKey(v)
		for _, typ := range sourceIsolationItemTypes {
			baseSet := baseline[vk][typ]
			for _, it := range v.Items[typ] {
				fp := resolveEvalItemPath(projDir, it)
				if fp == "" {
					continue
				}
				canon := canonicalForComparison(fp)
				if _, existed := baseSet[canon]; existed {
					continue
				}
				if pathHasPrefixDir(canon, serviceDirCanon) {
					leaks = append(leaks, leakedItem{
						ItemType:        typ,
						FullPath:        fp,
						DefiningProject: strings.TrimSpace(it.DefiningProjectFullPath),
						Configuration:   v.Configuration,
						TFM:             v.TFM,
					})
				}
			}
		}
	}
	return leaks
}

func verifyBaselineExactMatch(pe *projectEval, baseline sourceItemBaseline, serviceDirCanon string) error {
	if leaks := findLeakedServiceItems(pe, baseline, serviceDirCanon); len(leaks) > 0 {
		first := leaks[0]
		defNote := ""
		if first.DefiningProject != "" {
			defNote = fmt.Sprintf(" (defined by %s)", normalizeMSBuildPath("", first.DefiningProject))
		}
		return fmt.Errorf(
			"host project %s still captures generated service file %s as %s in %s%s after adding local exclusions%s; check custom host MSBuild targets/imports",
			pe.ProjectRel, first.FullPath, first.ItemType, first.Configuration, formatTFMSuffix(first.TFM), defNote,
		)
	}

	projDir := filepath.Dir(pe.ProjectAbs)
	for _, v := range pe.Variants {
		vk := variantKey(v)
		for _, typ := range sourceIsolationItemTypes {
			baseSet := baseline[vk][typ]
			curSet := map[string]struct{}{}
			for _, it := range v.Items[typ] {
				fp := resolveEvalItemPath(projDir, it)
				if fp != "" {
					curSet[canonicalForComparison(fp)] = struct{}{}
				}
			}
			for orig := range baseSet {
				if _, ok := curSet[orig]; !ok {
					return fmt.Errorf(
						"host project %s lost pre-existing %s item %s in %s%s after adding service exclusions",
						pe.ProjectRel, typ, orig, v.Configuration, formatTFMSuffix(v.TFM),
					)
				}
			}
			for cur := range curSet {
				if _, ok := baseSet[cur]; !ok && pathHasPrefixDir(cur, serviceDirCanon) {
					return fmt.Errorf(
						"host project %s captured unexpected service %s item %s in %s%s",
						pe.ProjectRel, typ, cur, v.Configuration, formatTFMSuffix(v.TFM),
					)
				}
			}
		}
	}
	return nil
}

// isolateHostProjects checks every discovered existing in-worktree .csproj for
// leaked service files, appends a narrow Forge isolation ItemGroup to any
// capturing project, and re-evaluates to prove baseline preservation.
func isolateHostProjects(
	ctx context.Context,
	r cmdRunner,
	worktreeRoot string,
	serviceRelPath string,
	serviceDirAbs string,
	serviceDirCanon string,
	existingProjects []string,
	baselines map[string]sourceItemBaseline,
	tx *transaction,
) error {
	var edited []string
	for _, projAbs := range existingProjects {
		pe, err := evalProjectAllConfigs(ctx, r, worktreeRoot, projAbs)
		if err != nil {
			return err
		}
		leaks := findLeakedServiceItems(pe, baselines[projAbs], serviceDirCanon)
		if len(leaks) == 0 {
			continue
		}

		projRelService, err := filepath.Rel(filepath.Dir(projAbs), serviceDirAbs)
		if err != nil {
			return fmt.Errorf("compute relative service path from %s: %w", pe.ProjectRel, err)
		}
		projRelService = filepath.ToSlash(projRelService)

		if err := tx.snapshotSharedFile(projAbs); err != nil {
			return err
		}
		canon, err := canonicalPath(projAbs)
		if err != nil {
			return err
		}
		snap := tx.sharedSnapshots[canon]
		updated, err := insertServiceIsolationGroup(snap.originalBytes, serviceRelPath, projRelService)
		if err != nil {
			return fmt.Errorf("insert service isolation into %s: %w", pe.ProjectRel, err)
		}
		if err := tx.writeSharedFile(projAbs, updated); err != nil {
			return err
		}
		edited = append(edited, projAbs)
	}

	// Re-evaluate edited projects (and verify exact baseline match).
	for _, projAbs := range edited {
		pe, err := evalProjectAllConfigs(ctx, r, worktreeRoot, projAbs)
		if err != nil {
			return err
		}
		if err := verifyBaselineExactMatch(pe, baselines[projAbs], serviceDirCanon); err != nil {
			return err
		}
	}

	return nil
}

// verifyAllHostProjectsIsolated re-evaluates all existing projects for a given
// configuration (called after restore to catch NuGet-imported globs).
func verifyAllHostProjectsIsolated(
	ctx context.Context,
	r cmdRunner,
	worktreeRoot string,
	serviceDirCanon string,
	existingProjects []string,
	baselines map[string]sourceItemBaseline,
) error {
	for _, projAbs := range existingProjects {
		pe, err := evalProjectAllConfigs(ctx, r, worktreeRoot, projAbs)
		if err != nil {
			return err
		}
		if err := verifyBaselineExactMatch(pe, baselines[projAbs], serviceDirCanon); err != nil {
			return err
		}
	}
	return nil
}

// registerProjectsInSolution registers all generated projects in a temporary
// sibling copy of the selected solution (retaining extension and relative-path
// base) via native `dotnet sln add`, then atomically replaces the original
// solution after verifying its snapshot.
func registerProjectsInSolution(
	ctx context.Context,
	r cmdRunner,
	worktreeRoot string,
	solutionAbs string,
	generatedProjRels []string,
	tx *transaction,
) error {
	canon, err := canonicalPath(solutionAbs)
	if err != nil {
		return err
	}
	snap, ok := tx.sharedSnapshots[canon]
	if !ok {
		return fmt.Errorf("internal error: solution %s was not snapshotted", solutionAbs)
	}

	slnDir := filepath.Dir(solutionAbs)
	ext := filepath.Ext(solutionAbs)
	tmpFile, err := os.CreateTemp(slnDir, ".forge-sln-*"+ext)
	if err != nil {
		return fmt.Errorf("create temporary solution copy: %w", err)
	}
	tmpAbs := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpAbs)
	}()

	if _, err := tmpFile.Write(snap.originalBytes); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temporary solution copy: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temporary solution copy: %w", err)
	}

	tmpRel, err := relToWorktree(worktreeRoot, tmpAbs)
	if err != nil {
		return err
	}

	args := make([]string, 0, 3+len(generatedProjRels))
	args = append(args, "sln", tmpRel, "add")
	args = append(args, generatedProjRels...)

	res, err := r.Run(ctx, worktreeRoot, "dotnet", args...)
	if err != nil {
		return formatCmdError("register generated projects in solution", res, err)
	}

	updatedBytes, err := os.ReadFile(tmpAbs)
	if err != nil {
		return fmt.Errorf("read updated temporary solution copy: %w", err)
	}

	return tx.writeSharedFile(solutionAbs, updatedBytes)
}
