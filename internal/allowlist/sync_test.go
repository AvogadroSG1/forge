package allowlist

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"forge"
)

func TestDetectReportsStaleManagedBlocks(t *testing.T) {
	t.Parallel()

	status, err := Detect(`"// BEGIN FORGE ALLOW v:0",` + "\n" + `"Bash(old:*)",` + "\n" + `"// END FORGE ALLOW",` + "\n")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !status.Stale {
		t.Fatalf("Detect() stale = false, want true")
	}
}

func TestDetectRejectsMalformedManagedBlockVersions(t *testing.T) {
	t.Parallel()

	_, err := Detect(`"// BEGIN FORGE ALLOW v:nope",` + "\n" + `"// END FORGE ALLOW",` + "\n")
	if err == nil || !strings.Contains(err.Error(), "invalid managed block version") {
		t.Fatalf("Detect() error = %v, want invalid managed block version", err)
	}
}

func TestSyncRewritesOnlyTheManagedBlock(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "settings.local.json")
	original := `{
  "permissions": {
    "allow": [
      "// BEGIN FORGE ALLOW v:0",
      "Bash(old:*)",
      "// END FORGE ALLOW",
      "Bash(true)"
    ]
  },
  "after": true
}
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	status, err := Sync(path, `      "Bash(new:*)",`, false)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if status.CurrentVersion != Version {
		t.Fatalf("Sync() version = %d, want %d", status.CurrentVersion, Version)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"after": true`) {
		t.Fatalf("Sync() rewrote surrounding content:\n%s", text)
	}
	if !strings.Contains(text, `"Bash(new:*)"`) {
		t.Fatalf("Sync() missing replacement block:\n%s", text)
	}
	if strings.Contains(text, `"Bash(old:*)"`) {
		t.Fatalf("Sync() did not remove old block:\n%s", text)
	}
}

func TestInferLanguageUsesOnlyTheManagedBlock(t *testing.T) {
	t.Parallel()

	contents := `{
  "note": "Bash(python:*) belongs in docs only",
  "permissions": {
    "allow": [
      "// BEGIN FORGE ALLOW v:1",
      "Bash(go:*)",
      "// END FORGE ALLOW",
      "Bash(true)"
    ]
  }
}
`

	language, err := InferLanguage(contents)
	if err != nil {
		t.Fatalf("InferLanguage() error = %v", err)
	}
	if language != "go" {
		t.Fatalf("InferLanguage() = %q, want go", language)
	}
}

func TestInferLanguageRejectsMissingManagedBlockLanguageMarkers(t *testing.T) {
	t.Parallel()

	contents := `{
  "permissions": {
    "allow": [
      "// BEGIN FORGE ALLOW v:1",
      "Bash(git status:*)",
      "// END FORGE ALLOW",
      "Bash(true)"
    ]
  }
}
`

	_, err := InferLanguage(contents)
	if err == nil || !strings.Contains(err.Error(), "could not infer project language") {
		t.Fatalf("InferLanguage() error = %v, want missing language marker", err)
	}
}

func TestCanonicalBlockKeepsPersonalRulesOptIn(t *testing.T) {
	t.Parallel()

	defaultBlock, err := CanonicalBlock(forge.Assets(), "go", false, false)
	if err != nil {
		t.Fatalf("CanonicalBlock(default) error = %v", err)
	}
	if strings.Contains(defaultBlock, `"Bash(gw:*)"`) {
		t.Fatalf("CanonicalBlock(default) unexpectedly included personal rules:\n%s", defaultBlock)
	}

	personalBlock, err := CanonicalBlock(forge.Assets(), "go", false, true)
	if err != nil {
		t.Fatalf("CanonicalBlock(include personal) error = %v", err)
	}
	for _, snippet := range []string{
		`"Bash(gw:*)",`,
		`"Bash(slack-cli:*)",`,
		`"Bash(docker images:*)",`,
	} {
		if !strings.Contains(personalBlock, snippet) {
			t.Fatalf("CanonicalBlock(include personal) missing %q in:\n%s", snippet, personalBlock)
		}
	}
}

func TestCanonicalBlockOpenCodeRendersFromJSONCTemplate(t *testing.T) {
	t.Parallel()

	block, err := CanonicalBlockOpenCode(forge.Assets(), "go", false, false)
	if err != nil {
		t.Fatalf("CanonicalBlockOpenCode() error = %v", err)
	}
	if !strings.Contains(block, `"go*": "allow"`) {
		t.Fatalf("CanonicalBlockOpenCode() missing go rule in:\n%s", block)
	}
}

