package scaffold

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"forge/internal/project"
)

const webAPIEndpointTestFragment = "templates/fragments/csharp/WeatherForecastEndpointTests.cs.tmpl"

// RenderCSharpService renders a C# application fragment (golden vanilla tree
// plus its .forge-overlay) suitable for insertion into an existing monorepo.
// It excludes repository-level gate/infrastructure files, renames the fixed
// template project files to match vars.CSharpNamespace, makes template policy
// defaults conditional on inherited host properties, and substitutes the
// service-mode Web API endpoint test.
func RenderCSharpService(assets fs.FS, vars project.Variables) ([]File, error) {
	switch vars.Stack {
	case "csharp-cli", "csharp-webapi", "csharp-blazor":
	default:
		return nil, fmt.Errorf("unsupported C# service stack %q (supported: csharp-cli, csharp-webapi, csharp-blazor)", vars.Stack)
	}
	if strings.TrimSpace(vars.CSharpNamespace) == "" {
		return nil, fmt.Errorf("CSharpNamespace is required")
	}

	var files []File
	index := map[string]int{}
	noDir := func(string) error { return nil }
	collect := func(f File) error {
		if i, ok := index[f.Path]; ok {
			files[i] = f
			return nil
		}
		index[f.Path] = len(files)
		files = append(files, f)
		return nil
	}

	stackRoot := path.Join("templates/golden", vars.Stack)
	if err := walkTree(assets, stackRoot, "", vars, skipRootGateFiles, noDir, collect); err != nil {
		return nil, err
	}

	overlayRoot := path.Join(stackRoot, ".forge-overlay")
	if err := walkTree(assets, overlayRoot, "", vars, skipRootGateFiles, noDir, collect); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	if vars.Stack == "csharp-webapi" {
		tmplBytes, err := fs.ReadFile(assets, webAPIEndpointTestFragment)
		if err != nil {
			return nil, fmt.Errorf("read service webapi endpoint test template: %w", err)
		}
		rendered, err := renderTemplate(webAPIEndpointTestFragment, tmplBytes, vars)
		if err != nil {
			return nil, err
		}
		if err := collect(File{
			Path: "tests/Project.Tests/WeatherForecastEndpointTests.cs",
			Data: rendered,
			Mode: 0o644,
		}); err != nil {
			return nil, err
		}
	}

	ns := vars.CSharpNamespace
	remapped := make([]File, 0, len(files))
	for _, f := range files {
		newPath, role := mapCSharpServicePath(f.Path, vars.Stack, ns)
		data := f.Data
		if role != csharpProjectNone {
			rewritten, err := rewriteServiceProjectXML(data, role, vars.Stack, ns)
			if err != nil {
				return nil, fmt.Errorf("rewrite %s: %w", newPath, err)
			}
			data = rewritten
		}
		remapped = append(remapped, File{
			Path: newPath,
			Data: data,
			Mode: f.Mode,
		})
	}

	return remapped, nil
}

type csharpProjectRole int

const (
	csharpProjectNone csharpProjectRole = iota
	csharpProjectApp
	csharpProjectTests
	csharpProjectClient
)

func mapCSharpServicePath(relPath, stack, ns string) (string, csharpProjectRole) {
	switch {
	case relPath == "Project.csproj":
		return ns + ".csproj", csharpProjectApp
	case relPath == "tests/Project.Tests/Project.Tests.csproj":
		return path.Join("tests", ns+".Tests", ns+".Tests.csproj"), csharpProjectTests
	case strings.HasPrefix(relPath, "tests/Project.Tests/"):
		rest := strings.TrimPrefix(relPath, "tests/Project.Tests/")
		return path.Join("tests", ns+".Tests", rest), csharpProjectNone
	case stack == "csharp-blazor" && relPath == "Project.Client/Project.Client.csproj":
		return path.Join(ns+".Client", ns+".Client.csproj"), csharpProjectClient
	case stack == "csharp-blazor" && strings.HasPrefix(relPath, "Project.Client/"):
		rest := strings.TrimPrefix(relPath, "Project.Client/")
		return path.Join(ns+".Client", rest), csharpProjectNone
	default:
		return relPath, csharpProjectNone
	}
}

type xmlNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Content  string
	Children []*xmlNode
}

