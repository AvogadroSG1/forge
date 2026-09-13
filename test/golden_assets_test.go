package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcesYAMLDefinesTheV1GoldenRecipes(t *testing.T) {
	repoRoot := repoRoot(t)
	sourcesPath := filepath.Join(repoRoot, "sources.yaml")

	contentBytes, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcesPath, err)
	}

	content := string(contentBytes)
	requiredSnippets := []string{
		"gitignore_repo:",
		"go-cli-cobra:\n  kind: scaffolder",
		"go-api-chi:\n  kind: recipe",
		"python-cli-typer:\n  kind: scaffolder",
		"python-fastapi:\n  kind: scaffolder",
		"csharp-cli:\n  kind: scaffolder",
		"csharp-webapi:\n  kind: scaffolder",
		"cobra-cli init --pkg-name {{.ModulePath}}",
		"github.com/golang-standards/project-layout",
		"uv init",
		"dotnet new console",
		"dotnet new webapi",
		"go-web-templ:\n  kind: scaffolder",
		"python-web-jinja:\n  kind: scaffolder",
		"csharp-blazor:\n  kind: scaffolder",
		"vite-ts:\n  kind: scaffolder",
		"sveltekit:\n  kind: scaffolder",
		"angular:\n  kind: scaffolder",
		"github.com/a-h/templ",
		"uv add fastapi jinja2",
		"dotnet new blazor --interactivity Auto",
		"npm create vite",
		"sv@0.16.2 create . --template minimal",
		"@angular/cli",
		"- type: strip_paths",
		"gitignore: Node",
	}

	for _, snippet := range requiredSnippets {
		if !strings.Contains(content, snippet) {
			t.Fatalf("sources.yaml missing required snippet %q", snippet)
		}
	}
}

