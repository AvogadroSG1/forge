package addcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const requiredTargetFramework = "net10.0"

var buildConfigurations = []string{"Debug", "Release"}

var msbuildQueryProperties = strings.Join([]string{
	"TargetFramework",
	"TargetFrameworks",
	"Nullable",
	"ImplicitUsings",
	"GenerateDocumentationFile",
	"ManagePackageVersionsCentrally",
	"DirectoryPackagesPropsPath",
	"DirectoryBuildPropsPath",
	"DirectoryBuildTargetsPath",
	"BaseOutputPath",
	"BaseIntermediateOutputPath",
	"IntermediateOutputPath",
	"OutputPath",
	"AssemblyName",
	"RestorePackagesWithLockFile",
	"RestoreLockedMode",
	"NuGetLockFilePath",
}, ",")

var msbuildQueryItems = strings.Join([]string{
	"PackageReference",
	"PackageVersion",
	"GlobalPackageReference",
	"ProjectReference",
	"Compile",
	"Content",
	"None",
	"EmbeddedResource",
	"RazorComponent",
	"RazorGenerate",
}, ",")

var sourceIsolationItemTypes = []string{
	"Compile",
	"Content",
	"None",
	"EmbeddedResource",
	"RazorComponent",
	"RazorGenerate",
}

// cmdResult holds captured stdout/stderr from a direct external process call.
type cmdResult struct {
	Stdout string
	Stderr string
}

// cmdRunner executes external commands (git, dotnet) with captured output.
// Private to internal/add so delegate.Runner stays narrow.
type cmdRunner interface {
	Run(ctx context.Context, dir, name string, args ...string) (cmdResult, error)
}

type execCmdRunner struct{}