func parseXMLDocument(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []*xmlNode
	var root *xmlNode

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
			node := &xmlNode{
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

func rewriteServiceProjectXML(data []byte, role csharpProjectRole, stack, ns string) ([]byte, error) {
	root, err := parseXMLDocument(data)
	if err != nil {
		return nil, err
	}
	if root.Name.Local != "Project" {
		return nil, fmt.Errorf("expected <Project> root element, got <%s>", root.Name.Local)
	}

	appProjName := ns + ".csproj"
	clientDir := ns + ".Client"
	clientProjRel := clientDir + "/" + clientDir + ".csproj"

	replacedExclusions := false
	for _, child := range root.Children {
		switch child.Name.Local {
		case "PropertyGroup":
			rewritePropertyGroup(child, role, ns)
		case "ItemGroup":
			if isTemplateExclusionItemGroup(child) {
				if role == csharpProjectApp {
					child.Children = buildServiceExclusionItems(stack, clientDir)
					replacedExclusions = true
				}
				continue
			}
			for _, item := range child.Children {
				if item.Name.Local != "ProjectReference" {
					continue
				}
				for i := range item.Attrs {
					if item.Attrs[i].Name.Local != "Include" {
						continue
					}
					switch item.Attrs[i].Value {
					case "../../Project.csproj":
						item.Attrs[i].Value = "../../" + appProjName
					case "Project.Client/Project.Client.csproj":
						item.Attrs[i].Value = clientProjRel
					}
				}
			}
		}
	}

	if role == csharpProjectApp && !replacedExclusions {
		root.Children = append(root.Children, &xmlNode{
			Name:     xml.Name{Local: "ItemGroup"},
			Children: buildServiceExclusionItems(stack, clientDir),
		})
	}

	if role == csharpProjectTests && stack == "csharp-webapi" {
		appendWebAPITestMetadataNodes(root, appProjName)
	}

	var buf bytes.Buffer
	writeXMLNode(&buf, root, 0)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func rewritePropertyGroup(pg *xmlNode, role csharpProjectRole, ns string) {
	kept := make([]*xmlNode, 0, len(pg.Children)+2)
	hasAssemblyName := false
	hasRootNamespace := false

	for _, prop := range pg.Children {
		switch prop.Name.Local {
		case "NoWarn":
			// Remove template's added NoWarn suppression in service mode so host warning policy wins.
			continue
		case "TargetFramework":
			setXMLAttr(prop, "Condition", "'$(TargetFramework)' == '' and '$(TargetFrameworks)' == ''")
		case "Nullable":
			setXMLAttr(prop, "Condition", "'$(Nullable)' == ''")
		case "ImplicitUsings":
			setXMLAttr(prop, "Condition", "'$(ImplicitUsings)' == ''")
		case "GenerateDocumentationFile":
			setXMLAttr(prop, "Condition", "'$(GenerateDocumentationFile)' == ''")
		case "AssemblyName":
			hasAssemblyName = true
		case "RootNamespace":
			hasRootNamespace = true
		}
		kept = append(kept, prop)
	}

	if role == csharpProjectTests {
		testName := ns + ".Tests"
		if !hasAssemblyName {
			kept = append(kept, &xmlNode{Name: xml.Name{Local: "AssemblyName"}, Content: testName})
		}
		if !hasRootNamespace {
			kept = append(kept, &xmlNode{Name: xml.Name{Local: "RootNamespace"}, Content: testName})
		}
	}

	pg.Children = kept
}

func setXMLAttr(node *xmlNode, name, value string) {
	for i := range node.Attrs {
		if node.Attrs[i].Name.Local == name {
			node.Attrs[i].Value = value
			return
		}
	}
	node.Attrs = append(node.Attrs, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}

func isTemplateExclusionItemGroup(ig *xmlNode) bool {
	if len(ig.Children) == 0 {
		return false
	}
	for _, item := range ig.Children {
		hasRemove := false
		for _, attr := range item.Attrs {
			if attr.Name.Local == "Remove" {
				hasRemove = true
				break
			}
		}
		if !hasRemove {
			return false
		}
	}
	return true
}

func buildServiceExclusionItems(stack, clientDir string) []*xmlNode {
	itemTypes := []string{"Compile", "Content", "None", "EmbeddedResource", "RazorComponent", "RazorGenerate"}
	var items []*xmlNode
	for _, typ := range itemTypes {
		items = append(items, &xmlNode{
			Name:  xml.Name{Local: typ},
			Attrs: []xml.Attr{{Name: xml.Name{Local: "Remove"}, Value: "tests/**"}},
		})
	}
	if stack == "csharp-blazor" {
		for _, typ := range itemTypes {
			items = append(items, &xmlNode{
				Name:  xml.Name{Local: typ},
				Attrs: []xml.Attr{{Name: xml.Name{Local: "Remove"}, Value: clientDir + "/**"}},
			})
		}
	}
	return items
}

func appendWebAPITestMetadataNodes(root *xmlNode, appProjName string) {
	appProjExpr := "$(MSBuildProjectDirectory)/../../" + appProjName

	metadataItems := &xmlNode{
		Name: xml.Name{Local: "ItemGroup"},
		Children: []*xmlNode{
			{
				Name:  xml.Name{Local: "ForgeTestMetadataAttribute"},
				Attrs: []xml.Attr{{Name: xml.Name{Local: "Include"}, Value: "System.Reflection.AssemblyMetadataAttribute"}},
				Children: []*xmlNode{
					{Name: xml.Name{Local: "_Parameter1"}, Content: "ForgeAppProjectPath"},
					{Name: xml.Name{Local: "_Parameter2"}, Content: appProjExpr},
				},
			},
			{
				Name:  xml.Name{Local: "ForgeTestMetadataAttribute"},
				Attrs: []xml.Attr{{Name: xml.Name{Local: "Include"}, Value: "System.Reflection.AssemblyMetadataAttribute"}},
				Children: []*xmlNode{
					{Name: xml.Name{Local: "_Parameter1"}, Content: "ForgeBuildConfiguration"},
					{Name: xml.Name{Local: "_Parameter2"}, Content: "$(Configuration)"},
				},
			},
		},
	}

	computeTarget := &xmlNode{
		Name:  xml.Name{Local: "Target"},
		Attrs: []xml.Attr{{Name: xml.Name{Local: "Name"}, Value: "ForgeComputeTestMetadata"}},
		Children: []*xmlNode{
			{
				Name: xml.Name{Local: "PropertyGroup"},
				Children: []*xmlNode{
					{Name: xml.Name{Local: "_ForgeTestMetadataInputsFile"}, Content: "$(IntermediateOutputPath)ForgeTestMetadata.inputs"},
					{Name: xml.Name{Local: "_ForgeTestMetadataFile"}, Content: "$(IntermediateOutputPath)ForgeTestMetadata.g.cs"},
				},
			},
			{
				Name: xml.Name{Local: "ItemGroup"},
				Children: []*xmlNode{
					{
						Name:  xml.Name{Local: "_ForgeTestMetadataLine"},
						Attrs: []xml.Attr{{Name: xml.Name{Local: "Include"}, Value: "ForgeAppProjectPath=" + appProjExpr}},
					},
					{
						Name:  xml.Name{Local: "_ForgeTestMetadataLine"},
						Attrs: []xml.Attr{{Name: xml.Name{Local: "Include"}, Value: "ForgeBuildConfiguration=$(Configuration)"}},
					},
					{
						Name:  xml.Name{Local: "Compile"},
						Attrs: []xml.Attr{{Name: xml.Name{Local: "Include"}, Value: "$(_ForgeTestMetadataFile)"}},
					},
					{
						Name:  xml.Name{Local: "FileWrites"},
						Attrs: []xml.Attr{{Name: xml.Name{Local: "Include"}, Value: "$(_ForgeTestMetadataInputsFile);$(_ForgeTestMetadataFile)"}},
					},
				},
			},
			{
				Name:  xml.Name{Local: "MakeDir"},
				Attrs: []xml.Attr{{Name: xml.Name{Local: "Directories"}, Value: "$(IntermediateOutputPath)"}},
			},
			{
				Name: xml.Name{Local: "WriteLinesToFile"},
				Attrs: []xml.Attr{
					{Name: xml.Name{Local: "File"}, Value: "$(_ForgeTestMetadataInputsFile)"},
					{Name: xml.Name{Local: "Lines"}, Value: "@(_ForgeTestMetadataLine)"},
					{Name: xml.Name{Local: "Overwrite"}, Value: "true"},
					{Name: xml.Name{Local: "WriteOnlyWhenDifferent"}, Value: "true"},
				},
			},
		},
	}

	generateTarget := &xmlNode{
		Name: xml.Name{Local: "Target"},
		Attrs: []xml.Attr{
			{Name: xml.Name{Local: "Name"}, Value: "ForgeGenerateTestMetadata"},
			{Name: xml.Name{Local: "BeforeTargets"}, Value: "CoreCompile"},
			{Name: xml.Name{Local: "DependsOnTargets"}, Value: "ForgeComputeTestMetadata"},
			{Name: xml.Name{Local: "Inputs"}, Value: "$(_ForgeTestMetadataInputsFile)"},
			{Name: xml.Name{Local: "Outputs"}, Value: "$(_ForgeTestMetadataFile)"},
		},
		Children: []*xmlNode{
			{
				Name: xml.Name{Local: "WriteCodeFragment"},
				Attrs: []xml.Attr{
					{Name: xml.Name{Local: "Language"}, Value: "C#"},
					{Name: xml.Name{Local: "AssemblyAttributes"}, Value: "@(ForgeTestMetadataAttribute)"},
					{Name: xml.Name{Local: "OutputDirectory"}, Value: "$(IntermediateOutputPath)"},
					{Name: xml.Name{Local: "OutputFile"}, Value: "ForgeTestMetadata.g.cs"},
				},
			},
		},
	}

	root.Children = append(root.Children, metadataItems, computeTarget, generateTarget)
}

func writeXMLNode(buf *bytes.Buffer, node *xmlNode, depth int) {
	indent := strings.Repeat("  ", depth)
	buf.WriteString(indent)
	buf.WriteByte('<')
	buf.WriteString(node.Name.Local)
	for _, attr := range node.Attrs {
		buf.WriteByte(' ')
		buf.WriteString(attr.Name.Local)
		buf.WriteString(`="`)
		writeXMLAttrValue(buf, attr.Value)
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
		writeXMLNode(buf, child, depth+1)
		buf.WriteByte('\n')
	}
	buf.WriteString(indent)
	buf.WriteString("</")
	buf.WriteString(node.Name.Local)
	buf.WriteByte('>')
}

func writeXMLAttrValue(buf *bytes.Buffer, value string) {
	for _, r := range value {
		switch r {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		case '"':
			buf.WriteString("&quot;")
		default:
			buf.WriteRune(r)
		}
	}
}