func TestGoldenCatalogPackagesVanillaAndOverlayAssetsForEveryV1Stack(t *testing.T) {
	repoRoot := repoRoot(t)

	tests := []struct {
		name  string
		files []string
	}{
		{
			name: "go-cli-cobra",
			files: []string{
				"templates/golden/go-cli-cobra/go.mod.tmpl",
				"templates/golden/go-cli-cobra/main.go.tmpl",
				"templates/golden/go-cli-cobra/cmd/root.go.tmpl",
				"templates/golden/go-cli-cobra/cmd/serve.go.tmpl",
				"templates/golden/go-cli-cobra/cmd/config.go.tmpl",
				"templates/golden/go-cli-cobra/.forge-overlay/cmd/root_test.go.tmpl",
				"templates/golden/go-cli-cobra/.forge-overlay/cmd/telemetry.go.tmpl",
				"templates/golden/go-cli-cobra/.forge-overlay/cmd/telemetry_test.go.tmpl",
			},
		},
		{
			name: "go-api-chi",
			files: []string{
				"templates/golden/go-api-chi/go.mod.tmpl",
				"templates/golden/go-api-chi/api/.keep",
				"templates/golden/go-api-chi/configs/.keep",
				"templates/golden/go-api-chi/internal/platform/.keep",
				"templates/golden/go-api-chi/pkg/.keep",
				"templates/golden/go-api-chi/.forge-overlay/cmd/api/main.go.tmpl",
				"templates/golden/go-api-chi/.forge-overlay/internal/httpapi/router.go.tmpl",
				"templates/golden/go-api-chi/.forge-overlay/internal/httpapi/health.go.tmpl",
				"templates/golden/go-api-chi/.forge-overlay/internal/httpapi/health_test.go.tmpl",
				"templates/golden/go-api-chi/.forge-overlay/internal/telemetry/telemetry.go.tmpl",
				"templates/golden/go-api-chi/.forge-overlay/internal/telemetry/telemetry_test.go.tmpl",
			},
		},
		{
			name: "python-cli-typer",
			files: []string{
				"templates/golden/python-cli-typer/pyproject.toml.tmpl",
				"templates/golden/python-cli-typer/src/{{.PythonPackage}}/__init__.py",
				"templates/golden/python-cli-typer/src/{{.PythonPackage}}/main.py",
				"templates/golden/python-cli-typer/.forge-overlay/tests/test_cli.py.tmpl",
				"templates/golden/python-cli-typer/.forge-overlay/src/{{.PythonPackage}}/telemetry.py.tmpl",
				"templates/golden/python-cli-typer/.forge-overlay/tests/test_telemetry.py.tmpl",
			},
		},
		{
			name: "python-fastapi",
			files: []string{
				"templates/golden/python-fastapi/pyproject.toml.tmpl",
				"templates/golden/python-fastapi/{{.PythonPackage}}/__init__.py",
				"templates/golden/python-fastapi/{{.PythonPackage}}/main.py",
				"templates/golden/python-fastapi/.forge-overlay/tests/test_health.py.tmpl",
				"templates/golden/python-fastapi/.forge-overlay/{{.PythonPackage}}/telemetry.py.tmpl",
				"templates/golden/python-fastapi/.forge-overlay/tests/test_telemetry.py.tmpl",
			},
		},
		{
			name: "csharp-cli",
			files: []string{
				"templates/golden/csharp-cli/Project.csproj.tmpl",
				"templates/golden/csharp-cli/Program.cs",
				"templates/golden/csharp-cli/GreetingBuilder.cs",
				"templates/golden/csharp-cli/.forge-overlay/tests/Project.Tests/Project.Tests.csproj.tmpl",
				"templates/golden/csharp-cli/.forge-overlay/tests/Project.Tests/ProgramTests.cs",
				"templates/golden/csharp-cli/.forge-overlay/Telemetry.cs.tmpl",
				"templates/golden/csharp-cli/.forge-overlay/tests/Project.Tests/TelemetryTests.cs.tmpl",
			},
		},
		{
			name: "csharp-webapi",
			files: []string{
				"templates/golden/csharp-webapi/Project.csproj.tmpl",
				"templates/golden/csharp-webapi/Program.cs",
				"templates/golden/csharp-webapi/WeatherForecast.cs.tmpl",
				"templates/golden/csharp-webapi/Controllers/WeatherForecastController.cs.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/tests/Project.Tests/Project.Tests.csproj.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/tests/Project.Tests/WeatherForecastEndpointTests.cs",
				"templates/golden/csharp-webapi/.forge-overlay/Controllers/HealthController.cs.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/Controllers/HealthStatus.cs.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/tests/Project.Tests/HealthEndpointTests.cs.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/Telemetry.cs.tmpl",
				"templates/golden/csharp-webapi/.forge-overlay/tests/Project.Tests/TelemetryTests.cs.tmpl",
			},
		},
		{
			name: "go-web-templ",
			files: []string{
				"templates/golden/go-web-templ/go.mod.tmpl",
				"templates/golden/go-web-templ/.forge-overlay/cmd/web/main.go.tmpl",
				"templates/golden/go-web-templ/.forge-overlay/internal/web/index.templ",
				"templates/golden/go-web-templ/.forge-overlay/internal/web/index_templ.go",
				"templates/golden/go-web-templ/.forge-overlay/internal/web/server.go",
				"templates/golden/go-web-templ/.forge-overlay/internal/web/server_test.go",
				"templates/golden/go-web-templ/.forge-overlay/internal/web/static/htmx.min.js",
				"templates/golden/go-web-templ/.forge-overlay/internal/telemetry/telemetry.go.tmpl",
				"templates/golden/go-web-templ/.forge-overlay/internal/telemetry/telemetry_test.go.tmpl",
			},
		},
		{
			name: "python-web-jinja",
			files: []string{
				"templates/golden/python-web-jinja/pyproject.toml.tmpl",
				"templates/golden/python-web-jinja/{{.PythonPackage}}/__init__.py",
				"templates/golden/python-web-jinja/.forge-overlay/{{.PythonPackage}}/main.py",
				"templates/golden/python-web-jinja/.forge-overlay/{{.PythonPackage}}/templates/index.html",
				"templates/golden/python-web-jinja/.forge-overlay/{{.PythonPackage}}/static/htmx.min.js",
				"templates/golden/python-web-jinja/.forge-overlay/tests/test_health.py.tmpl",
				"templates/golden/python-web-jinja/.forge-overlay/tests/test_index.py.tmpl",
				"templates/golden/python-web-jinja/.forge-overlay/{{.PythonPackage}}/telemetry.py.tmpl",
				"templates/golden/python-web-jinja/.forge-overlay/tests/test_telemetry.py.tmpl",
			},
		},
		{
			name: "csharp-blazor",
			files: []string{
				"templates/golden/csharp-blazor/Project.csproj.tmpl",
				"templates/golden/csharp-blazor/Project.Client/Project.Client.csproj.tmpl",
				"templates/golden/csharp-blazor/Project.Client/Program.cs.tmpl",
				"templates/golden/csharp-blazor/Components/App.razor",
				"templates/golden/csharp-blazor/Components/Routes.razor",
				"templates/golden/csharp-blazor/.forge-overlay/Program.cs.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/HealthReporter.cs.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/IHealthReporter.cs.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/HealthStatus.cs.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/Components/Pages/Home.razor.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/tests/Project.Tests/Project.Tests.csproj.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/tests/Project.Tests/HealthReporterTests.cs.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/Telemetry.cs.tmpl",
				"templates/golden/csharp-blazor/.forge-overlay/tests/Project.Tests/TelemetryTests.cs.tmpl",
			},
		},
		{
			name: "vite-ts",
			files: []string{
				"templates/golden/vite-ts/package.json.tmpl",
				"templates/golden/vite-ts/index.html.tmpl",
				"templates/golden/vite-ts/tsconfig.json",
				"templates/golden/vite-ts/src/main.ts",
				"templates/golden/vite-ts/.forge-overlay/package.json.tmpl",
				"templates/golden/vite-ts/.forge-overlay/src/api/client.ts.tmpl",
				"templates/golden/vite-ts/.forge-overlay/src/api/client.test.ts",
				"templates/golden/vite-ts/.forge-overlay/src/main.ts",
				"templates/golden/vite-ts/.forge-overlay/src/pages/home.ts",
				"templates/golden/vite-ts/.forge-overlay/src/components/.keep",
				"templates/golden/vite-ts/.forge-overlay/src/lib/.keep",
				"templates/golden/vite-ts/.forge-overlay/eslint.config.js",
				"templates/golden/vite-ts/.forge-overlay/vitest.config.ts",
				"templates/golden/vite-ts/.forge-overlay/src/lib/telemetry.ts.tmpl",
				"templates/golden/vite-ts/.forge-overlay/src/lib/telemetry.test.ts",
			},
		},
		{
			name: "sveltekit",
			files: []string{
				"templates/golden/sveltekit/package.json.tmpl",
				"templates/golden/sveltekit/tsconfig.json",
				"templates/golden/sveltekit/vite.config.ts",
				"templates/golden/sveltekit/src/routes/+page.svelte",
				"templates/golden/sveltekit/.forge-overlay/package.json.tmpl",
				"templates/golden/sveltekit/.forge-overlay/src/lib/api/client.ts.tmpl",
				"templates/golden/sveltekit/.forge-overlay/src/lib/api/client.test.ts",
				"templates/golden/sveltekit/.forge-overlay/src/routes/+page.svelte",
				"templates/golden/sveltekit/.forge-overlay/eslint.config.js",
				"templates/golden/sveltekit/.forge-overlay/vitest.config.ts",
				"templates/golden/sveltekit/.forge-overlay/src/lib/telemetry.ts.tmpl",
				"templates/golden/sveltekit/.forge-overlay/src/lib/telemetry.test.ts",
			},
		},
		{
			name: "angular",
			files: []string{
				"templates/golden/angular/package.json.tmpl",
				"templates/golden/angular/angular.json.tmpl",
				"templates/golden/angular/tsconfig.json",
				"templates/golden/angular/src/app/app.ts.tmpl",
				"templates/golden/angular/.forge-overlay/package.json.tmpl",
				"templates/golden/angular/.forge-overlay/src/app/api/client.ts",
				"templates/golden/angular/.forge-overlay/src/app/api/client.spec.ts",
				"templates/golden/angular/.forge-overlay/src/app/app.ts.tmpl",
				"templates/golden/angular/.forge-overlay/src/app/app.spec.ts.tmpl",
				"templates/golden/angular/.forge-overlay/src/environments/environment.ts.tmpl",
				"templates/golden/angular/.forge-overlay/eslint.config.js",
				"templates/golden/angular/.forge-overlay/src/app/telemetry.ts.tmpl",
				"templates/golden/angular/.forge-overlay/src/app/telemetry.spec.ts",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, relPath := range tt.files {
				fullPath := filepath.Join(repoRoot, relPath)

				info, err := os.Stat(fullPath)
				if err != nil {
					t.Fatalf("stat %s: %v", fullPath, err)
				}
				if info.IsDir() {
					t.Fatalf("%s is a directory, want file", fullPath)
				}
			}
		})
	}
}

