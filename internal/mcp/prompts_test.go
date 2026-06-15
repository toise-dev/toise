package mcp

import (
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func specByName(t *testing.T, name string) promptSpec {
	t.Helper()
	for _, p := range promptCatalog {
		if p.prompt.Name == name {
			return p
		}
	}
	t.Fatalf("no prompt named %q", name)
	return promptSpec{}
}

func TestPromptRequiredArg(t *testing.T) {
	spec := specByName(t, "investigate_incident")
	if _, err := renderPrompt(spec, map[string]string{}); err == nil {
		t.Error("missing required entity arg must error")
	}
	if _, err := renderPrompt(spec, map[string]string{"entity": "  "}); err == nil {
		t.Error("blank required entity arg must error")
	}
}

func TestPromptBuildsMessage(t *testing.T) {
	spec := specByName(t, "investigate_incident")
	res, err := renderPrompt(spec, map[string]string{"entity": "db-07", "window": "2h"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("want one user message, got %+v", res.Messages)
	}
	text := promptText(res)
	if !strings.Contains(text, "db-07") || !strings.Contains(text, "2h") {
		t.Errorf("message must embed args, got: %s", text)
	}
	if !strings.Contains(text, "impact_of") || !strings.Contains(text, "recent_changes") {
		t.Errorf("incident prompt must steer toward the right tools, got: %s", text)
	}
}

func TestPromptWindowDefault(t *testing.T) {
	spec := specByName(t, "whats_changed")
	res, err := renderPrompt(spec, map[string]string{}) // window optional -> default 1h
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(promptText(res), "1h") {
		t.Error("whats_changed must default the window to 1h")
	}
}

func TestEveryPromptRenders(t *testing.T) {
	for _, spec := range promptCatalog {
		args := map[string]string{}
		for _, a := range spec.prompt.Arguments {
			if a.Required {
				args[a.Name] = "x"
			}
		}
		res, err := renderPrompt(spec, args)
		if err != nil {
			t.Errorf("prompt %q failed to render: %v", spec.prompt.Name, err)
			continue
		}
		if strings.TrimSpace(promptText(res)) == "" {
			t.Errorf("prompt %q produced an empty message", spec.prompt.Name)
		}
	}
}

func promptText(res *mcpsdk.GetPromptResult) string {
	if len(res.Messages) == 0 {
		return ""
	}
	if tc, ok := res.Messages[0].Content.(*mcpsdk.TextContent); ok {
		return tc.Text
	}
	return ""
}
