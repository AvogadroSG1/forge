package addcmd

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forge"
)

func TestRunRejectsInvalidInputsWithoutMutatingHost(t *testing.T) {
	t.Parallel()

	t.Run("outside git repository", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		err := Run(context.Background(), forge.Assets(), dir, Options{
			Stack:       "csharp-cli",
			ProjectName: "BillingWorker",
			Path:        "services/billing",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "Git worktree") {
			t.Fatalf("Run() error = %v, want Git worktree error", err)
		}
	})

	t.Run("invalid paths symlinks existing destination and solutions", func(t *testing.T) {
		t.Parallel()
		repo := newStaticHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		slnBefore := mustReadBytes(t, filepath.Join(repo, "Product.slnx"))

		// Create an existing empty directory, an existing file, a symlink dir, and a .slnf filter.
		existingEmpty := filepath.Join(repo, "services", "already-there")
		mustMkdirAll(t, existingEmpty)
		mustWriteFile(t, filepath.Join(repo, "Product.slnf"), `{"solution":{"path":"Product.slnx","projects":[]}}`)
		outsideDir := t.TempDir()
		symlinkDir := filepath.Join(repo, "linked-services")
		if err := os.Symlink(outsideDir, symlinkDir); err != nil {
			t.Fatalf("os.Symlink() error = %v", err)
		}

		cases := []struct {
			name        string
			opts        Options
			wantContain string
		}{
			{
				name:        "absolute path",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: filepath.Join(repo, "services/svc"), AuthorName: "CI Bot"},
				wantContain: "--path",
			},
			{
				name:        "traversing path",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: "../escape", AuthorName: "CI Bot"},
				wantContain: "--path",
			},
			{
				name:        "root path",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: ".", AuthorName: "CI Bot"},
				wantContain: "--path",
			},
			{
				name:        "reserved .git path",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: ".git/svc", AuthorName: "CI Bot"},
				wantContain: ".git",
			},
			{
				name:        "reserved .beads path",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: ".beads/svc", AuthorName: "CI Bot"},
				wantContain: ".beads",
			},
			{
				name:        "reserved .forge path",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: ".forge/svc", AuthorName: "CI Bot"},
				wantContain: ".forge",
			},
			{
				name:        "existing empty directory destination",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: "services/already-there", AuthorName: "CI Bot"},
				wantContain: "already exists",
			},
			{
				name:        "symlink path component",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: "linked-services/svc", AuthorName: "CI Bot"},
				wantContain: "symlink",
			},
			{
				name:        "invalid project name starting with digit",
				opts:        Options{Stack: "csharp-cli", ProjectName: "123Svc", Path: "services/svc", AuthorName: "CI Bot"},
				wantContain: "--project-name",
			},
			{
				name:        "unsupported stack",
				opts:        Options{Stack: "python-fastapi", ProjectName: "Svc", Path: "services/svc", AuthorName: "CI Bot"},
				wantContain: "--stack",
			},
			{
				name:        "slnf filter rejected",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: "services/svc", Solution: "Product.slnf", AuthorName: "CI Bot"},
				wantContain: ".slnf",
			},
			{
				name:        "missing explicit solution",
				opts:        Options{Stack: "csharp-cli", ProjectName: "Svc", Path: "services/svc", Solution: "Missing.slnx", AuthorName: "CI Bot"},
				wantContain: "--solution",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				err := Run(context.Background(), forge.Assets(), repo, tc.opts, &bytes.Buffer{})
				if err == nil {
					t.Fatalf("Run() error = nil, want error containing %q", tc.wantContain)
				}
				if !strings.Contains(err.Error(), tc.wantContain) {
					t.Fatalf("Run() error = %q, want substring %q", err.Error(), tc.wantContain)
				}
			})
		}

		if !bytes.Equal(mustReadBytes(t, filepath.Join(repo, "Product.slnx")), slnBefore) {
			t.Fatal("Product.slnx was mutated by rejected invocations")
		}
	})

	t.Run("missing and ambiguous root solutions", func(t *testing.T) {
		t.Parallel()
		repo := newStaticHostRepo(t, hostRepoSpec{solutionName: "First.slnx"})
		mustWriteFile(t, filepath.Join(repo, "Second.sln"), "")

		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "Svc",
			Path:        "services/svc",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "ambiguous solution") {
			t.Fatalf("Run(ambiguous solution) error = %v, want ambiguous solution error", err)
		}

		mustRemove(t, filepath.Join(repo, "First.slnx"))
		mustRemove(t, filepath.Join(repo, "Second.sln"))
		err = Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "Svc",
			Path:        "services/svc",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--solution") {
			t.Fatalf("Run(no solution) error = %v, want missing --solution error", err)
		}
	})

	t.Run("missing author when git user.name unset", func(t *testing.T) {
		t.Parallel()
		repo := newStaticHostRepo(t, hostRepoSpec{solutionName: "Product.slnx", skipGitAuthor: true})
		// Inject a runner whose `git config user.name` returns empty so global ~/.gitconfig doesn't mask the test.
		fakeGitNoAuthor := &interceptRunner{
			inner: execCmdRunner{},
			fn: func(ctx context.Context, dir, name string, args ...string) (cmdResult, error, bool) {
				if name == "git" && len(args) == 2 && args[0] == "config" && args[1] == "user.name" {
					return cmdResult{}, errors.New("exit status 1"), true
				}
				return cmdResult{}, nil, false
			},
		}
		err := runWithRunner(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "BillingWorker",
			Path:        "services/billing",
		}, &bytes.Buffer{}, fakeGitNoAuthor, nil)
		if err == nil || !strings.Contains(err.Error(), "missing required flag: --author-name") {
			t.Fatalf("error = %v, want missing required flag: --author-name", err)
		}
	})
}

