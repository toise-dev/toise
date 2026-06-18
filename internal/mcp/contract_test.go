package mcp

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// updateGolden regenerates the pinned contract file: `go test ./internal/mcp -run
// ToolContract -update`. Do it only when a tool-surface change is intended.
var updateGolden = flag.Bool("update", false, "rewrite the tool-contract golden")

// toolContract lists every MCP tool with its input and output types — the
// public, LLM-facing API surface. Adding a tool means adding it here (and
// regenerating the golden), which is itself the deliberate-change checkpoint.
var toolContract = []struct {
	name    string
	in, out any
}{
	{"find_entities", FindEntitiesInput{}, FindEntitiesOutput{}},
	{"get_entity", GetEntityInput{}, GetEntityOutput{}},
	{"get_neighbors", GetNeighborsInput{}, GetNeighborsOutput{}},
	{"find_path", FindPathInput{}, FindPathOutput{}},
	{"impact_of", ImpactOfInput{}, ImpactOfOutput{}},
	{"entity_history", EntityHistoryInput{}, EntityHistoryOutput{}},
	{"recent_changes", RecentChangesInput{}, RecentChangesOutput{}},
	{"graph_diff", GraphDiffInput{}, GraphDiffOutput{}},
	{"describe_type", DescribeTypeInput{}, DescribeTypeOutput{}},
	{"telemetry_keys", TelemetryKeysInput{}, TelemetryKeysOutput{}},
	{"describe_schema", DescribeSchemaInput{}, DescribeSchemaOutput{}},
	{"annotate_entity", AnnotateEntityInput{}, AnnotateEntityOutput{}},
}

// TestToolContract pins the MCP tool surface (#166 / 0.7.0 API contract pinning):
// each tool's name and the json field names + kinds of its input and output. A
// rename, removal, retype, or added/dropped field changes the golden and so must
// be deliberate. The SDK derives the wire JSON schema from these same structs, so
// the golden tracks what clients actually see.
func TestToolContract(t *testing.T) {
	var b strings.Builder
	for _, tc := range toolContract {
		fmt.Fprintf(&b, "tool %s\n", tc.name)
		fmt.Fprintf(&b, "  in:\n%s", fieldSig(reflect.TypeOf(tc.in), 2))
		fmt.Fprintf(&b, "  out:\n%s", fieldSig(reflect.TypeOf(tc.out), 2))
	}
	// Resources, resource templates, and prompts are part of the same LLM-facing
	// surface; pin their names, URIs/MIME, and prompt arguments too.
	for _, r := range resourceCatalog {
		fmt.Fprintf(&b, "resource %s %s %s\n", r.Name, r.URI, r.MIMEType)
	}
	for _, t := range resourceTemplates {
		fmt.Fprintf(&b, "resource_template %s %s %s\n", t.Name, t.URITemplate, t.MIMEType)
	}
	for _, p := range promptCatalog {
		fmt.Fprintf(&b, "prompt %s\n", p.prompt.Name)
		args := make([]string, 0, len(p.prompt.Arguments))
		for _, a := range p.prompt.Arguments {
			req := "optional"
			if a.Required {
				req = "required"
			}
			args = append(args, fmt.Sprintf("  arg %s %s", a.Name, req))
		}
		sort.Strings(args)
		for _, line := range args {
			fmt.Fprintln(&b, line)
		}
	}
	got := b.String()

	const golden = "testdata/tool_contract.golden"
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading golden (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("MCP tool contract changed. If intended, regenerate:\n  go test ./internal/mcp -run ToolContract -update\n\n--- got ---\n%s", got)
	}
}

// fieldSig renders a struct type's exported, JSON-serialized fields as sorted
// "name kind" lines, recursing into struct fields and inlining embedded structs,
// so the signature is stable under field reordering but sensitive to any
// add/remove/rename/retype.
func fieldSig(t reflect.Type, indent int) string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return ""
	}
	var lines []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, omit := jsonName(f)
		if omit {
			continue
		}
		if f.Anonymous && name == "" {
			lines = append(lines, strings.TrimRight(fieldSig(f.Type, indent), "\n"))
			continue
		}
		line := strings.Repeat(" ", indent) + name + " " + kindOf(f.Type)
		if nested := fieldSig(f.Type, indent+2); nested != "" {
			line += "\n" + strings.TrimRight(nested, "\n")
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func jsonName(f reflect.StructField) (name string, omit bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	if tag == "" {
		if f.Anonymous {
			return "", false // embedded, inlined
		}
		return f.Name, false
	}
	return strings.Split(tag, ",")[0], false
}

func kindOf(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice {
		return "[]" + kindOf(t.Elem())
	}
	if t.Kind() == reflect.Struct {
		return "object"
	}
	return t.Kind().String()
}
