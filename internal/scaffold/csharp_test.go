package scaffold_test

import (
	"encoding/xml"
	"path"
	"strings"
	"testing"

	"forge"
	"forge/internal/project"
	"forge/internal/scaffold"
)

func TestRenderCSharpServiceProducesFragmentForEachStack(t *testing.T) {
	t.Parallel()

	cases := []struct {
		stack         string
		projectName   string
		wantNamespace string
		wantProjects  []string
		wantOverlay   string
	}{
		{
			stack:         "csharp-cli",
			projectName:   "BillingWorker",
			wantNamespace: "BillingWorker",
			wantProjects: []string{
				"BillingWorker.csproj",
				"tests/BillingWorker.Tests/BillingWorker.Tests.csproj",
			},
			wantOverlay: "Telemetry.cs",
		},
		{
			stack:         "csharp-webapi",
			projectName:   "OrdersApi",
			wantNamespace: "OrdersApi",
			wantProjects: []string{
				"OrdersApi.csproj",
				"tests/OrdersApi.Tests/OrdersApi.Tests.csproj",
			},
			wantOverlay: "Controllers/HealthController.cs",
		},
		{
			stack:         "csharp-blazor",
			projectName:   "PortalWeb",
			wantNamespace: "PortalWeb",
			wantProjects: []string{
				"PortalWeb.csproj",
				"PortalWeb.Client/PortalWeb.Client.csproj",
				"tests/PortalWeb.Tests/PortalWeb.Tests.csproj",
			},
			wantOverlay: "HealthReporter.cs",
		},
	}

	forbiddenFiles := []string{
		"AGENTS.md",
		"CLAUDE.md",
		"README.md",
		"CONTEXT.md",
		".gitignore",
		"mise.toml",
		"lefthook.yml",
		".github/workflows/ci.yml",
		".forge/manifest.json",
		".forge-infra-version",
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.stack, func(t *testing.T) {
			t.Parallel()

			vars, err := project.ResolveCSharpServiceVariables(tc.projectName, tc.stack, "CI Bot")
			if err != nil {
				t.Fatalf("ResolveCSharpServiceVariables() error = %v", err)
			}

			files, err := scaffold.RenderCSharpService(forge.Assets(), vars)
			if err != nil {
				t.Fatalf("RenderCSharpService() error = %v", err)
			}

			byPath := make(map[string]scaffold.File, len(files))
			for _, f := range files {
				if _, dup := byPath[f.Path]; dup {
					t.Fatalf("duplicate rendered path %q", f.Path)
				}
				byPath[f.Path] = f
			}

			for _, forbidden := range forbiddenFiles {
				if _, ok := byPath[forbidden]; ok {
					t.Fatalf("service fragment unexpectedly emitted repository file %q", forbidden)
				}
			}
			for p := range byPath {
				if strings.HasPrefix(p, ".claude/") || strings.HasPrefix(p, ".codex/") ||
					strings.HasPrefix(p, ".opencode/") || strings.HasPrefix(p, ".github/") ||
					strings.HasPrefix(p, ".config/mise/") || strings.HasPrefix(p, ".otel/") ||
					strings.HasPrefix(p, ".beads/") || strings.HasPrefix(p, ".forge/") {
					t.Fatalf("service fragment unexpectedly emitted repo infrastructure path %q", p)
				}
				if p == "Project.csproj" || strings.HasPrefix(p, "tests/Project.Tests/") || strings.HasPrefix(p, "Project.Client/") {
					t.Fatalf("unrenamed template path %q leaked into service output", p)
				}
			}

			if _, ok := byPath[tc.wantOverlay]; !ok {
				t.Fatalf("overlay file %q missing from rendered fragment", tc.wantOverlay)
			}

			for _, projPath := range tc.wantProjects {
				f, ok := byPath[projPath]
				if !ok {
					t.Fatalf("expected project %q not rendered", projPath)
				}
				assertValidServiceProjectXML(t, projPath, f.Data, byPath)
			}

			// Verify Web API test project carries metadata target and endpoint test uses metadata.
			if tc.stack == "csharp-webapi" {
				testProj := string(byPath["tests/OrdersApi.Tests/OrdersApi.Tests.csproj"].Data)
				if !strings.Contains(testProj, "ForgeGenerateTestMetadata") || !strings.Contains(testProj, "ForgeAppProjectPath") {
					t.Fatalf("webapi test project missing metadata generation targets:\n%s", testProj)
				}
				endpointTest := string(byPath["tests/OrdersApi.Tests/WeatherForecastEndpointTests.cs"].Data)
				if !strings.Contains(endpointTest, "ForgeAppProjectPath") || !strings.Contains(endpointTest, "ForgeBuildConfiguration") {
					t.Fatalf("webapi endpoint test does not read generated assembly metadata:\n%s", endpointTest)
				}
				if strings.Contains(endpointTest, "AppContext.BaseDirectory") {
					t.Fatalf("webapi endpoint test still derives root from AppContext.BaseDirectory")
				}
			}

			// Verify Blazor server excludes renamed client and references renamed client project.
			if tc.stack == "csharp-blazor" {
				serverProj := string(byPath["PortalWeb.csproj"].Data)
				if !strings.Contains(serverProj, `Remove="PortalWeb.Client/**"`) {
					t.Fatalf("blazor server project missing renamed client exclusion:\n%s", serverProj)
				}
				if !strings.Contains(serverProj, `Include="PortalWeb.Client/PortalWeb.Client.csproj"`) {
					t.Fatalf("blazor server project missing renamed client ProjectReference:\n%s", serverProj)
				}
			}
		})
	}
}

