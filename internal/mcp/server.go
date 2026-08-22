package mcp

import (
	"context"
	"net/http"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/annotations"
	"github.com/toise-dev/toise/internal/audit"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/store"
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
	RelationsTouching(id model.EntityID, relType string) []model.Relation
	CountByType() map[string]int
	EntityCount() int
	RelationCount() int
}

// EventReader is the subset of the event log the MCP tools read history from
// (ADR 0007). The concrete *store.Store satisfies it. Reads honor context
// cancellation, so the per-tool timeout actually stops a runaway scan.
type EventReader interface {
	ReadByEntity(ctx context.Context, id model.EntityID) ([]model.Event, error)
	ReadByTimeRange(ctx context.Context, start, end time.Time) ([]model.Event, error)
	// ScanByTimeRange streams the range to fn (no intermediate slice), backing
	// the as-of fold (projection.At).
	ScanByTimeRange(ctx context.Context, start, end time.Time, fn func(model.Event) error) error
	// ScanTimeIndex walks the window's index entries without resolving primary
	// records, so a filter can skip an event without paying its read and decode;
	// Resolve fetches the ones a caller keeps (#351).
	ScanTimeIndex(ctx context.Context, start, end time.Time, newestFirst bool, fn func(store.TimeIndexEntry) error) error
	Resolve(seq uint64) (model.Event, bool, error)
	// NewestEventTime dates the newest event in the log — the freshness every
	// read answer declares in its provenance block (#346).
	NewestEventTime() (time.Time, bool, error)
	// PruneHorizon is the latest retention cutoff ever applied (zero = never
	// pruned): the oldest instant an as-of read can answer completely.
	PruneHorizon() time.Time
}

// toolTimeout bounds every tool call. The Streamable HTTP GET listening stream
// is NOT under this budget — only tool work is (the WS-upgrade lesson from #54:
// never wrap a long-lived stream in a request timeout).
const toolTimeout = 30 * time.Second

// withTimeout wraps a tool handler with the per-call deadline.
func withTimeout[I, O any](timeout func() time.Duration, fn func(context.Context, *mcpsdk.CallToolRequest, I) (*mcpsdk.CallToolResult, O, error)) func(context.Context, *mcpsdk.CallToolRequest, I) (*mcpsdk.CallToolResult, O, error) {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in I) (*mcpsdk.CallToolResult, O, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout())
		defer cancel()
		return fn(ctx, req, in)
	}
}

// Observer records one finished tool call: the tool name, "ok" or "error", and
// the wall-clock duration. It is the query-side observability seam (#166) — the
// metrics collector implements it, and the future query audit log can hang off
// the same hook. nil disables recording.
type Observer interface {
	ObserveTool(tool, outcome string, dur time.Duration)
}

// observe wraps a tool handler with the per-call deadline and, when an Observer
// is set, records the call's name, outcome and wall-clock duration. s.obs is
// read at call time, so SetObserver after New takes effect.
func observe[I, O any](s *Server, tool string, fn func(context.Context, *mcpsdk.CallToolRequest, I) (*mcpsdk.CallToolResult, O, error)) func(context.Context, *mcpsdk.CallToolRequest, I) (*mcpsdk.CallToolResult, O, error) {
	timed := withTimeout(s.budget, fn)
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in I) (*mcpsdk.CallToolResult, O, error) {
		start := time.Now()
		res, out, err := timed(ctx, req, in)
		if s.obs != nil {
			outcome := "ok"
			if err != nil {
				outcome = "error"
			}
			s.obs.ObserveTool(tool, outcome, time.Since(start))
		}
		return res, out, err
	}
}

// Server exposes Toise's read model as MCP tools over stdio and Streamable HTTP.
type Server struct {
	graph   Graph
	store   EventReader
	now     func() time.Time
	timeout time.Duration // per-tool-call budget
	srv     *mcpsdk.Server
	obs     Observer
	ann     *annotations.Store // per-tenant annotation sidecar; nil disables annotate_entity
	audit   *audit.Auditor     // nil/disabled = no audit records (ADR 0028)
	idThr   float64            // same_as confidence threshold for the canonical view (ADR 0020 Lot B)
}

// defaultIdentityThreshold is the same_as confidence at or above which an alias
// belief joins an entity's canonical group — the high band (serial/engine-id/KVP)
// per ADR 0020. Override with SetIdentityThreshold.
const defaultIdentityThreshold = 0.9

// SetAnnotations attaches the per-tenant annotation sidecar, enabling
// annotate_entity and the annotations block on get_entity; returns s for
// chaining. nil leaves annotations disabled.
func (s *Server) SetAnnotations(a *annotations.Store) *Server {
	s.ann = a
	return s
}

// SetAuditor attaches the audit sink for operator writes (ADR 0028); returns s
// for chaining. nil (the default) records nothing.
func (s *Server) SetAuditor(a *audit.Auditor) *Server {
	s.audit = a
	return s
}

// SetObserver attaches a query-observability recorder; returns s for chaining.
// Set it before serving (the per-tool wrappers read it at call time).
func (s *Server) SetObserver(o Observer) *Server {
	s.obs = o
	return s
}

