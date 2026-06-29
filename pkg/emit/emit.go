// Package emit is the producer SDK for Toise: it builds and exports
// spec-correct OTel entity events (the merged entity-events convention —
// EventName entity.state / entity.delete, entity.type, entity.id map,
// entity.description, entity.report.interval, embedded entity.relationships)
// over OTLP/gRPC, so a producer never hand-rolls the wire contract.
//
// Determinism: attribute maps are written in sorted key order, so the same
// input always produces the same bytes — the conformance fixture pins that
// byte stream as the published contract (see the conformance package).
package emit

import (
	"context"
	"crypto/tls"
	"fmt"
	"sort"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// Entity is one entity observation to emit.
type Entity struct {
	// Type is the entity type, e.g. "host" or "service.listener".
	Type string
	// ID is the identifying attribute set. Exact-match identity: every key and
	// value counts (ADR 0018 on the consumer side). Values are strings by
	// deliberate choice — matching is byte-exact over strings, so a port is the
	// string "443", not an int; typed identity values would only invite
	// hash-mismatch ambiguity for no gain.
	ID map[string]string
	// Attributes are descriptive (non-identifying) attributes.
	Attributes map[string]string
	// Interval, when > 0, arms the consumer's liveness backstop: re-assert the
	// entity at least this often or it is expired. Size it with slack for
	// jitter and a missed heartbeat.
	Interval time.Duration
	// Relationships this entity asserts (removal is by absence on re-emit).
	Relationships []Relationship
	// DeleteReason is an optional motive attached to a Delete (entity.delete.reason),
	// e.g. "terminated", "expired", "evicted", "scaled_down". It is an open enum —
	// any string is valid — and is emitted only on Delete; it is ignored by State.
	DeleteReason string
}

// Relationship is an embedded relationship descriptor: the source is the
// entity carrying it.
type Relationship struct {
	Type       string
	TargetType string
	TargetID   map[string]string
}

// Options configure a Client.
type Options struct {
	// Endpoint is the OTLP/gRPC host:port.
	Endpoint string
	// TLS enables transport security when non-nil; nil dials insecurely
	// (loopback / trusted-network posture).
	TLS *tls.Config
	// Headers are sent as gRPC metadata on every export (e.g. "authorization":
	// "Bearer …", "x-scope-orgid": tenant). With TLS nil they travel in clear
	// text — only send a bearer token over TLS or a trusted network.
	Headers map[string]string
	// ServiceName and ServiceInstanceID identify this producer on the OTLP
	// Resource. The instance id is the liveness reference key on the consumer
	// (ADR 0019): set it stable per producer instance.
	ServiceName       string
	ServiceInstanceID string
	// Resource adds extra resource attributes (host.id and friends — the
	// telemetry join keys Toise pivots on).
	Resource map[string]string

	// now overrides the timestamp clock (tests, fixtures).
	now func() time.Time
}

// WithClock returns o with the timestamp clock overridden — exported for
// fixture builders and tests that need deterministic timestamps.
func (o Options) WithClock(now func() time.Time) Options {
	o.now = now
	return o
}

// PartialError reports an OTLP partial success: the server accepted the export
// as a whole (no retry is due), but rejected Rejected records as permanent
// contract violations. Message carries the server's first rejection reason.
// Detect it with errors.As to distinguish partial acceptance from a transport
// failure.
type PartialError struct {
	Rejected int64
	Message  string
}

func (e PartialError) Error() string {
	return fmt.Sprintf("emit: server rejected %d record(s): %s", e.Rejected, e.Message)
}

// Client emits entity events to one endpoint. Safe for concurrent use.
type Client struct {
	opts Options
	conn *grpc.ClientConn
	grpc plogotlp.GRPCClient
	now  func() time.Time
}

// New dials the endpoint and returns a ready Client.
func New(opts Options) (*Client, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("emit: an Endpoint is required")
	}
	cred := insecure.NewCredentials()
	if opts.TLS != nil {
		cred = credentials.NewTLS(opts.TLS)
	}
	conn, err := grpc.NewClient(opts.Endpoint, grpc.WithTransportCredentials(cred))
	if err != nil {
		return nil, fmt.Errorf("emit: dialing %s: %w", opts.Endpoint, err)
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	return &Client{opts: opts, conn: conn, grpc: plogotlp.NewGRPCClient(conn), now: now}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// State emits one entity.state event per entity, in one OTLP export (one
// durable append on the Toise side). A PartialError means the export was
// accepted but some records were rejected as contract violations — do not
// retry it; fix the producer.
func (c *Client) State(ctx context.Context, entities ...Entity) error {
	return c.export(ctx, wire.EventEntityState, entities)
}

// Delete emits one entity.delete event per entity. Toise releases this
// producer's liveness reference; the entity is deleted when the last
// reference goes (ADR 0019). A PartialError means the export was accepted
// but some records were rejected as contract violations — do not retry it;
// fix the producer.
func (c *Client) Delete(ctx context.Context, entities ...Entity) error {
	return c.export(ctx, wire.EventEntityDelete, entities)
}

func (c *Client) export(ctx context.Context, eventName string, entities []Entity) error {
	if len(entities) == 0 {
		return nil
	}
	ld, err := c.Build(eventName, entities)
	if err != nil {
		return err
	}
	for k, v := range c.opts.Headers {
		ctx = metadata.AppendToOutgoingContext(ctx, k, v)
	}
	resp, err := c.grpc.Export(ctx, plogotlp.NewExportRequestFromLogs(ld))
	if err != nil {
		return fmt.Errorf("emit: exporting %d %s events: %w", len(entities), eventName, err)
	}
	// Toise reports per-record contract violations via OTLP partial success:
	// the export succeeds at the transport, the rejection rides in the
	// response. Dropping it would turn rejected records into silent data loss.
	if ps := resp.PartialSuccess(); ps.RejectedLogRecords() > 0 {
		return PartialError{Rejected: ps.RejectedLogRecords(), Message: ps.ErrorMessage()}
	}
	return nil
}

// Build constructs the wire payload without sending it — the conformance kit
// and tests pin its exact byte form.
func (c *Client) Build(eventName string, entities []Entity) (plog.Logs, error) {
	if eventName != wire.EventEntityState && eventName != wire.EventEntityDelete {
		return plog.Logs{}, fmt.Errorf("emit: unknown event name %q (want %q or %q)", eventName, wire.EventEntityState, wire.EventEntityDelete)
	}
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	res := rl.Resource().Attributes()
	if c.opts.ServiceName != "" {
		res.PutStr(wire.ResServiceName, c.opts.ServiceName)
	}
	if c.opts.ServiceInstanceID != "" {
		res.PutStr(wire.ResServiceInstanceID, c.opts.ServiceInstanceID)
	}
	putSorted(res, c.opts.Resource)
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("toise-emit")

	when := pcommon.NewTimestampFromTime(c.now())
	for i := range entities {
		e := &entities[i]
		if e.Type == "" {
			return plog.Logs{}, fmt.Errorf("emit: entity %d has no Type", i)
		}
		if len(e.ID) == 0 {
			return plog.Logs{}, fmt.Errorf("emit: entity %d (%s) has an empty ID — identity is required", i, e.Type)
		}
		if e.Interval > 0 && e.Interval < time.Second {
			// report.interval is emitted in whole seconds; a sub-second interval
			// would round to 0 and silently disarm the liveness backstop.
			return plog.Logs{}, fmt.Errorf("emit: entity %d (%s) has Interval %s < 1s — it would round to report.interval=0 and disarm liveness; use >= 1s, or 0 for explicit-delete-only", i, e.Type, e.Interval)
		}
		lr := sl.LogRecords().AppendEmpty()
		lr.SetTimestamp(when)
		lr.SetEventName(eventName)
		a := lr.Attributes()
		a.PutStr(wire.AttrEntityType, e.Type)
		putSorted(a.PutEmptyMap(wire.AttrEntityID), e.ID)
		if len(e.Attributes) > 0 {
			putSorted(a.PutEmptyMap(wire.AttrEntityDescription), e.Attributes)
		}
		if e.Interval > 0 {
			a.PutInt(wire.AttrEntityReportInterval, int64(e.Interval/time.Second))
		}
		if eventName == wire.EventEntityDelete && e.DeleteReason != "" {
			a.PutStr(wire.AttrEntityDeleteReason, e.DeleteReason)
		}
		if eventName == wire.EventEntityState && len(e.Relationships) > 0 {
			slc := a.PutEmptySlice(wire.AttrEntityRelationships)
			for j := range e.Relationships {
				r := &e.Relationships[j]
				if r.Type == "" || r.TargetType == "" || len(r.TargetID) == 0 {
					return plog.Logs{}, fmt.Errorf("emit: entity %d (%s) relationship %d is incomplete (need Type, TargetType, TargetID)", i, e.Type, j)
				}
				m := slc.AppendEmpty().SetEmptyMap()
				m.PutStr(wire.RelType, r.Type)
				m.PutStr(wire.RelTargetType, r.TargetType)
				putSorted(m.PutEmptyMap(wire.RelTargetID), r.TargetID)
			}
		}
	}
	return ld, nil
}

// putSorted writes kvs in sorted key order, so the wire form is deterministic.
func putSorted(m pcommon.Map, kvs map[string]string) {
	keys := make([]string, 0, len(kvs))
	for k := range kvs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m.PutStr(k, kvs[k])
	}
}
