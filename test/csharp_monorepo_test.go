//go:build integration

package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCSharpMonorepoAdd(t *testing.T) {
	t.Parallel()

	t.Run("slnx-conventional-cpm-and-custom-outputs", func(t *testing.T) {
		t.Parallel()

		repo := tempRuntimeDirIn(t, "")
		envRoot := tempRuntimeDirIn(t, "")
		initNativeMonorepo(t, repo, envRoot, "Product.slnx", false)

		// Root Directory.Build.props / Directory.Build.targets with custom project-distinct
		// intermediate/output directories plus Central Package Management.
		writeMonorepoFile(t, filepath.Join(repo, "Directory.Build.props"), `<Project>
  <PropertyGroup>
    <UseArtifactsOutput>true</UseArtifactsOutput>
    <ArtifactsPath>$(MSBuildThisFileDirectory)artifacts</ArtifactsPath>
  </PropertyGroup>
</Project>
`)
		writeMonorepoFile(t, filepath.Join(repo, "Directory.Build.targets"), `<Project>
  <PropertyGroup>
    <ForgeMonorepoHostTargetsMarker>true</ForgeMonorepoHostTargetsMarker>
  </PropertyGroup>
</Project>
`)
		writeMonorepoFile(t, filepath.Join(repo, "Directory.Packages.props"), `<Project>
  <!-- Host monorepo CPM -->
  <PropertyGroup>
    <ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally>
  </PropertyGroup>
  <ItemGroup>
    <PackageVersion Include="xunit" Version="2.9.0" />
  </ItemGroup>
</Project>
`)

		sentinelAgents := "# Host Monorepo AGENTS\n"
		sentinelBeads := "prefix: hostmono\n"
		writeMonorepoFile(t, filepath.Join(repo, "AGENTS.md"), sentinelAgents)
		writeMonorepoFile(t, filepath.Join(repo, ".beads", "config.yaml"), sentinelBeads)
		runStep(t, "git add", repo, envRoot, "git", "add", ".")
		runStep(t, "git commit", repo, envRoot, "git", "commit", "-m", "initial monorepo")

		// 1. Add csharp-webapi service.
		runStep(t, "forge add csharp-webapi", repo, envRoot,
			forgeBinary, "add",
			"--stack", "csharp-webapi",
			"--project-name", "OrdersApi",
			"--path", "services/orders-api",
			"--solution", "Product.slnx",
			"--author-name", "CI Bot",
		)

		// 2. Add csharp-cli service into the same monorepo.
		runStep(t, "forge add csharp-cli", repo, envRoot,
			forgeBinary, "add",
			"--stack", "csharp-cli",
			"--project-name", "BillingCli",
			"--path", "services/billing-cli",
			"--solution", "Product.slnx",
			"--author-name", "CI Bot",
		)

		// Verify solution registration and run full solution tests in Debug and Release.
		slnList := runStepOutput(t, "dotnet sln list", repo, envRoot, "dotnet", "sln", "Product.slnx", "list")
		for _, want := range []string{
			"src/ExistingApp/ExistingApp.csproj",
			"services/orders-api/OrdersApi.csproj",
			"services/orders-api/tests/OrdersApi.Tests/OrdersApi.Tests.csproj",
			"services/billing-cli/BillingCli.csproj",
			"services/billing-cli/tests/BillingCli.Tests/BillingCli.Tests.csproj",
		} {
			if !strings.Contains(strings.ReplaceAll(slnList, "\\", "/"), want) {
				t.Fatalf("dotnet sln Product.slnx list missing %q:\n%s", want, slnList)
			}
		}

		runStep(t, "dotnet test Debug", repo, envRoot, "dotnet", "test", "Product.slnx", "--configuration", "Debug")
		runStep(t, "dotnet test Release", repo, envRoot, "dotnet", "test", "Product.slnx", "--configuration", "Release")

		assertProjectHasNoServiceItems(t, repo, envRoot, "src/ExistingApp/ExistingApp.csproj", "services/")

		// Exercise observable CLI greeting behavior.
		cliOut := runStepOutput(t, "dotnet run BillingCli", repo, envRoot,
			"dotnet", "run", "--project", "services/billing-cli/BillingCli.csproj", "--", "Monorepo")
		if !strings.Contains(cliOut, "hello, Monorepo!") {
			t.Fatalf("BillingCli output = %q, want greeting containing %q", cliOut, "hello, Monorepo!")
		}

		// Exercise observable Web API /Weatherforecast and /health endpoints.
		verifyWebEndpoint(t, repo, envRoot, "services/orders-api/OrdersApi.csproj", []httpCheck{
			{path: "/Weatherforecast", wantSubstring: "temperaturec"},
			{path: "/health", wantSubstring: `"status":"ok"`},
		})

		// Verify host sentinels remain byte-for-byte unchanged.
		if got := readMonorepoString(t, filepath.Join(repo, "AGENTS.md")); got != sentinelAgents {
			t.Fatalf("root AGENTS.md changed: %q", got)
		}
		if got := readMonorepoString(t, filepath.Join(repo, ".beads", "config.yaml")); got != sentinelBeads {
			t.Fatalf("root .beads/config.yaml changed: %q", got)
		}

		// Second forge add targeting an existing path MUST fail without changing tracked bytes.
		slnBeforeDup := readMonorepoString(t, filepath.Join(repo, "Product.slnx"))
		dupCmd := exec.Command(forgeBinary, "add",
			"--stack", "csharp-webapi",
			"--project-name", "OrdersApiDup",
			"--path", "services/orders-api",
			"--solution", "Product.slnx",
			"--author-name", "CI Bot",
		)
		dupCmd.Dir = repo
		dupCmd.Env = commandEnv(envRoot, forgeBinary)
		if dupOut, err := dupCmd.CombinedOutput(); err == nil {
			t.Fatalf("duplicate forge add unexpectedly succeeded:\n%s", dupOut)
		}
		if got := readMonorepoString(t, filepath.Join(repo, "Product.slnx")); got != slnBeforeDup {
			t.Fatal("Product.slnx mutated by failed duplicate forge add")
		}
	})

	t.Run("sln-root-web-capturing-host-with-blazor", func(t *testing.T) {
		t.Parallel()

		repo := tempRuntimeDirIn(t, "")
		envRoot := tempRuntimeDirIn(t, "")
		initNativeMonorepo(t, repo, envRoot, "Legacy.sln", true)

		sentinelAgents := "# Legacy Root Web Monorepo\n"
		writeMonorepoFile(t, filepath.Join(repo, "AGENTS.md"), sentinelAgents)
		runStep(t, "git add", repo, envRoot, "git", "add", ".")
		runStep(t, "git commit", repo, envRoot, "git", "commit", "-m", "initial root web host")

		runStep(t, "forge add csharp-blazor", repo, envRoot,
			forgeBinary, "add",
			"--stack", "csharp-blazor",
			"--project-name", "PortalWeb",
			"--path", "services/portal-web",
			"--author-name", "CI Bot",
		)

		slnList := runStepOutput(t, "dotnet sln list", repo, envRoot, "dotnet", "sln", "Legacy.sln", "list")
		for _, want := range []string{
			"HostWeb.csproj",
			"services/portal-web/PortalWeb.csproj",
			"services/portal-web/PortalWeb.Client/PortalWeb.Client.csproj",
			"services/portal-web/tests/PortalWeb.Tests/PortalWeb.Tests.csproj",
		} {
			if !strings.Contains(strings.ReplaceAll(slnList, "\\", "/"), want) {
				t.Fatalf("dotnet sln Legacy.sln list missing %q:\n%s", want, slnList)
			}
		}

		runStep(t, "dotnet test Debug", repo, envRoot, "dotnet", "test", "Legacy.sln", "--configuration", "Debug")
		runStep(t, "dotnet test Release", repo, envRoot, "dotnet", "test", "Legacy.sln", "--configuration", "Release")

		assertProjectHasNoServiceItems(t, repo, envRoot, "HostWeb.csproj", "services/")

		// Exercise observable Blazor /health and rendered home page health status.
		verifyWebEndpoint(t, repo, envRoot, "services/portal-web/PortalWeb.csproj", []httpCheck{
			{path: "/health", wantSubstring: `"status":"ok"`},
			{path: "/", wantSubstring: `id="api-status">ok</span>`},
		})

		if got := readMonorepoString(t, filepath.Join(repo, "AGENTS.md")); got != sentinelAgents {
			t.Fatalf("root AGENTS.md changed: %q", got)
		}
	})
}