func TestRunNestedInvocationPreservesRootSentinelsAndSupportsWorktrees(t *testing.T) {
	t.Parallel()

	mainRepo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
	// Add sentinel host-root files that forge add must NEVER modify.
	sentinels := map[string]string{
		"AGENTS.md":                "# Host Monorepo Instructions\n",
		"CLAUDE.md":                "# Host Claude Instructions\n",
		"mise.toml":                "[tools]\ndotnet = \"10\"\n",
		"lefthook.yml":             "pre-commit:\n  commands: {}\n",
		".github/workflows/ci.yml": "name: host-ci\n",
		".beads/config.yaml":       "prefix: host\n",
		".forge/manifest.json":     "{\"version\":1,\"stack\":\"custom\"}\n",
	}
	for rel, body := range sentinels {
		mustWriteFile(t, filepath.Join(mainRepo, filepath.FromSlash(rel)), body)
	}
	runGit(t, mainRepo, "add", ".")
	runGit(t, mainRepo, "commit", "-m", "initial host")

	// Create a linked Git worktree where .git is a file, not a directory.
	wtParent := t.TempDir()
	wtDir := filepath.Join(wtParent, "wt")
	runGit(t, mainRepo, "worktree", "add", "-b", "feature/add-svc", wtDir)

	nestedCwd := filepath.Join(wtDir, "src", "ExistingApp")
	existingProjBefore := mustReadBytes(t, filepath.Join(wtDir, "src", "ExistingApp", "ExistingApp.csproj"))
	gitConfigBefore := mustReadBytes(t, filepath.Join(mainRepo, ".git", "config"))

	var out bytes.Buffer
	err := Run(context.Background(), forge.Assets(), nestedCwd, Options{
		Stack:       "csharp-cli",
		ProjectName: "BillingWorker",
		Path:        "services/billing-worker",
		AuthorName:  "CI Bot",
	}, &out)
	if err != nil {
		t.Fatalf("Run() from nested worktree dir error = %v", err)
	}

	// Verify already-isolated sibling host project was NOT edited.
	if !bytes.Equal(mustReadBytes(t, filepath.Join(wtDir, "src", "ExistingApp", "ExistingApp.csproj")), existingProjBefore) {
		t.Fatal("already-isolated sibling project src/ExistingApp/ExistingApp.csproj should not have been edited")
	}

	// Verify all host sentinel files and git config are untouched byte-for-byte.
	for rel, want := range sentinels {
		got := string(mustReadBytes(t, filepath.Join(wtDir, filepath.FromSlash(rel))))
		if got != want {
			t.Fatalf("host sentinel %s changed: got %q, want %q", rel, got, want)
		}
	}
	if !bytes.Equal(mustReadBytes(t, filepath.Join(mainRepo, ".git", "config")), gitConfigBefore) {
		t.Fatal("host .git/config changed")
	}

	// Verify service-local AGENTS.md has exact relative link and root-relative commands, and no forbidden repo files exist under service.
	svcDir := filepath.Join(wtDir, "services", "billing-worker")
	svcAgents := string(mustReadBytes(t, filepath.Join(svcDir, "AGENTS.md")))
	for _, want := range []string{"../..", "BillingWorker", "csharp-cli", "dotnet test Product.slnx", "dotnet run --project services/billing-worker/BillingWorker.csproj"} {
		if !strings.Contains(svcAgents, want) {
			t.Fatalf("service AGENTS.md missing %q:\n%s", want, svcAgents)
		}
	}
	for _, forbidden := range []string{"CLAUDE.md", ".gitignore", "mise.toml", "lefthook.yml", ".beads", ".claude", ".codex", ".opencode", ".forge", ".github"} {
		if _, err := os.Lstat(filepath.Join(svcDir, forbidden)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("forbidden repository artifact %q created under service dir", forbidden)
		}
	}

	// Verify summary output names generated projects and solution.
	summary := out.String()
	for _, want := range []string{"services/billing-worker/BillingWorker.csproj", "services/billing-worker/tests/BillingWorker.Tests/BillingWorker.Tests.csproj", "Product.slnx"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary output missing %q:\n%s", want, summary)
		}
	}

	// Run real dotnet test across the solution to confirm tests pass!
	runCmd(t, wtDir, "dotnet", "test", "Product.slnx", "--no-build", "--configuration", "Debug")
}