func assertValidServiceProjectXML(t *testing.T, projPath string, data []byte, allFiles map[string]scaffold.File) {
	t.Helper()

	type prop struct {
		XMLName   xml.Name
		Condition string `xml:"Condition,attr"`
		Value     string `xml:",chardata"`
	}
	type projRef struct {
		Include string `xml:"Include,attr"`
	}
	type propGroup struct {
		Props []prop `xml:",any"`
	}
	type itemGroup struct {
		ProjectReferences []projRef `xml:"ProjectReference"`
	}
	type projectXML struct {
		PropertyGroups []propGroup `xml:"PropertyGroup"`
		ItemGroups     []itemGroup `xml:"ItemGroup"`
	}

	var parsed projectXML
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("%s is not valid XML: %v\n%s", projPath, err, data)
	}

	seenTFM := false
	for _, pg := range parsed.PropertyGroups {
		for _, p := range pg.Props {
			switch p.XMLName.Local {
			case "NoWarn":
				t.Fatalf("%s should not emit template NoWarn suppression in service mode", projPath)
			case "TargetFramework":
				seenTFM = true
				if !strings.Contains(p.Condition, "TargetFramework") || !strings.Contains(p.Condition, "TargetFrameworks") {
					t.Fatalf("%s TargetFramework condition = %q, want check on TargetFramework and TargetFrameworks", projPath, p.Condition)
				}
			case "Nullable", "ImplicitUsings", "GenerateDocumentationFile":
				if p.Condition == "" {
					t.Fatalf("%s %s should be conditioned on inherited host property", projPath, p.XMLName.Local)
				}
			}
		}
	}
	if !seenTFM {
		t.Fatalf("%s missing fallback TargetFramework", projPath)
	}

	for _, ig := range parsed.ItemGroups {
		for _, ref := range ig.ProjectReferences {
			resolved := path.Clean(path.Join(path.Dir(projPath), ref.Include))
			if _, ok := allFiles[resolved]; !ok {
				t.Fatalf("%s ProjectReference %q resolves to missing file %q", projPath, ref.Include, resolved)
			}
		}
	}
}

func TestRenderCSharpServiceRejectsUnsupportedStack(t *testing.T) {
	t.Parallel()

	vars := project.Variables{
		ProjectName:     "Sample",
		Language:        "go",
		Stack:           "go-api-chi",
		AuthorName:      "CI Bot",
		CSharpNamespace: "Sample",
		RepoSlug:        "sample",
	}
	if _, err := scaffold.RenderCSharpService(forge.Assets(), vars); err == nil {
		t.Fatal("RenderCSharpService(go-api-chi) = nil, want error")
	}
}