func (execCmdRunner) Run(ctx context.Context, dir, name string, args ...string) (cmdResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return cmdResult{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func formatCmdError(desc string, res cmdResult, err error) error {
	var parts []string
	if s := strings.TrimSpace(res.Stdout); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(res.Stderr); s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return fmt.Errorf("%s: %w", desc, err)
	}
	return fmt.Errorf("%s: %w\n%s", desc, err, strings.Join(parts, "\n"))
}

// checkHostDotnetSDK verifies the host's active dotnet executable (respecting
// worktree global.json and environment) is present and can target net10.0.
func checkHostDotnetSDK(ctx context.Context, r cmdRunner, worktreeRoot string) (string, error) {
	res, err := r.Run(ctx, worktreeRoot, "dotnet", "--version")
	if err != nil {
		return "", formatCmdError("host dotnet SDK is unavailable or incompatible (check global.json and installed .NET 10 SDK)", res, err)
	}
	version := strings.TrimSpace(res.Stdout)
	majorStr, _, _ := strings.Cut(version, ".")
	major, parseErr := strconv.Atoi(majorStr)
	if parseErr != nil || major < 10 {
		return "", fmt.Errorf(
			"host dotnet SDK version %q cannot target %s required by the selected C# template (check global.json or host SDK installation; forge add will not upgrade or override the host SDK)",
			version, requiredTargetFramework,
		)
	}
	return version, nil
}

type msbuildEvalResponse struct {
	Properties map[string]string        `json:"Properties"`
	Items      map[string][]msbuildItem `json:"Items"`
}

type msbuildItem struct {
	Identity                string `json:"Identity"`
	FullPath                string `json:"FullPath"`
	Version                 string `json:"Version"`
	DefiningProjectFullPath string `json:"DefiningProjectFullPath"`
}

type evalVariant struct {
	Configuration string
	TFM           string // empty when evaluated without -p:TargetFramework
	Properties    map[string]string
	Items         map[string][]msbuildItem
}

type projectEval struct {
	ProjectAbs string
	ProjectRel string
	Outer      map[string]evalVariant // keyed by Configuration
	Variants   []evalVariant          // inner builds when TargetFrameworks is set, else outer builds
}

func evalMSBuildSingle(ctx context.Context, r cmdRunner, worktreeRoot, projAbs, cfg, tfm string) (evalVariant, error) {
	args := []string{
		"msbuild",
		projAbs,
		"-nologo",
		"-p:Configuration=" + cfg,
	}
	if tfm != "" {
		args = append(args, "-p:TargetFramework="+tfm)
	}
	args = append(args,
		"-getProperty:"+msbuildQueryProperties,
		"-getItem:"+msbuildQueryItems,
	)

	res, err := r.Run(ctx, worktreeRoot, "dotnet", args...)
	if err != nil {
		return evalVariant{}, formatCmdError(fmt.Sprintf("evaluate MSBuild project %s (%s%s)", projAbs, cfg, formatTFMSuffix(tfm)), res, err)
	}

	var parsed msbuildEvalResponse
	if jsonErr := json.Unmarshal([]byte(res.Stdout), &parsed); jsonErr != nil {
		return evalVariant{}, fmt.Errorf("parse MSBuild evaluation JSON for %s (%s%s): %w\nstdout:\n%s\nstderr:\n%s",
			projAbs, cfg, formatTFMSuffix(tfm), jsonErr, res.Stdout, res.Stderr)
	}

	if parsed.Properties == nil {
		parsed.Properties = map[string]string{}
	}
	if parsed.Items == nil {
		parsed.Items = map[string][]msbuildItem{}
	}
	return evalVariant{
		Configuration: cfg,
		TFM:           tfm,
		Properties:    parsed.Properties,
		Items:         parsed.Items,
	}, nil
}

func formatTFMSuffix(tfm string) string {
	if tfm == "" {
		return ""
	}
	return "/" + tfm
}

func parseTFMList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ";") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func evalProjectAllConfigs(ctx context.Context, r cmdRunner, worktreeRoot, projAbs string) (*projectEval, error) {
	rel, err := relToWorktree(worktreeRoot, projAbs)
	if err != nil {
		rel = projAbs
	}
	pe := &projectEval{
		ProjectAbs: projAbs,
		ProjectRel: rel,
		Outer:      make(map[string]evalVariant, len(buildConfigurations)),
	}

	for _, cfg := range buildConfigurations {
		outer, err := evalMSBuildSingle(ctx, r, worktreeRoot, projAbs, cfg, "")
		if err != nil {
			return nil, err
		}
		pe.Outer[cfg] = outer

		tfms := parseTFMList(outer.Properties["TargetFrameworks"])
		if len(tfms) > 0 {
			for _, tfm := range tfms {
				inner, err := evalMSBuildSingle(ctx, r, worktreeRoot, projAbs, cfg, tfm)
				if err != nil {
					return nil, err
				}
				pe.Variants = append(pe.Variants, inner)
			}
		} else {
			pe.Variants = append(pe.Variants, outer)
		}
	}

	return pe, nil
}

// validateServiceTargetFrameworks requires every generated project to target
// exactly net10.0 across both Debug and Release configurations.
func validateServiceTargetFrameworks(serviceEvals []*projectEval) error {
	for _, pe := range serviceEvals {
		for _, cfg := range buildConfigurations {
			outer := pe.Outer[cfg]
			tfms := parseTFMList(outer.Properties["TargetFrameworks"])
			hostControls := describeHostBuildControls(outer.Properties)
			if len(tfms) > 0 {
				if len(tfms) != 1 || !strings.EqualFold(tfms[0], requiredTargetFramework) {
					return fmt.Errorf(
						"generated project %s (%s) inherited incompatible TargetFrameworks=%q (template requires exactly %s)%s",
						pe.ProjectRel, cfg, outer.Properties["TargetFrameworks"], requiredTargetFramework, hostControls,
					)
				}
				continue
			}
			tfm := strings.TrimSpace(outer.Properties["TargetFramework"])
			if !strings.EqualFold(tfm, requiredTargetFramework) {
				return fmt.Errorf(
					"generated project %s (%s) inherited incompatible TargetFramework=%q (template requires %s)%s",
					pe.ProjectRel, cfg, tfm, requiredTargetFramework, hostControls,
				)
			}
		}
	}
	return nil
}

