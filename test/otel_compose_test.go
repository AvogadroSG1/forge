package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"forge"
)

// otelComposeAssetDir is the embedded source of the shipped .otel/ directory.
const otelComposeAssetDir = "templates/common/otel"

// otelComposeConfigTimeout bounds `docker compose config`, which only parses
// and resolves the file and never contacts the daemon.
const otelComposeConfigTimeout = 30 * time.Second

const (
	otelDashboardImage         = "mcr.microsoft.com/dotnet/aspire-dashboard:13.5.2"
	otelDashboardContainerName = "forge-otel-dashboard"
)

// otelContainerLogPolicy is the rotating log policy both long-lived services
// MUST ship with so their container logs cannot grow without bound.
var otelContainerLogPolicy = struct {
	driver  string
	options map[string]string
}{
	driver: "local",
	options: map[string]string{
		"max-size": "10m",
		"max-file": "3",
	},
}

// otelComposeLogging is the subset of a compose service's `logging` block the
// log-policy contract asserts on. Options stay untyped so a non-string value
// (e.g. an unquoted YAML integer) is reported instead of silently coerced.
type otelComposeLogging struct {
	Driver  string         `yaml:"driver" json:"driver"`
	Options map[string]any `yaml:"options" json:"options"`
}

type otelComposeDependency struct {
	Condition string `yaml:"condition" json:"condition"`
}

type otelComposeService struct {
	Image         string                           `yaml:"image" json:"image"`
	ContainerName string                           `yaml:"container_name" json:"container_name"`
	Restart       string                           `yaml:"restart" json:"restart"`
	Environment   map[string]any                   `yaml:"environment" json:"environment"`
	Ports         []any                            `yaml:"ports" json:"ports"`
	DependsOn     map[string]otelComposeDependency `yaml:"depends_on" json:"depends_on"`
	Logging       *otelComposeLogging              `yaml:"logging" json:"logging"`
}

type otelComposeFile struct {
	Services map[string]otelComposeService `yaml:"services" json:"services"`
}

func TestOtelComposeRuntimeContract(t *testing.T) {
	data := readOtelAsset(t, "compose.yaml")

	var compose otelComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatalf("parse %s/compose.yaml as YAML: %v", otelComposeAssetDir, err)
	}

	assertOtelComposeRuntimeContract(t, "shipped .otel/compose.yaml", compose)
}

func TestOtelComposeConfigResolvesRuntimeContract(t *testing.T) {
	// HOME is deliberately not isolated: the compose CLI plugin usually lives
	// in ~/.docker/cli-plugins and an isolated HOME would hide it.
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not on PATH; skipping docker compose config check")
	}
	if out, err := runDockerWithin(t, docker, "", "compose", "version"); err != nil {
		t.Skipf("docker compose unavailable (%v); skipping docker compose config check\n%s", err, out)
	}

	dir := t.TempDir()
	// collector.yaml is bind-mounted relative to compose.yaml, so both files
	// are staged together exactly as `mise run otel` copies them.
	for _, name := range []string{"compose.yaml", "collector.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), readOtelAsset(t, name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	composePath := filepath.Join(dir, "compose.yaml")
	out, err := runDockerWithin(t, docker, dir, "compose", "-p", "forge-otel-configcheck", "-f", composePath, "config", "--format", "json")
	if err != nil {
		t.Fatalf("docker compose config on shipped .otel/compose.yaml failed: %v\n%s", err, out)
	}

	var resolved otelComposeFile
	if err := json.Unmarshal(out, &resolved); err != nil {
		t.Fatalf("parse docker compose config JSON: %v\n%s", err, out)
	}

	assertOtelComposeRuntimeContract(t, "docker compose config (resolved)", resolved)
}

