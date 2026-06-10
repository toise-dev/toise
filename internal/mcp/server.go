package mcp

import (
	"context"
	"net/http"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/version"
)

// maxDepth caps relation traversal in get_neighbors. Beyond it the tool returns
// a friendly error inviting a smaller query (ADR 0010, ADR 0011).
const maxDepth = 5

// defaultLimit and maxLimit bound find_entities result sizes.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// Graph is the subset of the in-memory projection the MCP tools read current
// state from (ADR 0008). The concrete *projection.Graph satisfies it.
type Graph interface {
	GetEntity(id model.EntityID) (model.Entity, bool, bool)
	ListEntities(typ string) []model.Entity
	ListRelations(typ string, from, to model.EntityID) []model.Relation
	CountByType() map[string]int
	EntityCount() int
	RelationCount() int
}

// EventReader is the subset of the event log the MCP tools read history from
// (ADR 0007). The concrete *store.Store satisfies it.
type EventReader interface {
	ReadByEntity(id model.EntityID) ([]model.Event, error)
	ReadByTimeRange(start, end time.Time) ([]model.Event, error)
}

// Server exposes Toise's read model as MCP tools over stdio and Streamable HTTP.
type Server struct {
	graph Graph
	store EventReader
	now   func() time.Time
	srv   *mcpsdk.Server
}

// New builds an MCP server reading from the given projection and event log. The
// underlying SDK server is constructed once and reused across transports and
// HTTP sessions; the tools are stateless reads.
func New(graph Graph, store EventReader) *Server {
	s := &Server{graph: graph, store: store, now: time.Now}
	impl := &mcpsdk.Implementation{
		Name:    "toise",
		Title:   "Toise — the living map of your infrastructure",
		Version: version.String(),
	}
	srv := mcpsdk.NewServer(impl, nil)
	s.register(srv)
	s.srv = srv
	return s
}

// HTTPHandler returns an http.Handler serving the MCP server over the Streamable
// HTTP transport, suitable for mounting at a path such as /mcp.
func (s *Server) HTTPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s.srv }, nil)
}

// ServeStdio runs the MCP server over stdio until the context is canceled or
// the client disconnects. This is the transport Claude Desktop drives.
func (s *Server) ServeStdio(ctx context.Context) error {
	return s.srv.Run(ctx, &mcpsdk.StdioTransport{})
}

func (s *Server) register(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "find_entities",
		Description: "Find infrastructure entities matching a filter. Optionally narrow " +
			"by entity type and by attribute key/value pairs (matched against both " +
			"identifying and descriptive attributes). Returns entity summaries with a " +
			"human-readable label, ids, types, and attributes. Use describe_schema first " +
			"if you are unsure which entity types or attribute keys exist.",
	}, s.findEntities)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "get_entity",
		Description: "Fetch a single entity by its logical id, with all of its identifying " +
			"and descriptive attributes. Use the id returned by find_entities, get_neighbors, " +
			"or recent_changes.",
	}, s.getEntity)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "get_neighbors",
		Description: "Traverse the topology outward from an entity, following relations in " +
			"either direction up to a given depth (capped at 5). Optionally filter by relation " +
			"type. Returns the reachable entities, each with the relation type, direction, and " +
			"hop distance that first reached it. Use this to answer questions about what an " +
			"entity is connected to, runs on, or depends on.",
	}, s.getNeighbors)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "find_path",
		Description: "Find the shortest relation path between two entities, traversing edges in " +
			"either direction (optionally only one relation type), up to max_depth hops. " +
			"reachable=false is a first-class answer meaning no path exists within the cap — " +
			"use it to answer 'does A depend on B?' or 'how are these connected?'.",
	}, s.findPath)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "entity_history",
		Description: "Return the timeline of changes for one entity, oldest first. Optionally " +
			"bound it with since/until (RFC 3339, in event-time — when changes became true). " +
			"Set as_known_at for an audit view: only what Toise had recorded by that instant. " +
			"Heartbeats (entity.unchanged) are excluded and the result is bounded by limit " +
			"(newest kept) unless asked otherwise; the digest reports totals per change type. " +
			"Use this to explain how an entity reached its current state.",
	}, s.entityHistory)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "recent_changes",
		Description: "List recent changes across the whole graph within a time window (a Go " +
			"duration such as 15m, 2h, or 24h), newest first. Optionally filter to entity " +
			"changes, relation changes, or only structural changes (alert-worthy topology " +
			"appearances/disappearances), or to one change_type. Heartbeats (entity.unchanged) " +
			"are excluded and the result is bounded by limit unless asked otherwise; the " +
			"digest reports totals per change type. Use this to answer 'what changed recently?'.",
	}, s.recentChanges)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "graph_diff",
		Description: "Fold the change log between two instants into the NET difference: entities " +
			"and relations created, deleted, or changed, plus transient ones that appeared AND " +
			"disappeared within the window (flapping). Intermediate churn and heartbeats are " +
			"collapsed away; totals always cover everything even when the item lists are " +
			"truncated by limit. Give a window (e.g. 24h) or from/to instants (RFC 3339). " +
			"Use this instead of paging recent_changes when you want 'what is different now " +
			"compared to then?'.",
	}, s.graphDiff)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "describe_schema",
		Description: "Describe the entity and relation types currently present in the graph, " +
			"with counts, in natural language. Call this first to bootstrap your understanding " +
			"of what this Toise instance knows about before issuing other tools.",
	}, s.describeSchema)
}