func describeHostBuildControls(props map[string]string) string {
	var sources []string
	if p := strings.TrimSpace(props["DirectoryBuildPropsPath"]); p != "" {
		sources = append(sources, "Directory.Build.props="+normalizeMSBuildPath("", p))
	}
	if p := strings.TrimSpace(props["DirectoryBuildTargetsPath"]); p != "" {
		sources = append(sources, "Directory.Build.targets="+normalizeMSBuildPath("", p))
	}
	if len(sources) == 0 {
		return ""
	}
	return fmt.Sprintf(" (controlling host settings: %s)", strings.Join(sources, ", "))
}

// reconcileServicePackages aligns each generated project with host CPM and
// host-provided GlobalPackageReference/common PackageReference entries, and
// updates controlling Directory.Packages.props files with genuinely missing IDs.
func reconcileServicePackages(
	worktreeRoot string,
	serviceEvals []*projectEval,
	tx *transaction,
) error {
	// Track missing central pins grouped by canonical controlling Directory.Packages.props path.
	missingByPropsFile := map[string]map[string]packagePin{}
	propsOrigPath := map[string]string{}

	for _, pe := range serviceEvals {
		projCanon, err := canonicalPath(pe.ProjectAbs)
		if err != nil {
			return err
		}
		currentBytes, err := os.ReadFile(pe.ProjectAbs)
		if err != nil {
			return fmt.Errorf("read generated project %s: %w", pe.ProjectRel, err)
		}
		pins, err := extractTemplatePackagePins(currentBytes)
		if err != nil {
			return fmt.Errorf("parse package references in %s: %w", pe.ProjectRel, err)
		}

		// Determine CPM setting across configurations.
		var cpmStates []bool
		for _, v := range pe.Variants {
			cpmStates = append(cpmStates, strings.EqualFold(strings.TrimSpace(v.Properties["ManagePackageVersionsCentrally"]), "true"))
		}
		cpmEnabled := cpmStates[0]
		for _, s := range cpmStates[1:] {
			if s != cpmEnabled {
				return fmt.Errorf(
					"generated project %s inherits conditional ManagePackageVersionsCentrally across configurations; configure CPM uniformly before running forge add",
					pe.ProjectRel,
				)
			}
		}

		// Identify host-provided packages (GlobalPackageReference or PackageReference
		// defined outside the generated .csproj) so we do not duplicate them.
		hostProvided := map[string]bool{}
		for _, v := range pe.Variants {
			for _, gpr := range v.Items["GlobalPackageReference"] {
				if id := strings.TrimSpace(gpr.Identity); id != "" {
					hostProvided[strings.ToLower(id)] = true
				}
			}
			for _, pr := range v.Items["PackageReference"] {
				id := strings.TrimSpace(pr.Identity)
				if id == "" {
					continue
				}
				defPath := strings.TrimSpace(pr.DefiningProjectFullPath)
				if defPath == "" {
					continue
				}
				defCanon, cErr := canonicalPath(normalizeMSBuildPath(filepath.Dir(pe.ProjectAbs), defPath))
				if cErr == nil && defCanon != projCanon {
					hostProvided[strings.ToLower(id)] = true
				}
			}
		}

		if cpmEnabled {
			// Validate controlling Directory.Packages.props and check each required package ID.
			var missingPins []packagePin
			for _, pin := range pins {
				lowerID := strings.ToLower(pin.ID)
				if hostProvided[lowerID] {
					continue
				}
				presentCount := 0
				for _, v := range pe.Variants {
					if hasEffectivePackageVersion(v.Items["PackageVersion"], lowerID) {
						presentCount++
					}
				}
				switch {
				case presentCount == len(pe.Variants):
					// Host central version already applies across all evaluated configs/TFMs.
					continue
				case presentCount > 0:
					return fmt.Errorf(
						"central package %q for %s is only conditionally active across configurations/targets; provide an unconditional PackageVersion in Directory.Packages.props before running forge add",
						pin.ID, pe.ProjectRel,
					)
				default:
					missingPins = append(missingPins, pin)
				}
			}

			if len(missingPins) > 0 {
				// Determine controlling Directory.Packages.props path.
				propsPath, err := resolveControllingPackagesProps(pe, worktreeRoot)
				if err != nil {
					return err
				}
				propsCanon, err := canonicalPath(propsPath)
				if err != nil {
					return err
				}

				declared, err := inspectDeclaredCentralPackages(propsPath, worktreeRoot)
				if err != nil {
					return fmt.Errorf("inspect %s: %w", propsPath, err)
				}

				for _, pin := range missingPins {
					lowerID := strings.ToLower(pin.ID)
					if declFile, isConditional := declared[lowerID]; isConditional {
						relDecl, _ := relToWorktree(worktreeRoot, declFile)
						if relDecl == "" {
							relDecl = declFile
						}
						return fmt.Errorf(
							"central package %q is conditionally declared in %s but has no effective PackageVersion for %s; resolve the condition rather than adding a duplicate PackageVersion",
							pin.ID, relDecl, pe.ProjectRel,
						)
					}
					if pin.Version == "" {
						return fmt.Errorf("template package %q in %s has no pinned version for CPM registration", pin.ID, pe.ProjectRel)
					}
					if missingByPropsFile[propsCanon] == nil {
						missingByPropsFile[propsCanon] = map[string]packagePin{}
						propsOrigPath[propsCanon] = propsPath
					}
					if _, exists := missingByPropsFile[propsCanon][lowerID]; !exists {
						missingByPropsFile[propsCanon][lowerID] = pin
					}
				}
			}
		}

		updated, changed, err := rewriteServiceProjectPackages(currentBytes, cpmEnabled, hostProvided)
		if err != nil {
			return fmt.Errorf("update package references in %s: %w", pe.ProjectRel, err)
		}
		if changed {
			if err := tx.writeServiceFile(pe.ProjectAbs, updated, 0o644); err != nil {
				return err
			}
		}
	}

	// Apply missing PackageVersion additions to each controlling Directory.Packages.props.
	propsKeys := make([]string, 0, len(missingByPropsFile))
	for k := range missingByPropsFile {
		propsKeys = append(propsKeys, k)
	}
	sort.Strings(propsKeys)

	for _, canonKey := range propsKeys {
		propsPath := propsOrigPath[canonKey]
		if err := tx.snapshotSharedFile(propsPath); err != nil {
			return err
		}
		snap := tx.sharedSnapshots[canonKey]
		pins := make([]packagePin, 0, len(missingByPropsFile[canonKey]))
		for _, p := range missingByPropsFile[canonKey] {
			pins = append(pins, p)
		}
		updated, err := appendCentralPackageVersions(snap.originalBytes, pins)
		if err != nil {
			rel, _ := relToWorktree(worktreeRoot, propsPath)
			return fmt.Errorf("update %s: %w", rel, err)
		}
		if err := tx.writeSharedFile(propsPath, updated); err != nil {
			return err
		}
	}

	return nil
}