func TestRunIsolatesCapturingParentNonSolutionSiblingAndSupportsSecondService(t *testing.T) {
	t.Parallel()

	repo := newTestHostRepo(t, hostRepoSpec{
		solutionName:      "Product.slnx",
		rootLevelWebHost:  true,
		nonSolutionGlobTo: "services",
	})

	// Preserve CRLF + UTF-8 BOM + comment in root project to prove byte-preserving insertion.
	rootProjPath := filepath.Join(repo, "HostWeb.csproj")
	rootProjWithBOM := "\xEF\xBB\xBF<Project Sdk=\"Microsoft.NET.Sdk.Web\">\r\n  <!-- Keep this host comment -->\r\n  <PropertyGroup>\r\n    <TargetFramework>net10.0</TargetFramework>\r\n    <Nullable>enable</Nullable>\r\n    <ImplicitUsings>enable</ImplicitUsings>\r\n  </PropertyGroup>\r\n  <ItemGroup>\r\n    <Compile Remove=\"non-solution/**\" />\r\n    <Content Remove=\"non-solution/**\" />\r\n    <None Remove=\"non-solution/**\" />\r\n  </ItemGroup>\r\n</Project>\r\n"
	mustWriteFile(t, rootProjPath, rootProjWithBOM)

	// First service.
	if err := Run(context.Background(), forge.Assets(), repo, Options{
		Stack:       "csharp-cli",
		ProjectName: "FirstWorker",
		Path:        "services/first-worker",
		AuthorName:  "CI Bot",
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}

	// Verify BOM, comment, CRLF preserved in HostWeb.csproj, and narrow exclusion appended.
	rootAfter := mustReadBytes(t, rootProjPath)
	if !bytes.HasPrefix(rootAfter, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("HostWeb.csproj lost UTF-8 BOM")
	}
	if !bytes.Contains(rootAfter, []byte("<!-- Keep this host comment -->")) {
		t.Fatal("HostWeb.csproj lost comment")
	}
	if !bytes.Contains(rootAfter, []byte("Forge service isolation: services/first-worker")) {
		t.Fatalf("HostWeb.csproj missing isolation group:\n%s", rootAfter)
	}

	// Verify non-solution sibling also received its narrow exclusion.
	siblingProjPath := filepath.Join(repo, "non-solution", "Watcher.csproj")
	siblingAfter := string(mustReadBytes(t, siblingProjPath))
	if !strings.Contains(siblingAfter, `Label="Forge service isolation: services/first-worker"`) ||
		!strings.Contains(siblingAfter, `Remove="../services/first-worker/**"`) {
		t.Fatalf("non-solution sibling missing isolation group:\n%s", siblingAfter)
	}

	// Add a SECOND service and verify both remain registered and isolated.
	if err := Run(context.Background(), forge.Assets(), repo, Options{
		Stack:       "csharp-cli",
		ProjectName: "SecondWorker",
		Path:        "services/second-worker",
		AuthorName:  "CI Bot",
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}

	slnList := runCmd(t, repo, "dotnet", "sln", "Product.slnx", "list")
	for _, want := range []string{
		"HostWeb.csproj",
		"services/first-worker/FirstWorker.csproj",
		"services/first-worker/tests/FirstWorker.Tests/FirstWorker.Tests.csproj",
		"services/second-worker/SecondWorker.csproj",
		"services/second-worker/tests/SecondWorker.Tests/SecondWorker.Tests.csproj",
	} {
		if !strings.Contains(strings.ReplaceAll(slnList, "\\", "/"), want) {
			t.Fatalf("solution list missing %q:\n%s", want, slnList)
		}
	}

	rootAfterSecond := string(mustReadBytes(t, rootProjPath))
	if !strings.Contains(rootAfterSecond, "Forge service isolation: services/first-worker") ||
		!strings.Contains(rootAfterSecond, "Forge service isolation: services/second-worker") {
		t.Fatalf("HostWeb.csproj did not retain both service isolation groups:\n%s", rootAfterSecond)
	}
}

func TestRunCentralPackageManagementReuseAndAppendAndRejectConditional(t *testing.T) {
	t.Parallel()

	t.Run("reuses compatible version strips inline versions removes global ref and preserves comments", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})

		// Host uses CPM with a different-but-compatible xunit 2.9.0 and a GlobalPackageReference for StyleCop.Analyzers.
		dirPackagesPath := filepath.Join(repo, "Directory.Packages.props")
		dirPackagesContent := "<Project>\n  <!-- Host central packages comment -->\n  <PropertyGroup>\n    <ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally>\n  </PropertyGroup>\n  <ItemGroup>\n    <PackageVersion Include=\"xunit\" Version=\"2.9.0\" />\n    <GlobalPackageReference Include=\"StyleCop.Analyzers\" Version=\"1.3.180\" />\n  </ItemGroup>\n</Project>\n"
		mustWriteFile(t, dirPackagesPath, dirPackagesContent)

		if err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "CpmWorker",
			Path:        "services/cpm-worker",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run(CPM) error = %v", err)
		}

		updatedProps := string(mustReadBytes(t, dirPackagesPath))
		if !strings.Contains(updatedProps, "<!-- Host central packages comment -->") {
			t.Fatal("Directory.Packages.props lost host comment")
		}
		if !strings.Contains(updatedProps, `<PackageVersion Include="xunit" Version="2.9.0" />`) {
			t.Fatalf("Directory.Packages.props modified existing xunit version:\n%s", updatedProps)
		}
		if strings.Count(strings.ToLower(updatedProps), `include="xunit"`) != 1 {
			t.Fatalf("Directory.Packages.props duplicated xunit:\n%s", updatedProps)
		}
		if !strings.Contains(updatedProps, `<PackageVersion Include="System.CommandLine"`) {
			t.Fatalf("Directory.Packages.props missing appended System.CommandLine:\n%s", updatedProps)
		}

		// Verify generated .csproj has NO inline Version attributes, NO VersionOverride, and NO duplicate StyleCop.Analyzers.
		for _, proj := range []string{
			filepath.Join(repo, "services/cpm-worker/CpmWorker.csproj"),
			filepath.Join(repo, "services/cpm-worker/tests/CpmWorker.Tests/CpmWorker.Tests.csproj"),
		} {
			data := mustReadBytes(t, proj)
			if bytes.Contains(data, []byte("VersionOverride")) {
				t.Fatalf("%s must not use VersionOverride", proj)
			}
			assertNoInlinePackageVersionsAndNoPackage(t, proj, data, "StyleCop.Analyzers")
		}

		runCmd(t, repo, "dotnet", "test", "Product.slnx", "--no-build", "--configuration", "Debug")
	})

	t.Run("rejects conditional unresolvable central package and rolls back", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		dirPackagesPath := filepath.Join(repo, "Directory.Packages.props")
		origProps := "<Project>\n  <PropertyGroup>\n    <ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally>\n  </PropertyGroup>\n  <ItemGroup Condition=\"'$(Configuration)' == 'NonExistent'\">\n    <PackageVersion Include=\"xunit\" Version=\"2.9.3\" />\n  </ItemGroup>\n</Project>\n"
		mustWriteFile(t, dirPackagesPath, origProps)
		slnBefore := mustReadBytes(t, filepath.Join(repo, "Product.slnx"))

		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "CondWorker",
			Path:        "services/cond-worker",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "xunit") || !strings.Contains(strings.ToLower(err.Error()), "conditional") {
			t.Fatalf("Run(conditional CPM) error = %v, want conditional xunit error", err)
		}

		if string(mustReadBytes(t, dirPackagesPath)) != origProps {
			t.Fatal("Directory.Packages.props was not restored after conditional CPM failure")
		}
		if !bytes.Equal(mustReadBytes(t, filepath.Join(repo, "Product.slnx")), slnBefore) {
			t.Fatal("Product.slnx was not restored after conditional CPM failure")
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "services", "cond-worker")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("services/cond-worker should have been removed on rollback, stat err = %v", statErr)
		}
	})
}