func TestCSharpCLIProgramPlacesTopLevelStatementsBeforeTypeDeclarations(t *testing.T) {
	repoRoot := repoRoot(t)
	programPath := filepath.Join(repoRoot, "templates", "golden", "csharp-cli", "Program.cs")

	contentBytes, err := os.ReadFile(programPath)
	if err != nil {
		t.Fatalf("read %s: %v", programPath, err)
	}

	content := string(contentBytes)
	topLevelIndex := strings.Index(content, "var target =")
	typeIndex := strings.Index(content, "public static class GreetingBuilder")
	if topLevelIndex == -1 {
		t.Fatalf("program missing expected snippets:\n%s", content)
	}
	if typeIndex == -1 {
		return
	}
	if topLevelIndex > typeIndex {
		t.Fatalf("top-level statements must precede type declarations:\n%s", content)
	}
}

func TestCSharpCLIProjectTemplateExcludesTestSourcesFromRootCompile(t *testing.T) {
	repoRoot := repoRoot(t)
	projectPath := filepath.Join(repoRoot, "templates", "golden", "csharp-cli", "Project.csproj.tmpl")

	contentBytes, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read %s: %v", projectPath, err)
	}

	content := string(contentBytes)
	if !strings.Contains(content, `<Compile Remove="tests/**/*.cs" />`) {
		t.Fatalf("project template must exclude test sources from root compile:\n%s", content)
	}
}