func hasEffectivePackageVersion(items []msbuildItem, lowerID string) bool {
	for _, it := range items {
		if strings.EqualFold(strings.TrimSpace(it.Identity), lowerID) && strings.TrimSpace(it.Version) != "" {
			return true
		}
	}
	return false
}

func resolveControllingPackagesProps(pe *projectEval, worktreeRoot string) (string, error) {
	var resolved string
	for _, v := range pe.Variants {
		raw := strings.TrimSpace(v.Properties["DirectoryPackagesPropsPath"])
		if raw == "" {
			return "", fmt.Errorf(
				"ManagePackageVersionsCentrally is enabled for %s (%s), but DirectoryPackagesPropsPath could not be determined",
				pe.ProjectRel, v.Configuration,
			)
		}
		abs := normalizeMSBuildPath(filepath.Dir(pe.ProjectAbs), raw)
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf(
				"controlling Directory.Packages.props %q for %s does not exist as a regular file",
				abs, pe.ProjectRel,
			)
		}
		inside, err := isWithinRoot(abs, worktreeRoot)
		if err != nil || !inside {
			return "", fmt.Errorf(
				"controlling Directory.Packages.props %q for %s is outside the Git worktree %s; refusing to edit outside the host repository",
				abs, pe.ProjectRel, worktreeRoot,
			)
		}
		canon, err := canonicalPath(abs)
		if err != nil {
			return "", err
		}
		if resolved == "" {
			resolved = canon
		} else if resolved != canon {
			return "", fmt.Errorf(
				"project %s resolves to different Directory.Packages.props files across configurations (%s vs %s)",
				pe.ProjectRel, resolved, canon,
			)
		}
	}
	return resolved, nil
}

