package test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These names mirror templates/common/otel/compose.yaml: compose project
// `forge-otel`, a fixed container_name, and the named volume prefixed with
// the project name.
const (
	otelCollectorContainer = "forge-otel-collector"
	otelDataVolume         = "forge-otel_forge-otel-data"
	otelNetwork            = "forge-otel_default"
	otelVolumeReaderImage  = "busybox:1.36"
	otelTracesURL          = "http://127.0.0.1:4318/v1/traces"
	otelChangedTracesPath  = "/data/traces-changed.jsonl"
	otelShippedTracesPath  = "/data/traces.jsonl"
	otelComposeLabel       = "label=com.docker.compose.project=forge-otel"
	otelLiveTaskTimeout    = 2 * time.Minute
	otelTraceFlushTimeout  = 30 * time.Second
	otelDockerCmdTimeout   = 30 * time.Second
	// otelCleanupReserve is held back from the go test deadline so
	// t.Cleanup can still tear the stack down: if `go test -timeout` fires
	// first, cleanups never run and the containers leak.
	otelCleanupReserve = 90 * time.Second
	otelCleanupTimeout = 60 * time.Second
)

// otelLiveHostPorts are the host ports the shipped compose stack binds.
var otelLiveHostPorts = []string{"4317", "4318", "13133"}

// otelDockerPassthroughEnv are daemon-selection variables that MUST reach
// docker unchanged; HOME is isolated, so without them docker would fall back
// to its default context instead of the one the developer uses.
var otelDockerPassthroughEnv = []string{
	"DOCKER_HOST",
	"DOCKER_CONTEXT",
	"DOCKER_TLS_VERIFY",
	"DOCKER_CERT_PATH",
	"DOCKER_API_VERSION",
}

func TestOtelLifecycleRecreatesOnConfigChange(t *testing.T) {
	requireOtelDockerLifecycle(t)
	h := newOtelLiveHarness(t)

	t.Cleanup(func() { h.teardownCollector(t) })

	// Given the collector is running with the shipped config.
	if result := h.run(t, "otel"); result.exitCode != 0 {
		t.Fatalf("precondition: first mise run otel exit = %d, want 0\n%s", result.exitCode, result.output)
	}
	firstMarker := postOtelTrace(t, "first")
	if !h.waitForVolumeFileContains(t, otelShippedTracesPath, firstMarker) {
		t.Fatalf("precondition: trace %q never reached %s with the shipped config; the pipeline itself is broken\n%s", firstMarker, otelShippedTracesPath, h.collectorLogs(t))
	}

	// And I change the traces file exporter path in .otel/collector.yaml.
	configPath := filepath.Join(h.repoDir, ".otel", "collector.yaml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read %s: %v", configPath, err)
	}
	shipped := "path: " + otelShippedTracesPath
	if n := bytes.Count(config, []byte(shipped)); n != 1 {
		t.Fatalf("precondition: %s contains %q %d times, want 1", configPath, shipped, n)
	}
	changed := bytes.Replace(config, []byte(shipped), []byte("path: "+otelChangedTracesPath), 1)
	if err := os.WriteFile(configPath, changed, 0o644); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}
	beforeID := h.collectorContainerID(t)

	// When I run "mise run otel".
	if result := h.run(t, "otel"); result.exitCode != 0 {
		t.Fatalf("second mise run otel exit = %d, want 0\n%s", result.exitCode, result.output)
	}

	// Then the collector container has been recreated.
	if afterID := h.collectorContainerID(t); afterID == beforeID {
		t.Errorf("collector container ID = %s after config change and rerun, want a recreated container (ID unchanged)", afterID)
	}

	// And a newly sent trace is written to the changed path.
	secondMarker := postOtelTrace(t, "second")
	if !h.waitForVolumeFileContains(t, otelChangedTracesPath, secondMarker) {
		t.Errorf("trace %q not written to %s within %s; the collector is still running the old pipeline\n%s", secondMarker, otelChangedTracesPath, otelTraceFlushTimeout, h.collectorLogs(t))
	}
}

