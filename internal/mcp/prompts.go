package mcp

import (
	"context"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP prompts are reusable, user-invocable templates that seed a conversation
// with a well-shaped operator task — they name the tools the assistant should
// reach for, so an analyst gets a good investigation without knowing the tool
// catalog. Each builds one user message from its arguments. The catalog is the
// single source of truth: register() binds it and the contract golden renders
// it (0.7.0).
type promptSpec struct {
	prompt *mcpsdk.Prompt
	build  func(args map[string]string) string
}

func arg(name, desc string, required bool) *mcpsdk.PromptArgument {
	return &mcpsdk.PromptArgument{Name: name, Description: desc, Required: required}
}

var promptCatalog = []promptSpec{
	{
		prompt: &mcpsdk.Prompt{
			Name:        "investigate_incident",
			Title:       "Investigate an incident",
			Description: "Triage an entity suspected in an incident: its current state, what changed recently, and what it could take down.",
			Arguments: []*mcpsdk.PromptArgument{
				arg("entity", "the entity to investigate — a logical id, or a name/attribute to find it by", true),
				arg("window", "how far back to look for changes (a Go duration like 30m, 2h, 24h); defaults to 1h", false),
			},
		},
		build: func(a map[string]string) string {
			window := orDefault(a["window"], "1h")
			return fmt.Sprintf(`Investigate %q as a suspected incident cause. Work the graph step by step:
1. Identify the entity: if it is not already a logical id, use find_entities to locate it, then get_entity for its current state and attributes.
2. Look at what changed in the last %s with recent_changes, and the entity's own timeline with entity_history — call out state flips, deletions, and new or dropped relations.
3. Map the blast radius with impact_of: what does this entity take down if it fails?
4. Pivot to telemetry_keys so the metrics and logs for this entity can be pulled from observability backends.
Summarize the most likely story: what changed, what it affects, and what to check next.`, target(a), window)
		},
	},
	{
		prompt: &mcpsdk.Prompt{
			Name:        "blast_radius",
			Title:       "Blast radius of a failure",
			Description: "Explain everything a (hypothetical) failure of one entity would take down, and how.",
			Arguments: []*mcpsdk.PromptArgument{
				arg("entity", "the entity to fail — a logical id, or a name/attribute to find it by", true),
			},
		},
		build: func(a map[string]string) string {
			return fmt.Sprintf(`Explain the blast radius if %q fails.
1. Resolve the entity (find_entities if needed, then get_entity).
2. Call impact_of to get everything it takes down, grouped by type, nearest first.
3. For the most important impacted entities, use get_neighbors to explain the dependency path that propagates the failure.
Present it as: direct dependents first, then second-order effects, and note any single points of failure.`, target(a))
		},
	},
	{
		prompt: &mcpsdk.Prompt{
			Name:        "explain_entity",
			Title:       "Explain an entity",
			Description: "Give a full picture of one entity: what it is, how it connects, how it got here, and where its telemetry lives.",
			Arguments: []*mcpsdk.PromptArgument{
				arg("entity", "the entity to explain — a logical id, or a name/attribute to find it by", true),
			},
		},
		build: func(a map[string]string) string {
			return fmt.Sprintf(`Give a complete picture of %q.
1. Resolve and fetch it with find_entities / get_entity (include its operator annotations, if any).
2. Use describe_type on its type to frame what attributes and relations are normal for it.
3. Walk one hop with get_neighbors to show what it runs on, hosts, or connects to.
4. Summarize its recent history with entity_history.
5. List its telemetry_keys so its metrics and logs can be found.
Write a concise briefing an on-call engineer could read in under a minute.`, target(a))
		},
	},
	{
		prompt: &mcpsdk.Prompt{
			Name:        "whats_changed",
			Title:       "What changed recently",
			Description: "Triage recent change across the whole graph within a window.",
			Arguments: []*mcpsdk.PromptArgument{
				arg("window", "how far back to look (a Go duration like 15m, 1h, 24h); defaults to 1h", false),
			},
		},
		build: func(a map[string]string) string {
			window := orDefault(a["window"], "1h")
			return fmt.Sprintf(`Triage what changed across the infrastructure in the last %s.
1. Call recent_changes for the window; lead with structural relation changes (appearances/disappearances) and entity state flips.
2. Use graph_diff over the same window to collapse churn into the net difference and surface transient (flapping) entities and relations.
3. For anything notable, drill in with get_entity / entity_history.
Summarize: what appeared, what disappeared, what flipped state, and what is flapping — newest and most significant first.`, window)
		},
	},
}

// target renders the entity argument for embedding in a prompt.
func target(a map[string]string) string { return strings.TrimSpace(a["entity"]) }

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// renderPrompt validates required arguments and builds the templated message.
func renderPrompt(spec promptSpec, args map[string]string) (*mcpsdk.GetPromptResult, error) {
	for _, pa := range spec.prompt.Arguments {
		if pa.Required && strings.TrimSpace(args[pa.Name]) == "" {
			return nil, fmt.Errorf("prompt %q requires the %q argument", spec.prompt.Name, pa.Name)
		}
	}
	return &mcpsdk.GetPromptResult{
		Description: spec.prompt.Description,
		Messages: []*mcpsdk.PromptMessage{{
			Role:    "user",
			Content: &mcpsdk.TextContent{Text: spec.build(args)},
		}},
	}, nil
}

func (s *Server) registerPrompts(srv *mcpsdk.Server) {
	for i := range promptCatalog {
		spec := promptCatalog[i]
		srv.AddPrompt(spec.prompt, func(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
			args := map[string]string{}
			if req != nil && req.Params != nil {
				args = req.Params.Arguments
			}
			return renderPrompt(spec, args)
		})
	}
}