func initNativeMonorepo(t *testing.T, repo, envRoot, solutionName string, rootWebHost bool) {
	t.Helper()

	runStep(t, "git init", repo, envRoot, "git", "init", "-b", "main")
	runStep(t, "git user.name", repo, envRoot, "git", "config", "user.name", "Host Maintainer")
	runStep(t, "git user.email", repo, envRoot, "git", "config", "user.email", "host@example.com")

	format := "slnx"
	if strings.HasSuffix(solutionName, ".sln") {
		format = "sln"
	}
	slnBase := strings.TrimSuffix(solutionName, filepath.Ext(solutionName))
	runStep(t, "dotnet new sln", repo, envRoot, "dotnet", "new", "sln", "--name", slnBase, "--format", format)

	if rootWebHost {
		runStep(t, "dotnet new web", repo, envRoot, "dotnet", "new", "web", "--name", "HostWeb", "--output", ".", "--framework", "net10.0")
		runStep(t, "dotnet sln add HostWeb", repo, envRoot, "dotnet", "sln", solutionName, "add", "HostWeb.csproj")
	} else {
		runStep(t, "dotnet new console", repo, envRoot, "dotnet", "new", "console", "--name", "ExistingApp", "--output", "src/ExistingApp", "--framework", "net10.0")
		runStep(t, "dotnet sln add ExistingApp", repo, envRoot, "dotnet", "sln", solutionName, "add", "src/ExistingApp/ExistingApp.csproj")
	}
}