func TestRunRejectsIncompatibleHostTargetFrameworkAndSDKAndRollsBack(t *testing.T) {
	t.Parallel()

	t.Run("inherited net8.0 TargetFramework rejected", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		mustWriteFile(t, filepath.Join(repo, "Directory.Build.props"),
			"<Project>\n  <PropertyGroup>\n    <TargetFramework>net8.0</TargetFramework>\n  </PropertyGroup>\n</Project>\n")
		slnBefore := mustReadBytes(t, filepath.Join(repo, "Product.slnx"))

		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "OldHostSvc",
			Path:        "services/old-host-svc",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "net8.0") || !strings.Contains(err.Error(), "net10.0") {
			t.Fatalf("Run(net8.0 host) error = %v, want diagnostic naming net8.0 and net10.0", err)
		}
		if !bytes.Equal(mustReadBytes(t, filepath.Join(repo, "Product.slnx")), slnBefore) {
			t.Fatal("Product.slnx mutated on TFM incompatibility")
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "services", "old-host-svc")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("services/old-host-svc should have been rolled back, err = %v", statErr)
		}
	})

	t.Run("inherited multi-target TargetFrameworks rejected", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		mustWriteFile(t, filepath.Join(repo, "Directory.Build.props"),
			"<Project>\n  <PropertyGroup>\n    <TargetFrameworks>net8.0;net10.0</TargetFrameworks>\n  </PropertyGroup>\n</Project>\n")

		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "MultiTfmSvc",
			Path:        "services/multi-tfm",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "TargetFrameworks") {
			t.Fatalf("Run(multi-TFM host) error = %v, want diagnostic naming TargetFrameworks", err)
		}
	})

	t.Run("host global.json pinning older SDK rejected", func(t *testing.T) {
		t.Parallel()
		requireDotnetSDKMajor(t, "8.")
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		mustWriteFile(t, filepath.Join(repo, "global.json"), `{"sdk":{"version":"8.0.100","rollForward":"latestFeature"}}`)

		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "OldSdkSvc",
			Path:        "services/old-sdk",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || (!strings.Contains(err.Error(), "SDK") && !strings.Contains(err.Error(), "global.json")) {
			t.Fatalf("Run(global.json 8.0) error = %v, want SDK incompatibility error", err)
		}
	})
}

