package addcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge"
)

func TestXMLHelpersPreserveBOMCRLFAndComments(t *testing.T) {
	t.Parallel()

	origProj := "\xEF\xBB\xBF<Project Sdk=\"Microsoft.NET.Sdk.Web\">\r\n  <!-- keep comment -->\r\n  <PropertyGroup>\r\n    <TargetFramework>net10.0</TargetFramework>\r\n  </PropertyGroup>\r\n</Project>\r\n"
	isolated, err := insertServiceIsolationGroup([]byte(origProj), "services/orders-api", "services/orders-api")
	if err != nil {
		t.Fatalf("insertServiceIsolationGroup() error = %v", err)
	}
	if !bytes.HasPrefix(isolated, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("lost UTF-8 BOM")
	}
	if !bytes.Contains(isolated, []byte("<!-- keep comment -->")) {
		t.Fatal("lost XML comment")
	}
	if !bytes.Contains(isolated, []byte("\r\n  <ItemGroup Label=\"Forge service isolation: services/orders-api\">\r\n")) {
		t.Fatalf("did not preserve CRLF or isolation label:\n%q", string(isolated))
	}

	origProps := "\xEF\xBB\xBF<Project>\r\n  <!-- keep central comment -->\r\n  <PropertyGroup>\r\n    <ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally>\r\n  </PropertyGroup>\r\n  <ItemGroup>\r\n    <PackageVersion Include=\"xunit\" Version=\"2.9.0\" />\r\n  </ItemGroup>\r\n</Project>\r\n"
	updatedProps, err := appendCentralPackageVersions([]byte(origProps), []packagePin{
		{ID: "System.CommandLine", Version: "3.0.0"},
		{ID: "NSubstitute", Version: "6.2.0"},
	})
	if err != nil {
		t.Fatalf("appendCentralPackageVersions() error = %v", err)
	}
	if !bytes.HasPrefix(updatedProps, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("Directory.Packages.props lost BOM")
	}
	if !bytes.Contains(updatedProps, []byte("<!-- keep central comment -->")) {
		t.Fatal("Directory.Packages.props lost comment")
	}
	if !bytes.Contains(updatedProps, []byte(`<PackageVersion Include="xunit" Version="2.9.0" />`)) {
		t.Fatal("Directory.Packages.props modified existing entry")
	}
	idxNSub := bytes.Index(updatedProps, []byte(`Include="NSubstitute"`))
	idxSys := bytes.Index(updatedProps, []byte(`Include="System.CommandLine"`))
	if idxNSub < 0 || idxSys < 0 || idxNSub > idxSys {
		t.Fatalf("appended packages missing or unsorted:\n%s", updatedProps)
	}
}

