package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP resources expose Toise context a client can read by URI and pin into the
// conversation, complementing the tools (which answer parametric questions).
// They are read-only and live under the toise:// scheme. The catalogs below are
// the single source of truth: register() binds them and the contract golden
// renders them, so adding one is a deliberate, pinned change (0.7.0).
var resourceCatalog = []*mcpsdk.Resource{
	{
		Name:        "schema",
		Title:       "Graph schema",
		URI:         "toise://schema",
		MIMEType:    "application/json",
		Description: "The entity and relation types currently in the graph, with counts and a natural-language summary — the same data as describe_schema, as a pinnable resource. Read this first to bootstrap.",
	},
	{
		Name:        "guide",
		Title:       "Using Toise over MCP",
		URI:         "toise://guide",
		MIMEType:    "text/markdown",
		Description: "How to query this Toise instance: what it models, its bi-temporal event log, the tool catalog, and a suggested first move.",
	},
}

// resourceTemplates are parameterized resources (RFC 6570 URI templates): a
// client can construct a concrete URI to pin a specific entity.
var resourceTemplates = []*mcpsdk.ResourceTemplate{
	{
		Name:        "entity",
		Title:       "Entity by id",
		URITemplate: "toise://entity/{id}",
		MIMEType:    "application/json",
		Description: "A single entity (identity, descriptive attributes, operator annotations) by its logical id — the same data as get_entity, addressable as a resource so a client can pin it into context.",
	},
}

func (s *Server) registerResources(srv *mcpsdk.Server) {
	for _, r := range resourceCatalog {
		srv.AddResource(r, s.readResource)
	}
	for _, t := range resourceTemplates {
		srv.AddResourceTemplate(t, s.readResource)
	}
}

// readResource dispatches a resources/read by URI. Schema and entity reads reuse
// the tool handlers so a resource never diverges from its tool twin.
func (s *Server) readResource(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	uri := req.Params.URI
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid resource uri %q: %w", uri, err)
	}
	if u.Scheme != "toise" {
		return nil, fmt.Errorf("unknown resource scheme %q (Toise resources use toise://)", u.Scheme)
	}
	switch u.Host {
	case "schema":
		_, out, derr := s.describeSchema(ctx, nil, DescribeSchemaInput{})
		if derr != nil {
			return nil, derr
		}
		return jsonResource(uri, out)
	case "guide":
		return textResource(uri, "text/markdown", guideText), nil
	case "entity":
		id := strings.TrimPrefix(u.Path, "/")
		if id == "" {
			return nil, fmt.Errorf("entity resource needs an id: toise://entity/<id>")
		}
		_, out, gerr := s.getEntity(ctx, nil, GetEntityInput{EntityID: id})
		if gerr != nil {
			return nil, gerr
		}
		return jsonResource(uri, out)
	default:
		return nil, fmt.Errorf("unknown Toise resource %q", uri)
	}
}

func jsonResource(uri string, v any) (*mcpsdk.ReadResourceResult, error) {
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding resource %q: %w", uri, err)
	}
	return textResource(uri, "application/json", string(blob)), nil
}

func textResource(uri, mime, text string) *mcpsdk.ReadResourceResult {
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{URI: uri, MIMEType: mime, Text: text}},
	}
}

const guideText = `# Querying Toise over MCP

Toise is a living map of your infrastructure: an entity/topology graph built
from OpenTelemetry entity events. It tracks **what exists** (hosts, processes,
network interfaces, services, addresses, routes, …), **how things connect**
(runs_on, has_interface, listens_on, …), and **how that changed over time** —
every observation is an event in a bi-temporal log.

## Two clocks

Every change carries an **event time** (when it became true in the world, from
the producer) and a **recorded-at** time (when Toise saw it). Ask "the world as
it was last Tuesday" with ` + "`as_of`" + ` on any reading tool; ask "what Toise
*knew* at an instant" with ` + "`as_known_at`" + ` on entity_history.

## Start here

1. Read the ` + "`toise://schema`" + ` resource (or call ` + "`describe_schema`" + `) to see which
   entity and relation types exist and how many of each.
2. Call ` + "`describe_type`" + ` on a type to learn its observed attributes, relation
   shapes, and (for relations) failure-propagation direction.
3. Find entities with ` + "`find_entities`" + `; fetch one with ` + "`get_entity`" + `; walk the
   topology with ` + "`get_neighbors`" + ` / ` + "`find_path`" + `.
4. Reason about failures with ` + "`impact_of`" + ` (blast radius).
5. Investigate change with ` + "`recent_changes`" + `, ` + "`graph_diff`" + `, and ` + "`entity_history`" + `.
6. Pivot to metrics/logs with ` + "`telemetry_keys`" + ` (the join keys for your
   observability backend).

## Writing

The graph is producer truth and read-only. The one write is ` + "`annotate_entity`" + `:
operator notes (owner, runbook, ticket) kept as an overlay, surfaced back on
` + "`get_entity`" + `, never mixed into producer attributes.

## Traps worth knowing before you trust an answer

These are field-earned: each one has made a competent consumer draw a wrong
conclusion from a correct query (#346).

- **A disappearance never means "a human deleted this."** Deletions carry
  ` + "`delete_source`" + `, written out in the ` + "`disappearance`" + ` field. ` + "`producer`" + ` = the
  producer reported it gone; ` + "`liveness_expiry`" + ` = the producer went silent and the
  thing may still be running; ` + "`cascade`" + ` = something it touched died. Reporting an
  operator action, a rename or a manual removal from a disappearance alone is the
  single most expensive mistake made against this API.
- **A wide window returns its newest slice, not all of it.** To investigate a past
  incident give ` + "`from`" + `/` + "`to`" + ` to ` + "`graph_diff`" + ` or ` + "`recent_changes`" + `; a five-hour ask
  answered under the limit can omit an event two hours back. The payload names the
  window it actually covered — read it before concluding nothing changed.
- **Ids are not portable.** They are per-replica, and an entity that returns after
  more than 15 minutes of silence is minted a new one. Keep identities
  (` + "`host.id`" + `, ` + "`container.id`" + `), re-resolve with ` + "`find_entities`" + `.
- **Topology is traversed, not listed.** An address is a ` + "`network.address`" + ` entity two
  hops from its host, not an attribute on it. Walk with ` + "`get_neighbors`" + ` depth 2
  before deciding a fact is absent.
- **Absence is not evidence of absence.** The graph is exactly as complete as its
  producers; check ` + "`describe_schema`" + ` counts before treating a gap as a fact about
  the world.

Results are structured and name-bearing — ids carry human labels and types — so
one call answers most questions, and the ones that need a second hop say so.
`