func TestRunHostLockfileProtectionAndDriftRollback(t *testing.T) {
	t.Parallel()

	t.Run("missing host lockfile when RestorePackagesWithLockFile enabled stops before restore", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		mustWriteFile(t, filepath.Join(repo, "Directory.Build.props"),
			"<Project>\n  <PropertyGroup>\n    <RestorePackagesWithLockFile>true</RestorePackagesWithLockFile>\n  </PropertyGroup>\n</Project>\n")

		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "LockSvc",
			Path:        "services/lock-svc",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "restore that host project first") {
			t.Fatalf("Run(missing host lockfile) error = %v, want instruction to restore host project first", err)
		}
	})

	t.Run("custom NuGetLockFilePath drift detected and rolled back", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		// Configure a custom NuGetLockFilePath on the existing host project and generate its lockfile cleanly first.
		existingProj := filepath.Join(repo, "src", "ExistingApp", "ExistingApp.csproj")
		mustWriteFile(t, existingProj, `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
    <RestorePackagesWithLockFile>true</RestorePackagesWithLockFile>
    <NuGetLockFilePath>$(MSBuildProjectDirectory)/custom.lock.json</NuGetLockFilePath>
  </PropertyGroup>
</Project>
`)
		runCmd(t, repo, "dotnet", "restore", "Product.slnx")
		customLockPath := filepath.Join(repo, "src", "ExistingApp", "custom.lock.json")
		origLockBytes := mustReadBytes(t, customLockPath)

		// Inject a runner that simulates dependency drift rewriting custom.lock.json during dotnet restore.
		driftingRunner := &interceptRunner{
			inner: execCmdRunner{},
			fn: func(ctx context.Context, dir, name string, args ...string) (cmdResult, error, bool) {
				if name == "dotnet" && len(args) > 0 && args[0] == "restore" {
					res, err := (execCmdRunner{}).Run(ctx, dir, name, args...)
					if err == nil {
						_ = os.WriteFile(customLockPath, append(append([]byte(nil), origLockBytes...), []byte("\n// drifted\n")...), 0o644)
					}
					return res, err, true
				}
				return cmdResult{}, nil, false
			},
		}

		slnBefore := mustReadBytes(t, filepath.Join(repo, "Product.slnx"))
		err := runWithRunner(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "DriftSvc",
			Path:        "services/drift-svc",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{}, driftingRunner, nil)
		if err == nil || !strings.Contains(err.Error(), "dependency drift") {
			t.Fatalf("Run(lockfile drift) error = %v, want dependency drift error", err)
		}
		if !bytes.Equal(mustReadBytes(t, customLockPath), origLockBytes) {
			t.Fatal("host custom.lock.json bytes were not restored after dependency drift failure")
		}
		if !bytes.Equal(mustReadBytes(t, filepath.Join(repo, "Product.slnx")), slnBefore) {
			t.Fatal("Product.slnx was not restored after dependency drift failure")
		}
	})
}

func TestRunWebAPIEndpointTestPassesWithCustomOutputLayoutAndDisabledGenerateAssemblyInfo(t *testing.T) {
	t.Parallel()

	for _, genAsmInfo := range []bool{true, false} {
		genAsmInfo := genAsmInfo
		name := fmt.Sprintf("GenerateAssemblyInfo=%v", genAsmInfo)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
			mustWriteFile(t, filepath.Join(repo, "Directory.Build.props"), fmt.Sprintf(`<Project>
  <PropertyGroup>
    <UseArtifactsOutput>true</UseArtifactsOutput>
    <ArtifactsPath>$(MSBuildThisFileDirectory)artifacts</ArtifactsPath>
    <GenerateAssemblyInfo>%v</GenerateAssemblyInfo>
  </PropertyGroup>
</Project>
`, genAsmInfo))

			if err := Run(context.Background(), forge.Assets(), repo, Options{
				Stack:       "csharp-webapi",
				ProjectName: "OrdersApi",
				Path:        "services/orders-api",
				AuthorName:  "CI Bot",
			}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run(csharp-webapi, %s) error = %v", name, err)
			}

			// Both Debug and Release tests must run and pass against the matching built app!
			runCmd(t, repo, "dotnet", "test", "Product.slnx", "--no-build", "--configuration", "Debug")
			runCmd(t, repo, "dotnet", "test", "Product.slnx", "--no-build", "--configuration", "Release")
		})
	}
}