func TestCSharpCLIStarterFilesCarryStyleCopFileHeaders(t *testing.T) {
	repoRoot := repoRoot(t)

	files := []string{
		filepath.Join(repoRoot, "templates", "golden", "csharp-cli", "Program.cs"),
		filepath.Join(repoRoot, "templates", "golden", "csharp-cli", ".forge-overlay", "tests", "Project.Tests", "ProgramTests.cs"),
	}

	for _, path := range files {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		content := string(contentBytes)
		if !strings.Contains(content, `<copyright file=`) {
			t.Fatalf("%s missing StyleCop file header:\n%s", path, content)
		}
	}
}

func TestCSharpCLIProjectFilesSuppressStyleCopHeaderMismatchRule(t *testing.T) {
	repoRoot := repoRoot(t)

	files := []string{
		filepath.Join(repoRoot, "templates", "golden", "csharp-cli", "Project.csproj.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "csharp-cli", ".forge-overlay", "tests", "Project.Tests", "Project.Tests.csproj.tmpl"),
	}

	for _, path := range files {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		content := string(contentBytes)
		if !strings.Contains(content, "SA1636") {
			t.Fatalf("%s missing SA1636 suppression:\n%s", path, content)
		}
	}
}

func TestCSharpCLIProjectFilesEnableXmlDocumentationAnalysis(t *testing.T) {
	repoRoot := repoRoot(t)

	files := []string{
		filepath.Join(repoRoot, "templates", "golden", "csharp-cli", "Project.csproj.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "csharp-cli", ".forge-overlay", "tests", "Project.Tests", "Project.Tests.csproj.tmpl"),
	}

	for _, path := range files {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		content := string(contentBytes)
		if !strings.Contains(content, "<GenerateDocumentationFile>true</GenerateDocumentationFile>") {
			t.Fatalf("%s must enable XML documentation output so StyleCop XML analysis can run:\n%s", path, content)
		}
	}
}

func TestPythonCLITyperStarterTestInvokesHelloSubcommand(t *testing.T) {
	repoRoot := repoRoot(t)
	mainPath := filepath.Join(repoRoot, "templates", "golden", "python-cli-typer", "src", "{{.PythonPackage}}", "main.py")
	testPath := filepath.Join(repoRoot, "templates", "golden", "python-cli-typer", ".forge-overlay", "tests", "test_cli.py.tmpl")

	mainBytes, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read %s: %v", mainPath, err)
	}

	testBytes, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("read %s: %v", testPath, err)
	}

	mainContent := string(mainBytes)
	testContent := string(testBytes)
	if strings.Contains(mainContent, `def hello(name: str = "world")`) &&
		!strings.Contains(testContent, `runner.invoke(app, ["--name", "Peter"])`) {
		t.Fatalf("python-cli-typer starter test must pass the defaulted Typer parameter as an option:\n%s", testContent)
	}
}