// checkProjectCollisions verifies that generated service projects do not
// collide with existing solution projects (or each other) on project name,
// assembly identity, or evaluated intermediate/output directories.
func checkProjectCollisions(serviceEvals, solutionEvals []*projectEval) error {
	type identityOwner struct {
		projRel string
	}

	// Check project stems and assembly names.
	byStem := map[string]identityOwner{}
	byAssembly := map[string]identityOwner{}
	// Keyed by configuration + "|" + tfm + "|" + canonicalDir.
	byIntermediate := map[string]identityOwner{}
	byOutput := map[string]identityOwner{}

	register := func(pe *projectEval, isNew bool) error {
		stem := strings.ToLower(strings.TrimSuffix(filepath.Base(pe.ProjectAbs), filepath.Ext(pe.ProjectAbs)))
		if existing, ok := byStem[stem]; ok && isNew {
			return fmt.Errorf("project name collision: new project %s conflicts with existing project %s", pe.ProjectRel, existing.projRel)
		}
		if !isNew {
			byStem[stem] = identityOwner{projRel: pe.ProjectRel}
		} else {
			byStem[stem] = identityOwner{projRel: pe.ProjectRel}
		}

		seenAsmForProj := map[string]bool{}
		for _, v := range pe.Variants {
			asm := strings.ToLower(strings.TrimSpace(v.Properties["AssemblyName"]))
			if asm != "" && !seenAsmForProj[asm] {
				seenAsmForProj[asm] = true
				if existing, ok := byAssembly[asm]; ok && existing.projRel != pe.ProjectRel {
					return fmt.Errorf(
						"assembly identity collision (%q): new project %s conflicts with %s",
						strings.TrimSpace(v.Properties["AssemblyName"]), pe.ProjectRel, existing.projRel,
					)
				}
				byAssembly[asm] = identityOwner{projRel: pe.ProjectRel}
			}

			projDir := filepath.Dir(pe.ProjectAbs)
			if inter := strings.TrimSpace(v.Properties["IntermediateOutputPath"]); inter != "" {
				interAbs := normalizeMSBuildPath(projDir, inter)
				interKey := v.Configuration + "|" + strings.ToLower(v.TFM) + "|" + canonicalForComparison(interAbs)
				if existing, ok := byIntermediate[interKey]; ok && existing.projRel != pe.ProjectRel {
					return fmt.Errorf(
						"shared intermediate directory collision (%s in %s%s): %s conflicts with %s",
						interAbs, v.Configuration, formatTFMSuffix(v.TFM), pe.ProjectRel, existing.projRel,
					)
				}
				byIntermediate[interKey] = identityOwner{projRel: pe.ProjectRel}
			}

			if outDir := strings.TrimSpace(v.Properties["OutputPath"]); outDir != "" {
				outAbs := normalizeMSBuildPath(projDir, outDir)
				outKey := v.Configuration + "|" + strings.ToLower(v.TFM) + "|" + canonicalForComparison(outAbs)
				if existing, ok := byOutput[outKey]; ok && existing.projRel != pe.ProjectRel {
					return fmt.Errorf(
						"shared output directory collision (%s in %s%s): %s conflicts with %s",
						outAbs, v.Configuration, formatTFMSuffix(v.TFM), pe.ProjectRel, existing.projRel,
					)
				}
				byOutput[outKey] = identityOwner{projRel: pe.ProjectRel}
			}
		}
		return nil
	}

	for _, pe := range solutionEvals {
		if err := register(pe, false); err != nil {
			return err
		}
	}
	for _, pe := range serviceEvals {
		if err := register(pe, true); err != nil {
			return err
		}
	}
	return nil
}