func TestRunBlazorServiceExcludesClientAndPassesTests(t *testing.T) {
	t.Parallel()

	repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
	if err := Run(context.Background(), forge.Assets(), repo, Options{
		Stack:       "csharp-blazor",
		ProjectName: "PortalWeb",
		Path:        "services/portal-web",
		AuthorName:  "CI Bot",
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(csharp-blazor) error = %v", err)
	}

	slnList := runCmd(t, repo, "dotnet", "sln", "Product.slnx", "list")
	for _, want := range []string{
		"services/portal-web/PortalWeb.csproj",
		"services/portal-web/PortalWeb.Client/PortalWeb.Client.csproj",
		"services/portal-web/tests/PortalWeb.Tests/PortalWeb.Tests.csproj",
	} {
		if !strings.Contains(strings.ReplaceAll(slnList, "\\", "/"), want) {
			t.Fatalf("solution list missing %q:\n%s", want, slnList)
		}
	}

	runCmd(t, repo, "dotnet", "test", "Product.slnx", "--no-build", "--configuration", "Debug")
	runCmd(t, repo, "dotnet", "test", "Product.slnx", "--no-build", "--configuration", "Release")
}

func TestRunBoundedRollbackOnInjectedFailuresAndConcurrentEdits(t *testing.T) {
	t.Parallel()

	t.Run("failing dotnet sln add rolls back cleanly", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		slnBefore := mustReadBytes(t, filepath.Join(repo, "Product.slnx"))

		failingSlnAdd := &interceptRunner{
			inner: execCmdRunner{},
			fn: func(ctx context.Context, dir, name string, args ...string) (cmdResult, error, bool) {
				if name == "dotnet" && len(args) >= 3 && args[0] == "sln" && args[2] == "add" {
					return cmdResult{Stderr: "simulated sln add failure"}, errors.New("exit status 1"), true
				}
				return cmdResult{}, nil, false
			},
		}

		err := runWithRunner(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "RollbackSvc",
			Path:        "services/rollback-svc",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{}, failingSlnAdd, nil)
		if err == nil || !strings.Contains(err.Error(), "simulated sln add failure") {
			t.Fatalf("error = %v, want simulated sln add failure", err)
		}
		if !bytes.Equal(mustReadBytes(t, filepath.Join(repo, "Product.slnx")), slnBefore) {
			t.Fatal("Product.slnx changed after failed sln add")
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "services", "rollback-svc")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("services/rollback-svc should be completely removed after sln add failure, err = %v", statErr)
		}
	})

	t.Run("concurrently modified solution file is preserved and not overwritten", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		slnPath := filepath.Join(repo, "Product.slnx")
		concurrentMarker := "<!-- concurrent user edit -->\n"

		hooks := &testHooks{
			beforeSolutionWrite: func(abs string) error {
				orig := mustReadBytes(t, abs)
				return os.WriteFile(abs, append(orig, []byte(concurrentMarker)...), 0o644)
			},
		}

		err := runWithRunner(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "RaceSvc",
			Path:        "services/race-svc",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{}, execCmdRunner{}, hooks)
		if err == nil || !strings.Contains(err.Error(), "changed on disk") {
			t.Fatalf("error = %v, want concurrent edit error", err)
		}
		gotSln := string(mustReadBytes(t, slnPath))
		if !strings.Contains(gotSln, concurrentMarker) {
			t.Fatalf("concurrent user edit in Product.slnx was lost:\n%s", gotSln)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "services", "race-svc")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("services/race-svc should be rolled back after concurrent solution edit, err = %v", statErr)
		}
	})

	t.Run("failing dotnet build preserves externally added service file and reports retained paths", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		slnBefore := mustReadBytes(t, filepath.Join(repo, "Product.slnx"))
		userNoteRel := "services/build-fail/USER_NOTE.txt"
		userNoteContent := "do not delete me"

		hooks := &testHooks{
			afterMaterialize: func(serviceDirAbs string) error {
				return os.WriteFile(filepath.Join(serviceDirAbs, "USER_NOTE.txt"), []byte(userNoteContent), 0o644)
			},
		}
		failingBuild := &interceptRunner{
			inner: execCmdRunner{},
			fn: func(ctx context.Context, dir, name string, args ...string) (cmdResult, error, bool) {
				if name == "dotnet" && len(args) > 0 && args[0] == "build" {
					return cmdResult{Stderr: "simulated CS0001 compile failure"}, errors.New("exit status 1"), true
				}
				return cmdResult{}, nil, false
			},
		}

		err := runWithRunner(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "BuildFailSvc",
			Path:        "services/build-fail",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{}, failingBuild, hooks)
		if err == nil || !strings.Contains(err.Error(), "simulated CS0001 compile failure") {
			t.Fatalf("error = %v, want simulated compile failure", err)
		}
		if !strings.Contains(err.Error(), "retained paths") || !strings.Contains(err.Error(), "services/build-fail") {
			t.Fatalf("error should report retained destination path:\n%v", err)
		}
		// Solution must be restored.
		if !bytes.Equal(mustReadBytes(t, filepath.Join(repo, "Product.slnx")), slnBefore) {
			t.Fatal("Product.slnx was not restored after build failure")
		}
		// Unrelated user file inside destination MUST be preserved!
		if got := string(mustReadBytes(t, filepath.Join(repo, filepath.FromSlash(userNoteRel)))); got != userNoteContent {
			t.Fatalf("unrelated user file %s was lost or corrupted: %q", userNoteRel, got)
		}
		// Generated unchanged service files MUST have been removed.
		if _, statErr := os.Lstat(filepath.Join(repo, "services/build-fail/BuildFailSvc.csproj")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unchanged generated BuildFailSvc.csproj should have been removed during rollback")
		}
	})
}