func TestCSharpWebAPIProjectTemplateExcludesTestSourcesFromRootCompile(t *testing.T) {
	repoRoot := repoRoot(t)
	projectPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "Project.csproj.tmpl")

	contentBytes, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read %s: %v", projectPath, err)
	}

	content := string(contentBytes)
	if !strings.Contains(content, `<Compile Remove="tests/**/*.cs" />`) {
		t.Fatalf("webapi project template must exclude test sources from root compile:\n%s", content)
	}
	if !strings.Contains(content, `<Content Remove="tests/**" />`) {
		t.Fatalf("webapi project template must exclude test artifacts from web content discovery:\n%s", content)
	}
}

func TestCSharpWebAPITestProjectDoesNotDependOnTestHost(t *testing.T) {
	repoRoot := repoRoot(t)
	projectPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", ".forge-overlay", "tests", "Project.Tests", "Project.Tests.csproj.tmpl")

	contentBytes, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read %s: %v", projectPath, err)
	}

	content := string(contentBytes)
	if strings.Contains(content, `PackageReference Include="Microsoft.AspNetCore.TestHost"`) {
		t.Fatalf("webapi test project must verify the real app process without Microsoft.AspNetCore.TestHost:\n%s", content)
	}
}

func TestCSharpWebAPIStarterFilesCarryStyleCopFileHeaders(t *testing.T) {
	repoRoot := repoRoot(t)

	files := []string{
		filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "Program.cs"),
		filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "WeatherForecast.cs.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "Controllers", "WeatherForecastController.cs.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", ".forge-overlay", "tests", "Project.Tests", "WeatherForecastEndpointTests.cs"),
	}

	for _, path := range files {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		content := string(contentBytes)
		if !strings.Contains(content, `<copyright file=`) {
			t.Fatalf("%s missing StyleCop file header:\n%s", path, content)
		}
	}
}

func TestCSharpWebAPIProjectFilesEnableXmlDocumentationAnalysis(t *testing.T) {
	repoRoot := repoRoot(t)

	files := []string{
		filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "Project.csproj.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", ".forge-overlay", "tests", "Project.Tests", "Project.Tests.csproj.tmpl"),
	}

	for _, path := range files {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		content := string(contentBytes)
		if !strings.Contains(content, "<GenerateDocumentationFile>true</GenerateDocumentationFile>") {
			t.Fatalf("%s must enable XML documentation output so StyleCop XML analysis can run:\n%s", path, content)
		}
	}
}

func TestCSharpWebAPIProjectTemplatePinsSwaggerDependencyForStarterProgram(t *testing.T) {
	repoRoot := repoRoot(t)
	projectPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "Project.csproj.tmpl")

	contentBytes, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read %s: %v", projectPath, err)
	}

	content := string(contentBytes)
	if !strings.Contains(content, `PackageReference Include="Swashbuckle.AspNetCore" Version="6.6.2"`) {
		t.Fatalf("webapi project template must pin the swagger dependency used by the starter program:\n%s", content)
	}
}

func TestCSharpWebAPIProjectFilesSuppressStyleCopHeaderMismatchRule(t *testing.T) {
	repoRoot := repoRoot(t)

	files := []string{
		filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "Project.csproj.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", ".forge-overlay", "tests", "Project.Tests", "Project.Tests.csproj.tmpl"),
	}

	for _, path := range files {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		content := string(contentBytes)
		if !strings.Contains(content, "SA1636") {
			t.Fatalf("%s missing SA1636 suppression:\n%s", path, content)
		}
	}
}

func TestCSharpWebAPITestProjectEnablesImplicitUsings(t *testing.T) {
	repoRoot := repoRoot(t)
	projectPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", ".forge-overlay", "tests", "Project.Tests", "Project.Tests.csproj.tmpl")

	contentBytes, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read %s: %v", projectPath, err)
	}

	content := string(contentBytes)
	if !strings.Contains(content, "<ImplicitUsings>enable</ImplicitUsings>") {
		t.Fatalf("webapi test project must enable implicit usings for starter async tests:\n%s", content)
	}
}

func TestCSharpWebAPIWeatherForecastEndpointTestUsesStyleCopFriendlyAsyncPattern(t *testing.T) {
	repoRoot := repoRoot(t)
	testPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", ".forge-overlay", "tests", "Project.Tests", "WeatherForecastEndpointTests.cs")

	contentBytes, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("read %s: %v", testPath, err)
	}

	content := string(contentBytes)
	if !strings.Contains(content, "/// <returns>A task that completes when the assertion finishes.</returns>") {
		t.Fatalf("weather forecast endpoint test must document the async return value for StyleCop:\n%s", content)
	}
	if !strings.Contains(content, "await Task.Delay(500, cancellationToken);") {
		t.Fatalf("weather forecast endpoint test must use a bounded readiness pause between probes:\n%s", content)
	}
}

