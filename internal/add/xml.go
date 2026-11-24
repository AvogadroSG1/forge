package addcmd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type packagePin struct {
	ID      string
	Version string
}

func detectBOMAndNewline(data []byte) (bom []byte, newline string) {
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		bom = []byte{0xEF, 0xBB, 0xBF}
	}
	if bytes.Contains(data, []byte("\r\n")) {
		newline = "\r\n"
	} else {
		newline = "\n"
	}
	return bom, newline
}

// appendBeforeClosingProject inserts snippet immediately before the final
// </Project> closing tag, preserving all bytes outside the insertion, BOM,
// and CRLF/LF newline conventions.
func appendBeforeClosingProject(original []byte, snippet string) ([]byte, error) {
	_, nl := detectBOMAndNewline(original)

	closingIdx, err := findClosingProjectTag(original)
	if err != nil {
		return nil, err
	}

	// Preserve indentation before </Project> on its line, if any.
	lineStart := closingIdx
	for lineStart > 0 && original[lineStart-1] != '\n' && original[lineStart-1] != '\r' {
		if original[lineStart-1] != ' ' && original[lineStart-1] != '\t' {
			lineStart = closingIdx
			break
		}
		lineStart--
	}

	normalizedSnippet := strings.ReplaceAll(snippet, "\r\n", "\n")
	normalizedSnippet = strings.TrimRight(normalizedSnippet, "\n")
	if nl != "\n" {
		normalizedSnippet = strings.ReplaceAll(normalizedSnippet, "\n", nl)
	}

	prefix := original[:lineStart]
	suffix := original[lineStart:]

	var out bytes.Buffer
	out.Grow(len(original) + len(normalizedSnippet) + 8)
	out.Write(prefix)

	// Ensure a clean blank line separation before the inserted block when
	// prefix already ends with a newline, or add a newline if inline.
	if len(prefix) > 0 {
		switch {
		case bytes.HasSuffix(prefix, []byte(nl+nl)):
			// Already has a blank line before </Project>.
		case bytes.HasSuffix(prefix, []byte(nl)):
			out.WriteString(nl)
		default:
			out.WriteString(nl)
		}
	}

	out.WriteString(normalizedSnippet)
	out.WriteString(nl)
	out.Write(suffix)

	return out.Bytes(), nil
}

// findClosingProjectTag locates the byte offset of the last </Project> tag
// outside XML comments.
func findClosingProjectTag(data []byte) (int, error) {
	lower := bytes.ToLower(data)
	target := []byte("</project>")
	inComment := false
	lastIdx := -1

	for i := 0; i < len(lower); {
		if inComment {
			end := bytes.Index(lower[i:], []byte("-->"))
			if end < 0 {
				break
			}
			i += end + 3
			inComment = false
			continue
		}
		if bytes.HasPrefix(lower[i:], []byte("<!--")) {
			inComment = true
			i += 4
			continue
		}
		if bytes.HasPrefix(lower[i:], target) {
			lastIdx = i
			i += len(target)
			continue
		}
		i++
	}

	if lastIdx < 0 {
		return -1, fmt.Errorf("missing closing </Project> tag")
	}
	return lastIdx, nil
}

func insertServiceIsolationGroup(original []byte, labelServicePath, projRelServicePath string) ([]byte, error) {
	glob := strings.TrimSuffix(filepath.ToSlash(projRelServicePath), "/") + "/**"
	var b strings.Builder
	fmt.Fprintf(&b, "  <ItemGroup Label=\"%s\">\n", escapeXMLAttr("Forge service isolation: "+filepath.ToSlash(labelServicePath)))
	for _, itemType := range []string{"Compile", "Content", "None", "EmbeddedResource", "RazorComponent", "RazorGenerate"} {
		fmt.Fprintf(&b, "    <%s Remove=\"%s\" />\n", itemType, escapeXMLAttr(glob))
	}
	b.WriteString("  </ItemGroup>")
	return appendBeforeClosingProject(original, b.String())
}