func TestRunDetectsCollisionsAndLateImportDefeat(t *testing.T) {
	t.Parallel()

	t.Run("assembly and project name collision rejected", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "ExistingApp",
			Path:        "services/existing-app",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "collision") {
			t.Fatalf("Run(name collision) error = %v, want collision error", err)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "services", "existing-app")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("services/existing-app should be rolled back after collision, err = %v", statErr)
		}
	})

	t.Run("shared intermediate directory collision rejected", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		mustWriteFile(t, filepath.Join(repo, "Directory.Build.props"), `<Project>
  <PropertyGroup>
    <BaseIntermediateOutputPath>$(MSBuildThisFileDirectory)shared-obj/</BaseIntermediateOutputPath>
    <IntermediateOutputPath>$(MSBuildThisFileDirectory)shared-obj/$(Configuration)/</IntermediateOutputPath>
  </PropertyGroup>
</Project>
`)
		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "SharedObjSvc",
			Path:        "services/shared-obj-svc",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "shared intermediate directory collision") {
			t.Fatalf("Run(shared intermediate dir) error = %v, want shared intermediate directory collision error", err)
		}
	})

	t.Run("late Directory.Build.targets re-including service files fails with defining target named", func(t *testing.T) {
		t.Parallel()
		repo := newTestHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		mustWriteFile(t, filepath.Join(repo, "Directory.Build.targets"), `<Project>
  <ItemGroup Condition="'$(MSBuildProjectName)' == 'ExistingApp'">
    <Compile Include="$(MSBuildThisFileDirectory)services/**/*.cs" />
  </ItemGroup>
</Project>
`)
		err := Run(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "LateLeakSvc",
			Path:        "services/late-leak",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "Directory.Build.targets") {
			t.Fatalf("Run(late import leak) error = %v, want error naming Directory.Build.targets", err)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "services", "late-leak")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("services/late-leak should be rolled back after late import leak, err = %v", statErr)
		}
	})
}

type interceptRunner struct {
	inner cmdRunner
	fn    func(ctx context.Context, dir, name string, args ...string) (cmdResult, error, bool)
}

func (ir *interceptRunner) Run(ctx context.Context, dir, name string, args ...string) (cmdResult, error) {
	if ir.fn != nil {
		if res, err, handled := ir.fn(ctx, dir, name, args...); handled {
			return res, err
		}
	}
	return ir.inner.Run(ctx, dir, name, args...)
}

type hostRepoSpec struct {
	solutionName      string
	rootLevelWebHost  bool
	nonSolutionGlobTo string
	skipGitAuthor     bool
}

func requireDotnet10(t *testing.T) {
	t.Helper()
	requireDotnetSDKMajor(t, "10.")
}

func requireDotnetSDKMajor(t *testing.T, majorPrefix string) {
	t.Helper()
	dotnetPath, err := exec.LookPath("dotnet")
	if err != nil {
		t.Skipf("dotnet not found on PATH; skipping real-SDK test requiring %sx", majorPrefix)
	}
	out, err := exec.Command(dotnetPath, "--list-sdks").CombinedOutput()
	if err != nil {
		t.Skipf("dotnet --list-sdks failed (%v); skipping real-SDK test", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), majorPrefix) {
			return
		}
	}
	t.Skipf("no .NET %sx SDK installed (installed:\n%s); skipping real-SDK test", majorPrefix, strings.TrimSpace(string(out)))
}