func TestCSharpWebAPIProgramTemplateKeepsControllerBasedStarter(t *testing.T) {
	repoRoot := repoRoot(t)
	programPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "Program.cs")

	contentBytes, err := os.ReadFile(programPath)
	if err != nil {
		t.Fatalf("read %s: %v", programPath, err)
	}

	content := string(contentBytes)
	for _, snippet := range []string{
		"var builder = Program.CreateBuilder(args);",
		"builder.Services.AddControllers();",
		"var app = builder.Build();",
		"Program.ConfigureApp(app);",
		"app.MapControllers();",
		"public static WebApplicationBuilder CreateBuilder(string[] args)",
		"public static void ConfigureApp(WebApplication app)",
	} {
		if !strings.Contains(content, snippet) {
			t.Fatalf("program template missing controller-based webapi starter snippet %q:\n%s", snippet, content)
		}
	}
}

func TestCSharpWebAPIWeatherForecastEndpointTestUsesRealAppProcessProbe(t *testing.T) {
	repoRoot := repoRoot(t)
	testPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", ".forge-overlay", "tests", "Project.Tests", "WeatherForecastEndpointTests.cs")

	contentBytes, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("read %s: %v", testPath, err)
	}

	content := string(contentBytes)
	for _, snippet := range []string{
		"using System.Diagnostics;",
		"using System.Net.Sockets;",
		"ASPNETCORE_URLS",
		"dotnet",
		"run",
		"127.0.0.1",
		"Weatherforecast",
		"UseCookies = false",
	} {
		if !strings.Contains(content, snippet) {
			t.Fatalf("weather forecast endpoint test missing real-process verification snippet %q:\n%s", snippet, content)
		}
	}
}

func TestCSharpWebAPIWeatherForecastModelUsesTemplateNamespace(t *testing.T) {
	repoRoot := repoRoot(t)
	modelPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "WeatherForecast.cs.tmpl")

	contentBytes, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatalf("read %s: %v", modelPath, err)
	}

	content := string(contentBytes)
	for _, snippet := range []string{
		"namespace {{.CSharpNamespace}};",
		"public sealed class WeatherForecast",
		"public int TemperatureF => 32 + (int)(this.TemperatureC / 0.5556);",
	} {
		if !strings.Contains(content, snippet) {
			t.Fatalf("weather forecast model missing snippet %q:\n%s", snippet, content)
		}
	}
}

func TestCSharpWebAPIWeatherForecastControllerExposesControllerRoute(t *testing.T) {
	repoRoot := repoRoot(t)
	controllerPath := filepath.Join(repoRoot, "templates", "golden", "csharp-webapi", "Controllers", "WeatherForecastController.cs.tmpl")

	contentBytes, err := os.ReadFile(controllerPath)
	if err != nil {
		t.Fatalf("read %s: %v", controllerPath, err)
	}

	content := string(contentBytes)
	for _, snippet := range []string{
		"namespace {{.CSharpNamespace}}.Controllers",
		"using Microsoft.AspNetCore.Mvc;",
		"[ApiController]",
		"[Route(\"[controller]\")]",
		"[HttpGet(Name = \"GetWeatherForecast\")]",
		"Enumerable.Range(1, 5)",
	} {
		if !strings.Contains(content, snippet) {
			t.Fatalf("weather forecast controller missing snippet %q:\n%s", snippet, content)
		}
	}
}

func TestGoStacksPinPatchedGoToolchainForVulnerabilityGate(t *testing.T) {
	repoRoot := repoRoot(t)

	// govulncheck runs in every Go stack's `mise run ci`; the OpenTelemetry
	// exporters make net/http and crypto/tls reachable, so the pinned patch
	// release must carry the stdlib fixes (GO-2026-5026, -5856, -6090, ...).
	const patchedToolchain = "1.26.8"

	files := []string{
		filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", "go.mod.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "go-cli-cobra", ".forge-overlay", "mise.toml"),
		filepath.Join(repoRoot, "templates", "golden", "go-api-chi", "go.mod.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "go-api-chi", ".forge-overlay", "mise.toml.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "go-web-templ", "go.mod.tmpl"),
		filepath.Join(repoRoot, "templates", "golden", "go-web-templ", ".forge-overlay", "mise.toml"),
	}

	for _, path := range files {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		content := string(contentBytes)
		if !strings.Contains(content, patchedToolchain) {
			t.Fatalf("%s must pin Go %s so govulncheck passes:\n%s", path, patchedToolchain, content)
		}
	}
}

