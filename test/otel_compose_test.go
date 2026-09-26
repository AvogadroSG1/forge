package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

// otelCollectorLogPolicy is the rotating log policy the long-lived collector
// MUST ship with so its container logs cannot grow without bound.
var otelCollectorLogPolicy = struct {
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

type otelComposeFile struct {
	Services map[string]struct {
		Logging *otelComposeLogging `yaml:"logging" json:"logging"`
	} `yaml:"services" json:"services"`
}

func TestOtelComposeCollectorLogsBounded(t *testing.T) {
	data := readOtelAsset(t, "compose.yaml")

	var compose otelComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatalf("parse %s/compose.yaml as YAML: %v", otelComposeAssetDir, err)
	}

	assertCollectorLogPolicy(t, "shipped .otel/compose.yaml", compose)
}

func TestOtelComposeConfigResolvesLogging(t *testing.T) {
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

	assertCollectorLogPolicy(t, "docker compose config (resolved)", resolved)
}

// assertCollectorLogPolicy checks the otel-collector service carries the
// rotating log policy; label names the compose document under test.
func assertCollectorLogPolicy(t *testing.T, label string, compose otelComposeFile) {
	t.Helper()

	collector, ok := compose.Services["otel-collector"]
	if !ok {
		t.Fatalf("%s: no service %q (services: %v)", label, "otel-collector", serviceNames(compose))
	}
	if collector.Logging == nil {
		t.Fatalf("%s: service %q has no logging block; want driver %q with options %v", label, "otel-collector", otelCollectorLogPolicy.driver, otelCollectorLogPolicy.options)
	}
	if got := collector.Logging.Driver; got != otelCollectorLogPolicy.driver {
		t.Errorf("%s: otel-collector logging.driver = %q, want %q", label, got, otelCollectorLogPolicy.driver)
	}
	for key, want := range otelCollectorLogPolicy.options {
		raw, ok := collector.Logging.Options[key]
		if !ok {
			t.Errorf("%s: otel-collector logging.options[%q] missing, want %q", label, key, want)
			continue
		}
		got, ok := raw.(string)
		if !ok {
			t.Errorf("%s: otel-collector logging.options[%q] = %v (%T), want string %q", label, key, raw, raw, want)
			continue
		}
		if got != want {
			t.Errorf("%s: otel-collector logging.options[%q] = %q, want %q", label, key, got, want)
		}
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