func TestGeneratedOpenCodePermissionsContract(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(forge.Assets(), "templates/common/opencode.jsonc.tmpl")
	if err != nil {
		t.Fatalf("ReadFile(templates/common/opencode.jsonc.tmpl) error = %v", err)
	}

	tmpl, err := template.New("opencode.jsonc.tmpl").Option("missingkey=error").Parse(string(data))
	if err != nil {
		t.Fatalf("template.Parse error = %v", err)
	}

	testCases := []struct {
		name            string
		language        string
		frontend        bool
		includePersonal bool
	}{
		{name: "go-default", language: "go", frontend: false, includePersonal: false},
		{name: "python-frontend", language: "python", frontend: true, includePersonal: false},
		{name: "typescript", language: "typescript", frontend: false, includePersonal: false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			frontendMarker := ""
			if tc.frontend {
				frontendMarker = "frontend"
			}
			var buf bytes.Buffer
			err := tmpl.Execute(&buf, struct {
				Language        string
				Frontend        string
				IncludePersonal bool
			}{
				Language:        tc.language,
				Frontend:        frontendMarker,
				IncludePersonal: tc.includePersonal,
			})
			if err != nil {
				t.Fatalf("tmpl.Execute() error = %v", err)
			}
			rendered := buf.String()

			// Check external_directory position relative to managed allowlist markers
			beginIdx := strings.Index(rendered, "// BEGIN FORGE ALLOW v:2")
			endIdx := strings.Index(rendered, "// END FORGE ALLOW")
			if beginIdx == -1 {
				t.Fatalf("rendered opencode.jsonc missing begin marker // BEGIN FORGE ALLOW v:2")
			}
			if endIdx == -1 || endIdx <= beginIdx {
				t.Fatalf("rendered opencode.jsonc missing or invalid end marker // END FORGE ALLOW")
			}

			extDirIdx := strings.Index(rendered, `"external_directory"`)
			if extDirIdx == -1 {
				t.Errorf("rendered opencode.jsonc missing \"external_directory\" entry")
			} else if extDirIdx >= beginIdx && extDirIdx <= endIdx {
				t.Errorf("\"external_directory\" must be outside managed allowlist block [%d, %d], found at %d", beginIdx, endIdx, extDirIdx)
			}

			// Parse JSONC configuration
			cleaned := stripTrailingCommas(stripJSONCComments(rendered))
			var parsed struct {
				Permission map[string]any `json:"permission"`
			}
			if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
				t.Fatalf("json.Unmarshal error = %v", err)
			}
			if parsed.Permission == nil {
				t.Fatalf("parsed opencode.jsonc missing \"permission\" section")
			}

			// Check external_directory allow entries
			extDirRaw, ok := parsed.Permission["external_directory"]
			if !ok || extDirRaw == nil {
				t.Errorf("permission.external_directory is missing")
			} else {
				extDirMap, ok := extDirRaw.(map[string]any)
				if !ok {
					t.Errorf("permission.external_directory is not an object, got %T", extDirRaw)
				} else {
					expectedAllowEntries := []string{
						"~/ObsidianNotes/**",
						"~/peter_code/scratch_work/**",
						"~/peter_code/ai_support/**",
						"~/.plannotator/plans/**",
					}
					for _, path := range expectedAllowEntries {
						if extDirMap[path] != "allow" {
							t.Errorf("permission.external_directory[%q] = %v, want \"allow\"", path, extDirMap[path])
						}
					}
				}
			}

			// Check permission.write is set to "allow" (or {"*": "allow"})
			writeRaw, ok := parsed.Permission["write"]
			if !ok || writeRaw == nil {
				t.Errorf("permission.write is missing")
			} else {
				switch w := writeRaw.(type) {
				case string:
					if w != "allow" {
						t.Errorf("permission.write = %q, want \"allow\"", w)
					}
				case map[string]any:
					if w["*"] != "allow" {
						t.Errorf("permission.write[\"*\"] = %v, want \"allow\"", w["*"])
					}
				default:
					t.Errorf("permission.write unexpected type %T (%v)", writeRaw, writeRaw)
				}
			}

			// Check permissions read, edit, write, bash, external_directory, grep, and glob evaluate to allow
			evaluatesToAllow := func(name string, val any) bool {
				if val == nil {
					return false
				}
				switch v := val.(type) {
				case string:
					return v == "allow"
				case map[string]any:
					if starVal, ok := v["*"]; ok && starVal == "allow" {
						return true
					}
					for _, entryVal := range v {
						if entryVal == "allow" {
							return true
						}
					}
					return false
				default:
					return false
				}
			}

			requiredPerms := []string{"read", "edit", "write", "bash", "external_directory", "grep", "glob"}
			for _, perm := range requiredPerms {
				if !evaluatesToAllow(perm, parsed.Permission[perm]) {
					t.Errorf("permission %q does not evaluate to allow (got %v)", perm, parsed.Permission[perm])
				}
			}
		})
	}
}

func stripJSONCComments(input string) string {
	var buf strings.Builder
	inString := false
	escaped := false
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inString {
			buf.WriteRune(r)
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == '"' {
				inString = false
			}
			continue
		}
		if r == '"' {
			inString = true
			buf.WriteRune(r)
			continue
		}
		if r == '/' && i+1 < len(runes) && runes[i+1] == '/' {
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			if i < len(runes) {
				buf.WriteRune('\n')
			}
			continue
		}
		buf.WriteRune(r)
	}
	return buf.String()
}

func stripTrailingCommas(input string) string {
	re := regexp.MustCompile(`,\s*([}\]])`)
	return re.ReplaceAllString(input, "$1")
}