func TestGoldenOverlayMiseTomlIncludesDCGTool(t *testing.T) {
	repoRoot := repoRoot(t)

	stacks := []string{
		"go-cli-cobra",
		"go-api-chi",
		"go-web-templ",
		"python-cli-typer",
		"python-fastapi",
		"python-web-jinja",
		"csharp-cli",
		"csharp-webapi",
		"csharp-blazor",
		"vite-ts",
		"sveltekit",
		"angular",
	}

	const requiredTool = `"github:Dicklesworthstone/destructive_command_guard" = "latest"`

	for _, stack := range stacks {
		t.Run(stack, func(t *testing.T) {
			tomlPath := filepath.Join(repoRoot, "templates", "golden", stack, ".forge-overlay", "mise.toml")
			tmplPath := filepath.Join(repoRoot, "templates", "golden", stack, ".forge-overlay", "mise.toml.tmpl")

			var (
				contentBytes []byte
				targetPath   string
				err          error
			)

			if contentBytes, err = os.ReadFile(tomlPath); err == nil {
				targetPath = tomlPath
			} else if contentBytes, err = os.ReadFile(tmplPath); err == nil {
				targetPath = tmplPath
			} else {
				t.Fatalf("neither %s nor %s exists", tomlPath, tmplPath)
			}

			content := string(contentBytes)
			toolsIdx := strings.Index(content, "[tools]")
			if toolsIdx == -1 {
				t.Fatalf("%s missing [tools] section:\n%s", targetPath, content)
			}

			rest := content[toolsIdx+len("[tools]"):]
			nextSectionIdx := strings.Index(rest, "\n[")
			toolsSection := rest
			if nextSectionIdx != -1 {
				toolsSection = rest[:nextSectionIdx]
			}

			if !strings.Contains(toolsSection, requiredTool) {
				t.Fatalf("%s [tools] section missing %s:\n%s", targetPath, requiredTool, toolsSection)
			}
		})
	}
}