func appendCentralPackageVersions(original []byte, missing []packagePin) ([]byte, error) {
	if len(missing) == 0 {
		return original, nil
	}
	sorted := append([]packagePin(nil), missing...)
	sort.Slice(sorted, func(i, j int) bool {
		li, lj := strings.ToLower(sorted[i].ID), strings.ToLower(sorted[j].ID)
		if li != lj {
			return li < lj
		}
		return sorted[i].ID < sorted[j].ID
	})

	var b strings.Builder
	b.WriteString("  <ItemGroup>\n")
	for _, pin := range sorted {
		fmt.Fprintf(&b, "    <PackageVersion Include=\"%s\" Version=\"%s\" />\n", escapeXMLAttr(pin.ID), escapeXMLAttr(pin.Version))
	}
	b.WriteString("  </ItemGroup>")
	return appendBeforeClosingProject(original, b.String())
}

// inspectDeclaredCentralPackages parses a Directory.Packages.props file (and
// any static, in-worktree <Import Project="..." /> files it references) to
// discover all declared PackageVersion IDs, including conditional ones that may
// not be active for the current project evaluation.
func inspectDeclaredCentralPackages(propsPath, worktreeRoot string) (map[string]string, error) {
	declared := map[string]string{}
	visited := map[string]bool{}
	var walk func(filePath string) error
	walk = func(filePath string) error {
		canon, err := canonicalPath(filePath)
		if err != nil {
			return err
		}
		if visited[canon] {
			return nil
		}
		visited[canon] = true

		data, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}

		dec := xml.NewDecoder(bytes.NewReader(data))
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("parse %s: %w", filePath, err)
			}
			start, ok := tok.(xml.StartElement)
			if !ok {
				continue
			}
			switch {
			case strings.EqualFold(start.Name.Local, "PackageVersion"):
				for _, attr := range start.Attr {
					if strings.EqualFold(attr.Name.Local, "Include") || strings.EqualFold(attr.Name.Local, "Update") {
						for _, part := range strings.Split(attr.Value, ";") {
							id := strings.TrimSpace(part)
							if id != "" {
								declared[strings.ToLower(id)] = filePath
							}
						}
					}
				}
			case strings.EqualFold(start.Name.Local, "Import"):
				for _, attr := range start.Attr {
					if !strings.EqualFold(attr.Name.Local, "Project") {
						continue
					}
					imp := strings.TrimSpace(attr.Value)
					if imp == "" || strings.Contains(imp, "$(") {
						continue
					}
					impNorm := strings.ReplaceAll(imp, "\\", "/")
					impAbs := impNorm
					if !filepath.IsAbs(impAbs) {
						impAbs = filepath.Join(filepath.Dir(filePath), filepath.FromSlash(impNorm))
					}
					impAbs = filepath.Clean(impAbs)
					if ok, _ := isWithinRoot(impAbs, worktreeRoot); ok {
						if info, statErr := os.Stat(impAbs); statErr == nil && !info.IsDir() {
							if err := walk(impAbs); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		return nil
	}

	if err := walk(propsPath); err != nil {
		return nil, err
	}
	return declared, nil
}

// extractTemplatePackagePins returns the PackageReference IDs and pinned
// versions declared directly in a rendered service .csproj file.
func extractTemplatePackagePins(csprojData []byte) ([]packagePin, error) {
	root, err := parseSimpleXML(csprojData)
	if err != nil {
		return nil, err
	}
	var pins []packagePin
	for _, child := range root.Children {
		if child.Name.Local != "ItemGroup" {
			continue
		}
		for _, item := range child.Children {
			if item.Name.Local != "PackageReference" {
				continue
			}
			id := getSimpleAttr(item, "Include")
			ver := getSimpleAttr(item, "Version")
			if ver == "" {
				for _, sub := range item.Children {
					if sub.Name.Local == "Version" {
						ver = strings.TrimSpace(sub.Content)
					}
				}
			}
			if id != "" {
				pins = append(pins, packagePin{ID: id, Version: ver})
			}
		}
	}
	return pins, nil
}

// rewriteServiceProjectPackages updates a newly generated service .csproj file:
// - removes PackageReference items whose ID is in removeIDs (lowercased)
// - strips Version attributes/elements when stripVersions is true (CPM mode)
// preserving all other metadata such as PrivateAssets.
func rewriteServiceProjectPackages(csprojData []byte, stripVersions bool, removeIDs map[string]bool) ([]byte, bool, error) {
	if !stripVersions && len(removeIDs) == 0 {
		return csprojData, false, nil
	}

	root, err := parseSimpleXML(csprojData)
	if err != nil {
		return nil, false, err
	}

	changed := false
	keptTop := make([]*simpleXMLNode, 0, len(root.Children))
	for _, child := range root.Children {
		if child.Name.Local != "ItemGroup" {
			keptTop = append(keptTop, child)
			continue
		}
		hasPkgRef := false
		for _, item := range child.Children {
			if item.Name.Local == "PackageReference" {
				hasPkgRef = true
				break
			}
		}
		if !hasPkgRef {
			keptTop = append(keptTop, child)
			continue
		}

		keptItems := make([]*simpleXMLNode, 0, len(child.Children))
		for _, item := range child.Children {
			if item.Name.Local != "PackageReference" {
				keptItems = append(keptItems, item)
				continue
			}
			id := getSimpleAttr(item, "Include")
			if removeIDs[strings.ToLower(id)] {
				changed = true
				continue
			}
			if stripVersions {
				newAttrs := make([]xml.Attr, 0, len(item.Attrs))
				for _, attr := range item.Attrs {
					if strings.EqualFold(attr.Name.Local, "Version") {
						changed = true
						continue
					}
					newAttrs = append(newAttrs, attr)
				}
				item.Attrs = newAttrs

				newSubs := make([]*simpleXMLNode, 0, len(item.Children))
				for _, sub := range item.Children {
					if strings.EqualFold(sub.Name.Local, "Version") {
						changed = true
						continue
					}
					newSubs = append(newSubs, sub)
				}
				item.Children = newSubs
			}
			keptItems = append(keptItems, item)
		}

		if len(keptItems) == 0 {
			changed = true
			continue
		}
		child.Children = keptItems
		keptTop = append(keptTop, child)
	}

	if !changed {
		return csprojData, false, nil
	}

	root.Children = keptTop
	var buf bytes.Buffer
	writeSimpleXML(&buf, root, 0)
	buf.WriteByte('\n')
	return buf.Bytes(), true, nil
}

type simpleXMLNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Content  string
	Children []*simpleXMLNode
}

func parseSimpleXML(data []byte) (*simpleXMLNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []*simpleXMLNode
	var root *simpleXMLNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			node := &simpleXMLNode{
				Name:  t.Name,
				Attrs: append([]xml.Attr(nil), t.Attr...),
			}
			if len(stack) == 0 {
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("unexpected closing tag </%s>", t.Name.Local)
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				trimmed := strings.TrimSpace(string(t))
				if trimmed != "" {
					stack[len(stack)-1].Content += trimmed
				}
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("empty XML document")
	}
	return root, nil
}

func getSimpleAttr(node *simpleXMLNode, name string) string {
	for _, attr := range node.Attrs {
		if strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

func writeSimpleXML(buf *bytes.Buffer, node *simpleXMLNode, depth int) {
	indent := strings.Repeat("  ", depth)
	buf.WriteString(indent)
	buf.WriteByte('<')
	buf.WriteString(node.Name.Local)
	for _, attr := range node.Attrs {
		buf.WriteByte(' ')
		buf.WriteString(attr.Name.Local)
		buf.WriteString(`="`)
		buf.WriteString(escapeXMLAttr(attr.Value))
		buf.WriteByte('"')
	}
	if len(node.Children) == 0 && node.Content == "" {
		buf.WriteString(" />")
		return
	}
	buf.WriteByte('>')
	if len(node.Children) == 0 {
		_ = xml.EscapeText(buf, []byte(node.Content))
		buf.WriteString("</")
		buf.WriteString(node.Name.Local)
		buf.WriteByte('>')
		return
	}
	buf.WriteByte('\n')
	for i, child := range node.Children {
		if depth == 0 && i > 0 {
			buf.WriteByte('\n')
		}
		writeSimpleXML(buf, child, depth+1)
		buf.WriteByte('\n')
	}
	buf.WriteString(indent)
	buf.WriteString("</")
	buf.WriteString(node.Name.Local)
	buf.WriteByte('>')
}

func escapeXMLAttr(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