type lockfileGuard struct {
	worktreeRoot    string
	serviceDirCanon string
	hostSnapshots   map[string]string // canonicalPath -> absPath for pre-existing host lockfiles
	absentHostPaths map[string]string // canonicalPath -> absPath for host lockfile paths that must not appear
}

// prepareLockfileGuard inspects NuGet lockfile properties across all existing
// solution projects and generated service projects before restore.
func prepareLockfileGuard(
	worktreeRoot string,
	serviceDirCanon string,
	solutionEvals []*projectEval,
	serviceEvals []*projectEval,
	tx *transaction,
) (*lockfileGuard, error) {
	guard := &lockfileGuard{
		worktreeRoot:    worktreeRoot,
		serviceDirCanon: serviceDirCanon,
		hostSnapshots:   map[string]string{},
		absentHostPaths: map[string]string{},
	}

	// Inspect existing solution projects (outside the new service).
	for _, pe := range solutionEvals {
		projDir := filepath.Dir(pe.ProjectAbs)
		projStem := strings.TrimSuffix(filepath.Base(pe.ProjectAbs), filepath.Ext(pe.ProjectAbs))
		projSpecificName := "packages." + strings.ReplaceAll(projStem, " ", "_") + ".lock.json"

		for _, v := range pe.Variants {
			withLock := strings.EqualFold(strings.TrimSpace(v.Properties["RestorePackagesWithLockFile"]), "true")
			lockedMode := strings.EqualFold(strings.TrimSpace(v.Properties["RestoreLockedMode"]), "true")
			customRaw := strings.TrimSpace(v.Properties["NuGetLockFilePath"])

			var effectivePath string
			var candidates []string

			if customRaw != "" {
				customAbs := normalizeMSBuildPath(projDir, customRaw)
				inside, err := isWithinRoot(customAbs, worktreeRoot)
				if err != nil || !inside {
					return nil, fmt.Errorf(
						"host project %s configures NuGetLockFilePath %q outside the Git worktree %s",
						pe.ProjectRel, customAbs, worktreeRoot,
					)
				}
				effectivePath = customAbs
				candidates = []string{customAbs}
			} else {
				projSpecificPath := filepath.Join(projDir, projSpecificName)
				defaultPath := filepath.Join(projDir, "packages.lock.json")
				candidates = append(candidates, projSpecificPath, defaultPath)
				// Also include case-variant project-specific lockfiles if present on disk.
				if entries, err := os.ReadDir(projDir); err == nil {
					for _, e := range entries {
						name := e.Name()
						if !e.IsDir() && strings.HasPrefix(strings.ToLower(name), "packages.") && strings.HasSuffix(strings.ToLower(name), ".lock.json") {
							candidates = append(candidates, filepath.Join(projDir, name))
						}
					}
				}
				if fileExists(projSpecificPath) {
					effectivePath = projSpecificPath
				} else {
					effectivePath = defaultPath
				}
			}

			for _, cand := range candidates {
				if !fileExists(cand) {
					continue
				}
				canon := canonicalForComparison(cand)
				if _, seen := guard.hostSnapshots[canon]; !seen {
					if err := tx.snapshotProtectiveFile(cand); err != nil {
						return nil, err
					}
					guard.hostSnapshots[canon] = cand
				}
			}

			if !fileExists(effectivePath) {
				if withLock || lockedMode {
					relLock, _ := relToWorktree(worktreeRoot, effectivePath)
					if relLock == "" {
						relLock = effectivePath
					}
					return nil, fmt.Errorf(
						"host project %s enables NuGet lockfiles (%s) but %s does not exist; restore that host project first before running forge add",
						pe.ProjectRel, v.Configuration, relLock,
					)
				}
				canon := canonicalForComparison(effectivePath)
				guard.absentHostPaths[canon] = effectivePath
			}
		}
	}

	// Inspect generated service projects for custom NuGetLockFilePath escaping
	// the worktree or colliding with host paths outside the new service.
	for _, pe := range serviceEvals {
		projDir := filepath.Dir(pe.ProjectAbs)
		for _, v := range pe.Variants {
			withLock := strings.EqualFold(strings.TrimSpace(v.Properties["RestorePackagesWithLockFile"]), "true")
			lockedMode := strings.EqualFold(strings.TrimSpace(v.Properties["RestoreLockedMode"]), "true")
			customRaw := strings.TrimSpace(v.Properties["NuGetLockFilePath"])
			if customRaw == "" {
				continue
			}
			customAbs := normalizeMSBuildPath(projDir, customRaw)
			insideWorktree, err := isWithinRoot(customAbs, worktreeRoot)
			if err != nil || !insideWorktree {
				return nil, fmt.Errorf(
					"generated project %s inherits NuGetLockFilePath %q outside the Git worktree %s",
					pe.ProjectRel, customAbs, worktreeRoot,
				)
			}
			insideService, _ := isWithinRoot(customAbs, serviceDirCanon)
			if !insideService {
				if fileExists(customAbs) {
					canon := canonicalForComparison(customAbs)
					if _, seen := guard.hostSnapshots[canon]; !seen {
						if err := tx.snapshotProtectiveFile(customAbs); err != nil {
							return nil, err
						}
						guard.hostSnapshots[canon] = customAbs
					}
				} else if withLock || lockedMode {
					relLock, _ := relToWorktree(worktreeRoot, customAbs)
					return nil, fmt.Errorf(
						"generated project %s inherits NuGetLockFilePath %q outside the new service directory; refusing to create a lockfile outside the new service",
						pe.ProjectRel, relLock,
					)
				}
			}
		}
	}

	return guard, nil
}