// assertOtelComposeRuntimeContract checks the exact local-only dashboard and
// bounded logging contract; label names the compose document under test.
func assertOtelComposeRuntimeContract(t *testing.T, label string, compose otelComposeFile) {
	t.Helper()

	collector, ok := compose.Services["otel-collector"]
	if !ok {
		t.Fatalf("%s: no service %q (services: %v)", label, "otel-collector", serviceNames(compose))
	}
	assertOtelLogPolicy(t, label, "otel-collector", collector)

	dashboard, ok := compose.Services["otel-dashboard"]
	if !ok {
		t.Fatalf("%s: no service %q (services: %v)", label, "otel-dashboard", serviceNames(compose))
	}
	if got := dashboard.Image; got != otelDashboardImage {
		t.Errorf("%s: otel-dashboard image = %q, want %q", label, got, otelDashboardImage)
	}
	if got := dashboard.ContainerName; got != otelDashboardContainerName {
		t.Errorf("%s: otel-dashboard container_name = %q, want %q", label, got, otelDashboardContainerName)
	}
	if got := dashboard.Restart; got != "unless-stopped" {
		t.Errorf("%s: otel-dashboard restart = %q, want %q", label, got, "unless-stopped")
	}
	if got, ok := dashboard.Environment["DOTNET_DASHBOARD_UNSECURED_ALLOW_ANONYMOUS"].(string); !ok || got != "true" {
		t.Errorf("%s: otel-dashboard anonymous setting = %#v, want string %q", label, dashboard.Environment["DOTNET_DASHBOARD_UNSECURED_ALLOW_ANONYMOUS"], "true")
	}
	assertOtelLogPolicy(t, label, "otel-dashboard", dashboard)

	aspireServices := 0
	for _, service := range compose.Services {
		if service.Image == otelDashboardImage {
			aspireServices++
		}
	}
	if aspireServices != 1 {
		t.Errorf("%s: services using %q = %d, want exactly 1", label, otelDashboardImage, aspireServices)
	}

	if len(dashboard.Ports) != 1 {
		t.Errorf("%s: otel-dashboard published ports = %v, want only 127.0.0.1:18888:18888", label, dashboard.Ports)
	} else {
		hostIP, published, target, protocol, err := normalizeComposePort(dashboard.Ports[0])
		if err != nil {
			t.Errorf("%s: parse otel-dashboard port: %v", label, err)
		} else if hostIP != "127.0.0.1" || published != "18888" || target != "18888" || (protocol != "" && protocol != "tcp") {
			t.Errorf("%s: otel-dashboard port = host %q published %q target %q protocol %q, want 127.0.0.1:18888:18888/tcp", label, hostIP, published, target, protocol)
		}
	}
	for _, rawPort := range dashboard.Ports {
		_, published, target, _, err := normalizeComposePort(rawPort)
		if err == nil && (published == "18889" || published == "18890" || target == "18889" || target == "18890") {
			t.Errorf("%s: otel-dashboard publishes OTLP port %v; 18889 and 18890 must stay internal", label, rawPort)
		}
	}

	dependency, ok := collector.DependsOn["otel-dashboard"]
	if !ok {
		t.Errorf("%s: otel-collector does not depend on otel-dashboard", label)
	} else if dependency.Condition != "service_started" {
		t.Errorf("%s: otel-collector dependency condition for otel-dashboard = %q, want %q", label, dependency.Condition, "service_started")
	}
}

func assertOtelLogPolicy(t *testing.T, label, serviceName string, service otelComposeService) {
	t.Helper()

	if service.Logging == nil {
		t.Fatalf("%s: service %q has no logging block; want driver %q with options %v", label, serviceName, otelContainerLogPolicy.driver, otelContainerLogPolicy.options)
	}
	if got := service.Logging.Driver; got != otelContainerLogPolicy.driver {
		t.Errorf("%s: %s logging.driver = %q, want %q", label, serviceName, got, otelContainerLogPolicy.driver)
	}
	for key, want := range otelContainerLogPolicy.options {
		raw, ok := service.Logging.Options[key]
		if !ok {
			t.Errorf("%s: %s logging.options[%q] missing, want %q", label, serviceName, key, want)
			continue
		}
		got, ok := raw.(string)
		if !ok {
			t.Errorf("%s: %s logging.options[%q] = %v (%T), want string %q", label, serviceName, key, raw, raw, want)
			continue
		}
		if got != want {
			t.Errorf("%s: %s logging.options[%q] = %q, want %q", label, serviceName, key, got, want)
		}
	}
}

func normalizeComposePort(raw any) (hostIP, published, target, protocol string, err error) {
	switch value := raw.(type) {
	case string:
		parts := strings.Split(value, ":")
		if len(parts) != 3 {
			return "", "", "", "", fmt.Errorf("unexpected short syntax %q", value)
		}
		return parts[0], parts[1], parts[2], "", nil
	case map[string]any:
		return fmt.Sprint(value["host_ip"]), fmt.Sprint(value["published"]), fmt.Sprint(value["target"]), fmt.Sprint(value["protocol"]), nil
	default:
		return "", "", "", "", fmt.Errorf("unexpected port value %#v (%T)", raw, raw)
	}
}

// readOtelAsset returns the embedded bytes of the shipped .otel/<name> file.
func readOtelAsset(t *testing.T, name string) []byte {
	t.Helper()

	path := otelComposeAssetDir + "/" + name
	data, err := fs.ReadFile(forge.Assets(), path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// runDockerWithin runs docker with args in dir (the current directory when
// empty) bounded by otelComposeConfigTimeout, returning stdout on success and
// combined stdout+stderr on failure.
func runDockerWithin(t *testing.T, docker, dir string, args ...string) ([]byte, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), otelComposeConfigTimeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, docker, args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), err
	}
	return stdout.Bytes(), nil
}

func serviceNames(compose otelComposeFile) []string {
	names := make([]string, 0, len(compose.Services))
	for name := range compose.Services {
		names = append(names, name)
	}
	return names
}