// SetIdentityThreshold sets the same_as confidence at or above which an alias
// joins an entity's canonical group (ADR 0020 Lot B); returns s for chaining. A
// value outside (0,1] falls back to the default.
func (s *Server) SetIdentityThreshold(t float64) *Server {
	if t > 0 && t <= 1 {
		s.idThr = t
	}
	return s
}

// serverInstructions is delivered to every client at initialize. It is the only
// guidance channel an autonomous agent receives without asking: resources must
// be fetched deliberately and prompts are user-invocable, so both are invisible
// to one. Leaving it empty was measurable — two operator agents worked a full
// day of infrastructure incidents without opening Toise once, and a third read
// disappearances as human deletions (#346). What goes here is therefore only
// what changes an answer: the traps that make a correct question return a wrong
// impression.
const serverInstructions = `Toise is a living map of infrastructure: what exists, how it connects, and how that changed — built from what producers observe, as an append-only event log you can read at any past instant.

START by calling describe_schema: it names the entity and relation types this instance actually holds, with counts. Guessing type or attribute names is the most common way to conclude "Toise does not know that" about something it holds.

INVESTIGATING AN INCIDENT — the highest-value thing here, and the least obvious:
Call graph_diff with from/to bounding the window (e.g. the fifteen minutes before an alert). It answers ACROSS THE WHOLE FLEET in one call, which is what makes it worth reaching for: a per-host log tells you a machine changed, this tells you that eleven machines changed the same way in the same window — a pattern no per-host query can show. recent_changes also accepts from/to for the same purpose.
Do NOT investigate a past incident by asking recent_changes for a wide window: the limit keeps the NEWEST changes, so an event two hours back is absent from a "5h" answer. When that happens the payload says so in "covered" — read it before concluding nothing happened.

READING A DISAPPEARANCE — the trap that has produced confidently wrong conclusions:
Deletions carry delete_source, glossed in plain language in the "disappearance" field. NONE of its values means a human deleted anything. producer = the producer reported it gone. liveness_expiry = the producer went silent, and the thing may still be running. cascade = something it touched died. Never report an operator action, a rename, or a manual removal from a disappearance alone.

IDENTIFIERS: entity ids are per-replica and are re-minted if an entity comes back after more than 15 minutes of silence. Never carry an id between investigations — keep the identity (host.id, container.id, service.instance.id) and re-resolve it with find_entities.

TOPOLOGY IS TRAVERSED, NOT LISTED: an address is not an attribute of a host — it is a network.address entity two hops away (host -has_interface-> network.interface <-bound_to- network.address). Use get_neighbors with depth 2 rather than concluding the address is missing. Same shape for anything that can be multiple and mutable.

COVERAGE AND FRESHNESS: every answer carries a "graph" block — how many entities and relations the answering graph holds, the newest event in its log, and the oldest instant it can still answer. Read it before trusting an answer, and especially before trusting an EMPTY one: this graph is exactly as complete as its producers, absence is not evidence of absence, and a stale newest_event means producers stopped talking — which is itself a finding.

Every reading tool takes as_of (RFC 3339) to read the graph as it was at that instant. Writes are limited to annotate_entity: operator notes kept as an overlay, never mixed into producer truth.`