func TestGoldenStacksShipTelemetryBootstrap(t *testing.T) {
	repoRoot := repoRoot(t)

	tests := []struct {
		stack      string
		manifest   string
		depSnippet string
		module     string
		test       string
	}{
		{
			stack:      "go-cli-cobra",
			manifest:   "go.mod.tmpl",
			depSnippet: "go.opentelemetry.io/otel",
			module:     ".forge-overlay/cmd/telemetry.go.tmpl",
			test:       ".forge-overlay/cmd/telemetry_test.go.tmpl",
		},
		{
			stack:      "go-api-chi",
			manifest:   "go.mod.tmpl",
			depSnippet: "go.opentelemetry.io/otel",
			module:     ".forge-overlay/internal/telemetry/telemetry.go.tmpl",
			test:       ".forge-overlay/internal/telemetry/telemetry_test.go.tmpl",
		},
		{
			stack:      "go-web-templ",
			manifest:   "go.mod.tmpl",
			depSnippet: "go.opentelemetry.io/otel",
			module:     ".forge-overlay/internal/telemetry/telemetry.go.tmpl",
			test:       ".forge-overlay/internal/telemetry/telemetry_test.go.tmpl",
		},
		{
			stack:      "python-cli-typer",
			manifest:   "pyproject.toml.tmpl",
			depSnippet: "opentelemetry-distro",
			module:     ".forge-overlay/src/{{.PythonPackage}}/telemetry.py.tmpl",
			test:       ".forge-overlay/tests/test_telemetry.py.tmpl",
		},
		{
			stack:      "python-fastapi",
			manifest:   "pyproject.toml.tmpl",
			depSnippet: "opentelemetry-distro",
			module:     ".forge-overlay/{{.PythonPackage}}/telemetry.py.tmpl",
			test:       ".forge-overlay/tests/test_telemetry.py.tmpl",
		},
		{
			stack:      "python-web-jinja",
			manifest:   "pyproject.toml.tmpl",
			depSnippet: "opentelemetry-distro",
			module:     ".forge-overlay/{{.PythonPackage}}/telemetry.py.tmpl",
			test:       ".forge-overlay/tests/test_telemetry.py.tmpl",
		},
		{
			stack:      "csharp-cli",
			manifest:   "Project.csproj.tmpl",
			depSnippet: `Include="OpenTelemetry.Extensions.Hosting"`,
			module:     ".forge-overlay/Telemetry.cs.tmpl",
			test:       ".forge-overlay/tests/Project.Tests/TelemetryTests.cs.tmpl",
		},
		{
			stack:      "csharp-webapi",
			manifest:   "Project.csproj.tmpl",
			depSnippet: `Include="OpenTelemetry.Extensions.Hosting"`,
			module:     ".forge-overlay/Telemetry.cs.tmpl",
			test:       ".forge-overlay/tests/Project.Tests/TelemetryTests.cs.tmpl",
		},
		{
			stack:      "csharp-blazor",
			manifest:   "Project.csproj.tmpl",
			depSnippet: `Include="OpenTelemetry.Extensions.Hosting"`,
			module:     ".forge-overlay/Telemetry.cs.tmpl",
			test:       ".forge-overlay/tests/Project.Tests/TelemetryTests.cs.tmpl",
		},
		{
			stack:      "vite-ts",
			manifest:   ".forge-overlay/package.json.tmpl",
			depSnippet: `"@opentelemetry/sdk-trace-web"`,
			module:     ".forge-overlay/src/lib/telemetry.ts.tmpl",
			test:       ".forge-overlay/src/lib/telemetry.test.ts",
		},
		{
			stack:      "sveltekit",
			manifest:   ".forge-overlay/package.json.tmpl",
			depSnippet: `"@opentelemetry/sdk-trace-web"`,
			module:     ".forge-overlay/src/lib/telemetry.ts.tmpl",
			test:       ".forge-overlay/src/lib/telemetry.test.ts",
		},
		{
			stack:      "angular",
			manifest:   ".forge-overlay/package.json.tmpl",
			depSnippet: `"@opentelemetry/sdk-trace-web"`,
			module:     ".forge-overlay/src/app/telemetry.ts.tmpl",
			test:       ".forge-overlay/src/app/telemetry.spec.ts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.stack, func(t *testing.T) {
			stackRoot := filepath.Join(repoRoot, "templates", "golden", tt.stack)

			manifestPath := filepath.Join(stackRoot, tt.manifest)
			manifestBytes, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Errorf("%s manifest: read %s: %v", tt.stack, manifestPath, err)
			} else if !strings.Contains(string(manifestBytes), tt.depSnippet) {
				t.Errorf("%s manifest %s missing OpenTelemetry dependency %q", tt.stack, manifestPath, tt.depSnippet)
			}

			for _, relPath := range []string{tt.module, tt.test} {
				fullPath := filepath.Join(stackRoot, relPath)
				info, err := os.Stat(fullPath)
				if err != nil {
					t.Errorf("%s telemetry asset: stat %s: %v", tt.stack, fullPath, err)
					continue
				}
				if info.IsDir() {
					t.Errorf("%s telemetry asset %s is a directory, want file", tt.stack, fullPath)
				}
			}
		})
	}
}

func TestPythonStacksRunUnderOpentelemetryInstrument(t *testing.T) {
	repoRoot := repoRoot(t)

	stacks := []string{
		"python-cli-typer",
		"python-fastapi",
		"python-web-jinja",
	}

	const requiredSnippet = "opentelemetry-instrument"

	for _, stack := range stacks {
		t.Run(stack, func(t *testing.T) {
			tomlPath := filepath.Join(repoRoot, "templates", "golden", stack, ".forge-overlay", "mise.toml")
			tmplPath := filepath.Join(repoRoot, "templates", "golden", stack, ".forge-overlay", "mise.toml.tmpl")

			var (
				contentBytes []byte
				targetPath   string
				err          error
			)

			if contentBytes, err = os.ReadFile(tomlPath); err == nil {
				targetPath = tomlPath
			} else if contentBytes, err = os.ReadFile(tmplPath); err == nil {
				targetPath = tmplPath
			} else {
				t.Fatalf("neither %s nor %s exists", tomlPath, tmplPath)
			}

			content := string(contentBytes)
			if !strings.Contains(content, requiredSnippet) {
				t.Fatalf("%s must run the %s app under %s:\n%s", targetPath, stack, requiredSnippet, content)
			}
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	return filepath.Clean(filepath.Join(wd, ".."))
}