// requireOtelDockerLifecycle skips unless the live docker lifecycle test was
// opted into and can run without disturbing a developer's real collector.
func requireOtelDockerLifecycle(t *testing.T) {
	t.Helper()

	if os.Getenv("FORGE_DOCKER_TESTS") != "1" {
		t.Skip("set FORGE_DOCKER_TESTS=1 to run live docker otel lifecycle tests")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; skipping live otel lifecycle test")
	}
	if _, err := os.Stat(resolveCommandPath("mise")); err != nil {
		t.Skip("mise not installed; skipping live otel lifecycle test")
	}
	if out, err := otelHostDocker("info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Skipf("docker daemon not reachable (docker info: %v); skipping live otel lifecycle test\n%s", err, out)
	}
	if out, err := otelHostDocker("container", "inspect", otelCollectorContainer); err == nil {
		t.Skipf("container %s already exists; refusing to clobber a real forge collector\n%.200s", otelCollectorContainer, out)
	}
	out, err := otelHostDocker("ps", "-aq", "--filter", otelComposeLabel)
	if err != nil {
		t.Skipf("docker ps --filter %s: %v; cannot prove the forge-otel project is absent\n%s", otelComposeLabel, err, out)
	}
	if ids := strings.Fields(out); len(ids) > 0 {
		t.Skipf("%d container(s) with %s already exist; refusing to clobber a real forge collector", len(ids), otelComposeLabel)
	}
	if _, err := otelHostDocker("volume", "inspect", otelDataVolume); err == nil {
		t.Skipf("volume %s already exists; refusing to delete a real forge collector's telemetry", otelDataVolume)
	}
	for _, port := range otelLiveHostPorts {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			t.Skipf("127.0.0.1:%s is in use (%v); skipping live otel lifecycle test", port, err)
		}
		_ = ln.Close()
	}
}

// otelHostDocker runs docker with the developer's own environment; it is
// only used for gating, before the isolated harness exists.
func otelHostDocker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), otelDockerCmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// newOtelLiveHarness scaffolds a repo with the isolated mise env but real
// docker and curl on PATH. HOME is isolated, so docker's CLI config (compose
// plugin, current context, credential store) is pointed back at the
// developer's real config dir and daemon-selection variables pass through.
func newOtelLiveHarness(t *testing.T) *otelTaskHarness {
	t.Helper()

	h := newOtelRepoHarness(t, "repo")
	h.timeout = otelLiveTaskTimeout
	if deadline, ok := t.Deadline(); ok {
		h.stopBy = deadline.Add(-otelCleanupReserve)
		if time.Until(h.stopBy) < otelLiveTaskTimeout {
			t.Skipf("go test deadline leaves %s, too little for the live otel lifecycle plus %s cleanup reserve; raise -timeout", time.Until(deadline).Round(time.Second), otelCleanupReserve)
		}
	}

	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		t.Fatalf("look up docker: %v", err)
	}
	pathDirs := []string{filepath.Dir(dockerPath)}
	// Docker Desktop symlinks docker into /usr/local/bin; its credential
	// helpers (docker-credential-*) live beside the resolved binary.
	if resolved, err := filepath.EvalSymlinks(dockerPath); err == nil && filepath.Dir(resolved) != pathDirs[0] {
		pathDirs = append(pathDirs, filepath.Dir(resolved))
	}
	pathDirs = append(pathDirs, filepath.Dir(h.mise), "/usr/bin", "/bin")
	h.env = append(h.env, "PATH="+strings.Join(pathDirs, string(os.PathListSeparator)))

	dockerConfig := os.Getenv("DOCKER_CONFIG")
	if dockerConfig == "" {
		realHome, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("resolve real home for docker config: %v", err)
		}
		dockerConfig = filepath.Join(realHome, ".docker")
	}
	h.env = append(h.env, "DOCKER_CONFIG="+dockerConfig)
	for _, key := range otelDockerPassthroughEnv {
		if value, ok := os.LookupEnv(key); ok {
			h.env = append(h.env, key+"="+value)
		}
	}

	if out, err := h.docker("compose", "version"); err != nil {
		t.Fatalf("harness: docker compose unavailable under the isolated env: %v\n%s", err, out)
	}

	return h
}