// New builds an MCP server reading from the given projection and event log. The
// underlying SDK server is constructed once and reused across transports and
// HTTP sessions; the tools are stateless reads.
func New(graph Graph, events EventReader) *Server {
	s := &Server{graph: graph, store: events, now: time.Now, timeout: toolTimeout, idThr: defaultIdentityThreshold}
	impl := &mcpsdk.Implementation{
		Name:    "toise",
		Title:   "Toise — the living map of your infrastructure",
		Version: version.String(),
	}
	srv := mcpsdk.NewServer(impl, &mcpsdk.ServerOptions{Instructions: serverInstructions})
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

// budget returns the per-call deadline (a method so tests can shrink it).
func (s *Server) budget() time.Duration { return s.timeout }

func (s *Server) register(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "find_entities",
		Description: "Find infrastructure entities matching a filter. Optionally narrow " +
			"by entity type and by attribute key/value pairs (matched against both " +
			"identifying and descriptive attributes). Returns entity summaries with a " +
			"human-readable label, ids, types, and attributes. Use describe_schema first " +
			"if you are unsure which entity types or attribute keys exist. Set as_of " +
			"(RFC 3339) to query the graph as it was at that instant.",
	}, observe(s, "find_entities", s.findEntities))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "get_entity",
		Description: "Fetch a single entity by its logical id, with all of its identifying " +
			"and descriptive attributes. Use the id returned by find_entities, get_neighbors, " +
			"or recent_changes. Set as_of (RFC 3339) to read it as it was at that instant.",
	}, observe(s, "get_entity", s.getEntity))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "get_neighbors",
		Description: "Traverse the topology outward from an entity, following relations in " +
			"either direction up to a given depth (capped at 5). Optionally filter by relation " +
			"type. Returns the reachable entities, each with the relation type, direction, and " +
			"hop distance that first reached it. Use this to answer questions about what an " +
			"entity is connected to, runs on, or depends on. Set as_of (RFC 3339) to traverse " +
			"the graph as it was at that instant.",
	}, observe(s, "get_neighbors", s.getNeighbors))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "find_path",
		Description: "Find the shortest relation path between two entities, traversing edges in " +
			"either direction (optionally only one relation type), up to max_depth hops. " +
			"reachable=false is a first-class answer meaning no path exists within the cap — " +
			"use it to answer 'does A depend on B?' or 'how are these connected?'. Set as_of " +
			"(RFC 3339) to search the graph as it was at that instant.",
	}, observe(s, "find_path", s.findPath))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "impact_of",
		Description: "Propagate a (hypothetical) failure of one entity through the graph and " +
			"return everything it takes down — the blast radius, nearest first, grouped by " +
			"type. Propagation follows each relation type's dependency direction: a host " +
			"failing takes down what runs_on it and its interfaces, connectivity breaks both " +
			"ways. Use it to answer 'if X goes down, what is affected?'. Set as_of (RFC 3339) " +
			"to ask it of the graph as it was at that instant.",
	}, observe(s, "impact_of", s.impactOf))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "entity_history",
		Description: "Return the timeline of changes for one entity, oldest first. Optionally " +
			"bound it with since/until (RFC 3339, in event-time — when changes became true). " +
			"Set as_known_at for an audit view: only what Toise had recorded by that instant. " +
			"Heartbeats (entity.unchanged) are excluded and the result is bounded by limit " +
			"(newest kept) unless asked otherwise; the digest reports totals per change type. " +
			"Use this to explain how an entity reached its current state.",
	}, observe(s, "entity_history", s.entityHistory))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "recent_changes",
		Description: "List changes across the whole graph — the entire fleet, not one host — within a time window (a Go " +
			"duration such as 15m, 2h, or 24h), newest first. Optionally filter to entity " +
			"changes, relation changes, or only structural changes (alert-worthy topology " +
			"appearances/disappearances), or to one change_type. Heartbeats (entity.unchanged) " +
			"are excluded and the result is bounded by limit unless asked otherwise; the " +
			"digest reports totals per change type. Use this to answer 'what changed recently?' — " +
			"and give from/to instead of window to investigate a PAST window, such as the minutes " +
			"before an alert. A wide window plus the limit keeps only the NEWEST changes, so an " +
			"older event can be missing from a window that claims to cover it; when that happens " +
			"'covered' says which slice came back. Every answer names the window actually read.",
	}, observe(s, "recent_changes", s.recentChanges))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "graph_diff",
		Description: "Fold the change log between two instants into the NET difference: entities " +
			"and relations created, deleted, or changed, plus transient ones that appeared AND " +
			"disappeared within the window (flapping). Intermediate churn and heartbeats are " +
			"collapsed away; totals always cover everything even when the item lists are " +
			"truncated by limit. Give a window (e.g. 24h) or from/to instants (RFC 3339). " +
			"Use this instead of paging recent_changes when you want 'what is different now " +
			"compared to then?'.",
	}, observe(s, "graph_diff", s.graphDiff))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "describe_type",
		Description: "Zoom on one entity or relation type: its registration, live count, the " +
			"identifying and descriptive attribute keys actually observed (with example " +
			"values), how it connects (relation types, directions, and peer types seen " +
			"empirically), example labels — or, for a relation type, its observed " +
			"endpoint-type shapes, structural flag, and failure-propagation direction. Use it " +
			"after describe_schema to learn what a type looks like HERE before querying it.",
	}, observe(s, "describe_type", s.describeType))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "telemetry_keys",
		Description: "Derive the join keys that locate an entity's metrics and logs in telemetry " +
			"backends: the OTel attributes on the entity itself plus those inherited from the " +
			"entity that OWNS it (a listener gains its host's host.id via runs_on). Observation " +
			"and peer relations are not followed: what monitors an entity, or what it depends " +
			"on, describes something else. Each key comes with its Prometheus-style flattened " +
			"label form and usage caveats (ephemeral pids, name-vs-identity, per-datapoint " +
			"identities of remote targets). An empty result means no key exists, not that none " +
			"was found. Use this to pivot from the graph to observability data.",
	}, observe(s, "telemetry_keys", s.telemetryKeys))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "describe_schema",
		Description: "Describe the entity and relation types currently present in the graph, " +
			"with counts, in natural language. Call this first to bootstrap your understanding " +
			"of what this Toise instance knows about before issuing other tools. Set as_of " +
			"(RFC 3339) to describe the graph as it was at that instant.",
	}, observe(s, "describe_schema", s.describeSchema))

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "annotate_entity",
		Description: "Attach operator annotations (free-form key/value notes — owner, runbook " +
			"link, ticket, a remark) to an entity. These are an OVERLAY, not producer truth: " +
			"stored separately and surfaced on get_entity, never mixed into the entity's " +
			"identifying or descriptive attributes. Merges onto existing annotations; an empty " +
			"value removes a key. Requires a write-capable (full or tenant-scoped) token.",
	}, observe(s, "annotate_entity", s.annotateEntity))

	s.registerResources(srv)
	s.registerPrompts(srv)
}