func TestRewriteServiceProjectPackagesStripsVersionsAndDeduplicatesHostRefs(t *testing.T) {
	t.Parallel()

	sample := []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="StyleCop.Analyzers" Version="1.3.180">
      <PrivateAssets>all</PrivateAssets>
    </PackageReference>
    <PackageReference Include="xunit" Version="2.9.3" />
    <PackageReference Include="coverlet.collector" Version="10.1.1">
      <PrivateAssets>all</PrivateAssets>
    </PackageReference>
  </ItemGroup>
</Project>
`)

	rewritten, changed, err := rewriteServiceProjectPackages(sample, true, map[string]bool{"stylecop.analyzers": true})
	if err != nil {
		t.Fatalf("rewriteServiceProjectPackages() error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed = true")
	}
	out := string(rewritten)
	if strings.Contains(out, "StyleCop.Analyzers") {
		t.Fatalf("host-provided StyleCop.Analyzers was not removed:\n%s", out)
	}
	if strings.Contains(out, "Version=") {
		t.Fatalf("inline Version attribute remained under CPM:\n%s", out)
	}
	if !strings.Contains(out, "<PrivateAssets>all</PrivateAssets>") {
		t.Fatalf("PrivateAssets metadata was lost:\n%s", out)
	}
}

func TestCheckHostDotnetSDKWithFakeRunner(t *testing.T) {
	t.Parallel()

	okRunner := &interceptRunner{
		fn: func(ctx context.Context, dir, name string, args ...string) (cmdResult, error, bool) {
			return cmdResult{Stdout: "10.0.403\n"}, nil, true
		},
	}
	ver, err := checkHostDotnetSDK(context.Background(), okRunner, ".")
	if err != nil || ver != "10.0.403" {
		t.Fatalf("checkHostDotnetSDK(10.0.403) = (%q, %v), want (10.0.403, nil)", ver, err)
	}

	oldRunner := &interceptRunner{
		fn: func(ctx context.Context, dir, name string, args ...string) (cmdResult, error, bool) {
			return cmdResult{Stdout: "8.0.427\n"}, nil, true
		},
	}
	if _, err := checkHostDotnetSDK(context.Background(), oldRunner, "."); err == nil || !strings.Contains(err.Error(), "8.0.427") {
		t.Fatalf("checkHostDotnetSDK(8.0.427) error = %v, want SDK version incompatibility", err)
	}
}

func TestRunWithFakeDotnetRunnerExercisesRollbackAndConcurrentEditsWithoutSDK(t *testing.T) {
	t.Parallel()

	newFakeDotnetRunner := func(failBuild bool) cmdRunner {
		return &interceptRunner{
			inner: execCmdRunner{}, // only used for git
			fn: func(ctx context.Context, dir, name string, args ...string) (cmdResult, error, bool) {
				if name != "dotnet" {
					return cmdResult{}, nil, false
				}
				switch {
				case len(args) == 1 && args[0] == "--version":
					return cmdResult{Stdout: "10.0.403\n"}, nil, true
				case len(args) >= 3 && args[0] == "sln" && args[2] == "list":
					return cmdResult{Stdout: "Project(s)\n----------\nsrc/ExistingApp/ExistingApp.csproj\n"}, nil, true
				case len(args) >= 3 && args[0] == "sln" && args[2] == "add":
					tmpSln := filepath.Join(dir, filepath.FromSlash(args[1]))
					orig, err := os.ReadFile(tmpSln)
					if err != nil {
						return cmdResult{}, err, true
					}
					updated := strings.Replace(string(orig), "</Solution>", "  <Project Path=\""+args[3]+"\" />\n</Solution>", 1)
					if err := os.WriteFile(tmpSln, []byte(updated), 0o644); err != nil {
						return cmdResult{}, err, true
					}
					return cmdResult{}, nil, true
				case len(args) >= 2 && args[0] == "msbuild":
					projAbs := args[1]
					projStem := strings.TrimSuffix(filepath.Base(projAbs), filepath.Ext(projAbs))
					cfg := "Debug"
					for _, a := range args {
						if strings.HasPrefix(a, "-p:Configuration=") {
							cfg = strings.TrimPrefix(a, "-p:Configuration=")
						}
					}
					resp := msbuildEvalResponse{
						Properties: map[string]string{
							"TargetFramework":        "net10.0",
							"AssemblyName":           projStem,
							"IntermediateOutputPath": filepath.Join("obj", cfg, "net10.0") + "/",
							"OutputPath":             filepath.Join("bin", cfg, "net10.0") + "/",
						},
						Items: map[string][]msbuildItem{},
					}
					b, _ := json.Marshal(resp)
					return cmdResult{Stdout: string(b)}, nil, true
				case len(args) >= 1 && args[0] == "restore":
					return cmdResult{}, nil, true
				case len(args) >= 1 && args[0] == "build":
					if failBuild {
						return cmdResult{Stderr: "simulated compiler failure"}, errors.New("exit status 1"), true
					}
					return cmdResult{}, nil, true
				default:
					return cmdResult{}, errors.New("unexpected dotnet call"), true
				}
			},
		}
	}

	t.Run("happy path without real dotnet", func(t *testing.T) {
		t.Parallel()
		repo := newStaticHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		var out bytes.Buffer
		err := runWithRunner(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "FakeRunnerSvc",
			Path:        "services/fake-runner",
			AuthorName:  "CI Bot",
		}, &out, newFakeDotnetRunner(false), nil)
		if err != nil {
			t.Fatalf("runWithRunner(happy) error = %v", err)
		}
		if !strings.Contains(string(mustReadBytes(t, filepath.Join(repo, "Product.slnx"))), "services/fake-runner/FakeRunnerSvc.csproj") {
			t.Fatal("Product.slnx did not receive generated project entry")
		}
		if _, err := os.Stat(filepath.Join(repo, "services/fake-runner/AGENTS.md")); err != nil {
			t.Fatalf("service AGENTS.md missing: %v", err)
		}
	})

	t.Run("build failure rollback preserves user file and reports retained path without real dotnet", func(t *testing.T) {
		t.Parallel()
		repo := newStaticHostRepo(t, hostRepoSpec{solutionName: "Product.slnx"})
		slnBefore := mustReadBytes(t, filepath.Join(repo, "Product.slnx"))

		hooks := &testHooks{
			afterMaterialize: func(serviceDirAbs string) error {
				return os.WriteFile(filepath.Join(serviceDirAbs, "KEEP.txt"), []byte("user data"), 0o644)
			},
		}
		err := runWithRunner(context.Background(), forge.Assets(), repo, Options{
			Stack:       "csharp-cli",
			ProjectName: "FailBuildSvc",
			Path:        "services/fail-build",
			AuthorName:  "CI Bot",
		}, &bytes.Buffer{}, newFakeDotnetRunner(true), hooks)
		if err == nil || !strings.Contains(err.Error(), "simulated compiler failure") || !strings.Contains(err.Error(), "services/fail-build") {
			t.Fatalf("error = %v, want compiler failure + retained path", err)
		}
		if !bytes.Equal(mustReadBytes(t, filepath.Join(repo, "Product.slnx")), slnBefore) {
			t.Fatal("Product.slnx was not restored")
		}
		if got := string(mustReadBytes(t, filepath.Join(repo, "services/fail-build/KEEP.txt"))); got != "user data" {
			t.Fatalf("KEEP.txt = %q, want user data", got)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "services/fail-build/FailBuildSvc.csproj")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("unchanged generated csproj should have been removed")
		}
	})
}