// verifyHostLockfilesUnchanged ensures no pre-existing host lockfile drifted
// during restore and no unexpected lockfile was created for a host project.
func (g *lockfileGuard) verifyHostLockfilesUnchanged(tx *transaction) error {
	keys := make([]string, 0, len(g.hostSnapshots))
	for k := range g.hostSnapshots {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, canon := range keys {
		absPath := g.hostSnapshots[canon]
		snap := tx.protectiveSnapshots[canon]
		current, err := os.ReadFile(absPath)
		if err != nil {
			rel, _ := relToWorktree(g.worktreeRoot, absPath)
			return fmt.Errorf("host lockfile %s could not be read after restore: %w", rel, err)
		}
		if !bytes.Equal(current, snap.originalBytes) {
			rel, _ := relToWorktree(g.worktreeRoot, absPath)
			return fmt.Errorf("dependency drift detected: dotnet restore modified existing host lockfile %s", rel)
		}
	}

	absentKeys := make([]string, 0, len(g.absentHostPaths))
	for k := range g.absentHostPaths {
		absentKeys = append(absentKeys, k)
	}
	sort.Strings(absentKeys)

	for _, canon := range absentKeys {
		absPath := g.absentHostPaths[canon]
		if fileExists(absPath) {
			tx.unexpectedHostFiles = append(tx.unexpectedHostFiles, absPath)
			rel, _ := relToWorktree(g.worktreeRoot, absPath)
			return fmt.Errorf("dependency drift detected: dotnet restore created unexpected host lockfile %s outside the new service", rel)
		}
	}

	return nil
}

func normalizeMSBuildPath(baseDir, raw string) string {
	slash := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if slash == "" {
		return ""
	}
	p := filepath.FromSlash(slash)
	if !filepath.IsAbs(p) && baseDir != "" {
		p = filepath.Join(baseDir, p)
	}
	return filepath.Clean(p)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