func assertProjectHasNoServiceItems(t *testing.T, repo, envRoot, projRel, servicePrefix string) {
	t.Helper()

	raw := runStepOutput(t, "dotnet msbuild items", repo, envRoot,
		"dotnet", "msbuild", projRel, "-nologo", "-getItem:Compile,Content,None,EmbeddedResource,RazorComponent,RazorGenerate")

	var parsed struct {
		Items map[string][]struct {
			Identity string `json:"Identity"`
			FullPath string `json:"FullPath"`
		} `json:"Items"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("parse msbuild JSON for %s: %v\n%s", projRel, err, raw)
	}

	serviceAbs := filepath.ToSlash(filepath.Join(repo, filepath.FromSlash(servicePrefix)))
	for itemType, list := range parsed.Items {
		for _, it := range list {
			norm := filepath.ToSlash(strings.ReplaceAll(it.FullPath, "\\", "/"))
			if strings.Contains(norm, "/"+servicePrefix) || strings.HasPrefix(norm, serviceAbs) {
				t.Fatalf("existing host project %s leaked %s item under %s: %s", projRel, itemType, servicePrefix, norm)
			}
		}
	}
}

type httpCheck struct {
	path          string
	wantSubstring string
}

func verifyWebEndpoint(t *testing.T, repo, envRoot, projRel string, checks []httpCheck) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	baseURL := "http://127.0.0.1:" + strconvItoa(port)

	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()

	if err := prepareCommandEnv(envRoot, "dotnet"); err != nil {
		t.Fatalf("prepare dotnet env: %v", err)
	}
	cmdEnv := append(commandEnv(envRoot, "dotnet"), "ASPNETCORE_URLS="+baseURL)
	cmd := exec.CommandContext(ctx, resolveCommandPathForEnv("dotnet", cmdEnv),
		"run", "--project", projRel, "--no-launch-profile")
	cmd.Dir = repo
	cmd.Env = cmdEnv

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", projRel, err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}

	deadline := time.Now().Add(45 * time.Second)
	var lastErr string
	for time.Now().Before(deadline) {
		allOK := true
		for _, chk := range checks {
			resp, reqErr := client.Get(baseURL + chk.path)
			if reqErr != nil {
				lastErr = chk.path + ": " + reqErr.Error()
				allOK = false
				break
			}
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				lastErr = chk.path + ": read: " + readErr.Error()
				allOK = false
				break
			}
			if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(string(body)), strings.ToLower(chk.wantSubstring)) {
				lastErr = chk.path + ": status=" + strconvItoa(resp.StatusCode) + " body=" + string(body)
				allOK = false
				break
			}
		}
		if allOK {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("%s did not pass HTTP checks (%s)\n--- output ---\n%s", projRel, lastErr, output.String())
}

func runStepOutput(t *testing.T, name, dir, envRoot, command string, args ...string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()

	if err := prepareCommandEnv(envRoot, command); err != nil {
		t.Fatalf("prepare command env: %v", err)
	}
	cmdEnv := commandEnv(envRoot, command)
	cmd := exec.CommandContext(ctx, resolveCommandPathForEnv(command, cmdEnv), args...)
	cmd.Dir = dir
	cmd.Env = cmdEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s (%s): %v\n%s", name, cmd.String(), err, out)
	}
	return string(out)
}

func writeMonorepoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func readMonorepoString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(b)
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