// newStaticHostRepo creates a Git-backed C# monorepo fixture without invoking
// the dotnet CLI, so input validation and fake-runner unit tests run on CI
// runners without a .NET SDK installed.
func newStaticHostRepo(t *testing.T, spec hostRepoSpec) string {
	t.Helper()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	if !spec.skipGitAuthor {
		runGit(t, dir, "config", "user.name", "Host Maintainer")
		runGit(t, dir, "config", "user.email", "host@example.com")
	}

	slnName := spec.solutionName
	if slnName == "" {
		slnName = "Product.slnx"
	}
	mustWriteFile(t, filepath.Join(dir, "src", "ExistingApp", "ExistingApp.csproj"), `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net10.0</TargetFramework>
  </PropertyGroup>
</Project>
`)
	mustWriteFile(t, filepath.Join(dir, "src", "ExistingApp", "Program.cs"), "Console.WriteLine(\"existing\");\n")
	mustWriteFile(t, filepath.Join(dir, slnName), "<Solution>\n  <Project Path=\"src/ExistingApp/ExistingApp.csproj\" />\n</Solution>\n")
	return dir
}

func newTestHostRepo(t *testing.T, spec hostRepoSpec) string {
	t.Helper()
	requireDotnet10(t)

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	if !spec.skipGitAuthor {
		runGit(t, dir, "config", "user.name", "Host Maintainer")
		runGit(t, dir, "config", "user.email", "host@example.com")
	}

	slnName := spec.solutionName
	if slnName == "" {
		slnName = "Product.slnx"
	}
	format := "slnx"
	if strings.HasSuffix(slnName, ".sln") {
		format = "sln"
	}
	baseName := strings.TrimSuffix(slnName, filepath.Ext(slnName))
	runCmd(t, dir, "dotnet", "new", "sln", "--name", baseName, "--format", format)

	if spec.rootLevelWebHost {
		mustWriteFile(t, filepath.Join(dir, "HostWeb.csproj"), `<Project Sdk="Microsoft.NET.Sdk.Web">
  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
    <Nullable>enable</Nullable>
    <ImplicitUsings>enable</ImplicitUsings>
  </PropertyGroup>
</Project>
`)
		mustWriteFile(t, filepath.Join(dir, "Program.cs"), "var builder = WebApplication.CreateBuilder(args);\nvar app = builder.Build();\napp.MapGet(\"/\", () => \"host\");\napp.Run();\n")
		runCmd(t, dir, "dotnet", "sln", slnName, "add", "HostWeb.csproj")
	} else {
		mustWriteFile(t, filepath.Join(dir, "src", "ExistingApp", "ExistingApp.csproj"), `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net10.0</TargetFramework>
    <Nullable>enable</Nullable>
    <ImplicitUsings>enable</ImplicitUsings>
  </PropertyGroup>
</Project>
`)
		mustWriteFile(t, filepath.Join(dir, "src", "ExistingApp", "Program.cs"), "Console.WriteLine(\"existing\");\n")
		runCmd(t, dir, "dotnet", "sln", slnName, "add", "src/ExistingApp/ExistingApp.csproj")
	}

	if spec.nonSolutionGlobTo != "" {
		mustWriteFile(t, filepath.Join(dir, "non-solution", "Watcher.csproj"), fmt.Sprintf(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
  </PropertyGroup>
  <ItemGroup>
    <Compile Include="../%s/**/*.cs" />
  </ItemGroup>
</Project>
`, spec.nonSolutionGlobTo))
		mustWriteFile(t, filepath.Join(dir, "non-solution", "Marker.cs"), "namespace Watcher;\npublic sealed class Marker {}\n")
	}

	return dir
}

func assertNoInlinePackageVersionsAndNoPackage(t *testing.T, projPath string, data []byte, forbiddenPkg string) {
	t.Helper()

	type pkgRef struct {
		Include string `xml:"Include,attr"`
		Version string `xml:"Version,attr"`
	}
	type ig struct {
		PackageReferences []pkgRef `xml:"PackageReference"`
	}
	type doc struct {
		ItemGroups []ig `xml:"ItemGroup"`
	}
	var parsed doc
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse %s: %v", projPath, err)
	}
	for _, g := range parsed.ItemGroups {
		for _, r := range g.PackageReferences {
			if r.Version != "" {
				t.Fatalf("%s retained inline Version=%q on PackageReference %q under CPM", projPath, r.Version, r.Include)
			}
			if strings.EqualFold(r.Include, forbiddenPkg) {
				t.Fatalf("%s duplicated host-provided package %q", projPath, forbiddenPkg)
			}
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runCmd(t, dir, "git", args...)
}

func runCmd(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func mustReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return data
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(%s): %v", path, err)
	}
}
