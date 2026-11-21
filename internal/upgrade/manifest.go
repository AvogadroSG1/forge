package upgrade

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// manifestSchemaVersion is the schema of .forge/manifest.json itself, distinct
// from InfraVersion (the embedded template version the repo was last stamped
// with). Bump this only if the manifest's field set changes shape.
const manifestSchemaVersion = 1

const manifestPath = ".forge/manifest.json"

// Manifest is the committed record of what `forge init` scaffolded a repo
// with. It lets `forge upgrade` re-render templated files (defect E) instead
// of blind-copying, and lets version checks avoid the bare-integer legacy
// marker's inability to carry init parameters (defect A).
//
// No timestamps: the manifest must be deterministic so repeated init/upgrade
// runs with identical params produce byte-identical output.
type Manifest struct {
	SchemaVersion   int    `json:"schemaVersion"`
	InfraVersion    int    `json:"infraVersion"`
	Language        string `json:"language"`
	Frontend        string `json:"frontend"`
	IncludePersonal bool   `json:"includePersonal"`
	Stack           string `json:"stack"`
	// Agent is the opt-in agent add-on the repo was scaffolded with. It is
	// omitted when empty so manifests of repos without an add-on stay
	// byte-identical to those written before the field existed.
	Agent string `json:"agent,omitempty"`
}

// ReadManifest reads .forge/manifest.json from targetDir.
func ReadManifest(targetDir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(targetDir, filepath.FromSlash(manifestPath)))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	return m, nil
}

// Stamp writes both the committed manifest and the legacy bare-integer
// marker, forcing SchemaVersion to manifestSchemaVersion and InfraVersion to
// the current Version regardless of what the caller passed in m. Params
// (Language, Frontend, IncludePersonal, Stack, Agent) are carried through as given.
//
// Stamp MUST be called last, after all other managed files are written, so
// an interrupted init or upgrade re-applies on retry.
func Stamp(targetDir string, m Manifest) error {
	m.SchemaVersion = manifestSchemaVersion
	m.InfraVersion = Version

	dir := filepath.Join(targetDir, ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create .forge directory: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", manifestPath, err)
	}

	versionContent := fmt.Sprintf("%d\n", Version)
	if err := os.WriteFile(filepath.Join(targetDir, versionFile), []byte(versionContent), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", versionFile, err)
	}

	return nil
}