// docker runs docker with the harness env and returns combined output.
func (h *otelTaskHarness) docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), otelDockerCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = h.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *otelTaskHarness) collectorContainerID(t *testing.T) string {
	t.Helper()

	out, err := h.docker("inspect", "-f", "{{.Id}}", otelCollectorContainer)
	if err != nil {
		t.Fatalf("docker inspect %s: %v\n%s", otelCollectorContainer, err, out)
	}
	return strings.TrimSpace(out)
}

func (h *otelTaskHarness) collectorLogs(t *testing.T) string {
	t.Helper()

	out, err := h.docker("logs", "--tail", "40", otelCollectorContainer)
	if err != nil {
		return fmt.Sprintf("(docker logs %s: %v)", otelCollectorContainer, err)
	}
	return "collector logs (tail):\n" + out
}

// waitForVolumeFileContains polls a file inside the collector's data volume
// until it contains marker or otelTraceFlushTimeout elapses. The contrib
// image is distroless, so the file is read through a throwaway busybox
// container mounting the volume read-only.
func (h *otelTaskHarness) waitForVolumeFileContains(t *testing.T, path, marker string) bool {
	t.Helper()

	deadline := time.Now().Add(otelTraceFlushTimeout)
	for {
		out, err := h.docker("run", "--rm", "-v", otelDataVolume+":/data:ro", otelVolumeReaderImage, "cat", path)
		if err == nil && strings.Contains(out, marker) {
			return true
		}
		if time.Now().After(deadline) {
			t.Logf("last read of %s: err=%v\n%.500s", path, err, out)
			return false
		}
		time.Sleep(time.Second)
	}
}

// teardownCollector stops the stack and removes everything the test
// created. It never fails fatally so every step runs even after a failure,
// and it bounds itself by the cleanup reserve rather than h.stopBy.
func (h *otelTaskHarness) teardownCollector(t *testing.T) {
	t.Helper()

	timeout := otelCleanupTimeout
	if deadline, ok := t.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline)-5*time.Second)
	}
	result, err := h.tryRunWithin("otel:down", max(timeout, time.Second))
	if err != nil || result.exitCode != 0 {
		t.Errorf("cleanup: mise run otel:down exit = %d, err = %v\n%s", result.exitCode, err, result.output)
	}
	// Fallback for a failed or partial `down`: remove every container of
	// the compose project (collector and otel-data-init). The gate
	// guaranteed none of them pre-existed, so all of them are ours.
	out, err := h.docker("ps", "-aq", "--filter", otelComposeLabel)
	if err != nil {
		t.Errorf("cleanup: docker ps --filter %s: %v\n%s", otelComposeLabel, err, out)
	}
	if ids := strings.Fields(out); len(ids) > 0 {
		if rmOut, err := h.docker(append([]string{"rm", "-f"}, ids...)...); err != nil {
			t.Errorf("cleanup: docker rm -f %v: %v\n%s", ids, err, rmOut)
		}
	}
	// `compose down` removes the network; this only matters when it failed.
	_, _ = h.docker("network", "rm", otelNetwork)
	// otel:down runs `compose down` without -v, so the named volume survives
	// it; the gate guaranteed it did not pre-exist.
	if out, err := h.docker("volume", "rm", otelDataVolume); err != nil {
		t.Errorf("cleanup: docker volume rm %s: %v\n%s", otelDataVolume, err, out)
	}
}

// postOtelTrace sends one OTLP/HTTP JSON span whose name is a unique marker
// and returns that marker.
func postOtelTrace(t *testing.T, label string) string {
	t.Helper()

	traceID := otelRandomHex(t, 16)
	marker := "forge-f4-" + label + "-" + traceID[:12]
	now := time.Now().UnixNano()
	body := fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"forge-otel-lifecycle-test"}}]},"scopeSpans":[{"scope":{"name":"forge-test"},"spans":[{"traceId":%q,"spanId":%q,"name":%q,"kind":1,"startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]}]}`,
		traceID, otelRandomHex(t, 8), marker, now-int64(time.Millisecond), now)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, otelTracesURL, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build OTLP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", otelTracesURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d", otelTracesURL, resp.StatusCode, http.StatusOK)
	}

	return marker
}

func otelRandomHex(t *testing.T, n int) string {
	t.Helper()

	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	return hex.EncodeToString(buf)
}
