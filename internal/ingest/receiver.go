package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	// Register the gzip decompressor so the server can accept gzip-encoded
	// OTLP exports. gRPC-Go does not install it by default, yet gzip is the
	// OTel ecosystem default (the OTel SDK and senhub-agent compress with it):
	// without this, gzip'd exports fail at the transport with "Decompressor is
	// not installed" before reaching the handler — a silent drop on the wire.
	_ "google.golang.org/grpc/encoding/gzip"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/tenant"
)

// Receiver is an OTLP/gRPC logs server that routes entity events to the change
// engine.
type Receiver struct {
	srv    *grpc.Server
	logs   *logsServer
	logger *slog.Logger
}

// NewReceiver builds a receiver routing every record to e (single tenant). A nil
// logger uses slog.Default. It is shorthand for NewRoutedReceiver with a constant
// engine and no metrics.
func NewReceiver(e *change.Engine, logger *slog.Logger, opts ...grpc.ServerOption) *Receiver {
	return NewRoutedReceiver(func(string) (*change.Engine, error) { return e, nil }, nil, nil, false, logger, opts...)
}

// NewRoutedReceiver builds a receiver that resolves the change engine per tenant.
// engineFor maps a (sanitized) tenant id to its engine; the tenant is read from
// the X-Scope-OrgID gRPC metadata and may be overridden per ResourceLogs by a
// tenant.id resource attribute (ADR 0025, #95). authorize, when non-nil, is
// consulted for every RESOLVED tenant id — including the per-ResourceLogs
// override, which a gRPC interceptor cannot see (#104). m carries the hot-path
// ingest counters (nil counts nothing). A nil logger uses slog.Default.
func NewRoutedReceiver(engineFor func(tenantID string) (*change.Engine, error), authorize func(ctx context.Context, tenantID string) bool, m *Metrics, acceptUnknownTypes bool, logger *slog.Logger, opts ...grpc.ServerOption) *Receiver {
	if logger == nil {
		logger = slog.Default()
	}
	srv := grpc.NewServer(opts...)
	ls := &logsServer{engineFor: engineFor, authorize: authorize, metrics: m, acceptUnknown: acceptUnknownTypes, logger: logger, reconcilers: make(map[string]*embeddedReconciler)}
	plogotlp.RegisterGRPCServer(srv, ls)
	return &Receiver{srv: srv, logs: ls, logger: logger}
}

// Serve accepts connections on lis until Stop is called. It blocks.
func (r *Receiver) Serve(lis net.Listener) error {
	r.logger.Info("otlp receiver listening", "addr", lis.Addr().String())
	if err := r.srv.Serve(lis); err != nil {
		return fmt.Errorf("serving otlp: %w", err)
	}
	return nil
}

// Stop gracefully stops the server.
func (r *Receiver) Stop() { r.srv.GracefulStop() }

// logsServer implements the OTLP logs service.
type logsServer struct {
	plogotlp.UnimplementedGRPCServer
	engineFor func(tenantID string) (*change.Engine, error)
	authorize func(ctx context.Context, tenantID string) bool
	metrics   *Metrics
	// acceptUnknown relaxes the vocabulary check at the boundary (#141):
	// unknown entity types pass shape validation and are counted, not rejected.
	acceptUnknown bool
	logger        *slog.Logger

	// reconcilers holds the embedded-relationship state per tenant. It must be
	// per-tenant: two tenants may assert the same source entity key, and a shared
	// reconciler would let one tenant's re-emit remove the other's relations.
	mu          sync.Mutex
	reconcilers map[string]*embeddedReconciler
}

func (s *logsServer) reconcilerFor(tenantID string) *embeddedReconciler {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reconcilers[tenantID]
	if !ok {
		r = newEmbeddedReconciler()
		s.reconcilers[tenantID] = r
	}
	return r
}

// Export ingests a batch of OTLP logs, routing entity-event LogRecords to the
// per-tenant change engine and ignoring the rest. The tenant comes from the
// request's X-Scope-OrgID metadata and may be overridden per ResourceLogs by a
// tenant.id resource attribute (so one OTLP stream can carry several tenants).
func (s *logsServer) Export(ctx context.Context, req plogotlp.ExportRequest) (resp plogotlp.ExportResponse, err error) {
	defer func() { s.metrics.export(err == nil) }()
	ld := req.Logs()
	var handled, skipped, rejected int
	var rejectMsg string
	var dropped []string

	// An invalid X-Scope-OrgID is rejected rather than silently folded into the
	// default tenant — a tenant id that cannot be honored is a caller error.
	baseTenant, ok := tenant.FromGRPC(ctx)
	if !ok {
		// Permanent caller error: InvalidArgument tells a spec-compliant
		// exporter not to retry (#111).
		s.metrics.tenantRejected()
		return plogotlp.NewExportResponse(), status.Errorf(codes.InvalidArgument, "invalid %s metadata", tenant.HeaderOrgID)
	}

	// Each ResourceLogs (one producer) is ingested as a single batch so its events
	// commit with one durable append (one fsync) instead of one per record.
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		res := rls.At(i).Resource()
		producer, _ := strAttr(res.Attributes(), resAttrProducer)
		tenantID := baseTenant
		if rt, ok := strAttr(res.Attributes(), tenant.ResourceAttr); ok {
			san, ok := tenant.Sanitize(rt)
			if !ok {
				s.metrics.tenantRejected()
				return plogotlp.NewExportResponse(), status.Errorf(codes.InvalidArgument, "invalid %s resource attribute %q", tenant.ResourceAttr, rt)
			}
			tenantID = san
		}
		if s.authorize != nil && !s.authorize(ctx, tenantID) {
			// The token authenticated (the interceptor passed) but is not
			// bound to this tenant: permanent, do not retry (#104).
			s.metrics.tenantRejected()
			return plogotlp.NewExportResponse(), status.Errorf(codes.PermissionDenied, "token not authorized for tenant %q", tenantID)
		}
		engine, err := s.engineFor(tenantID)
		if err != nil {
			// A policy refusal (auto-create off, allowlist, tenant cap) is
			// permanent: InvalidArgument, do not retry. Anything else is a
			// transient store/resolution failure: Unavailable, retry (#111).
			if errors.Is(err, tenant.ErrNotAllowed) {
				s.metrics.tenantRejected()
				return plogotlp.NewExportResponse(), status.Errorf(codes.InvalidArgument, "resolving tenant %q: %v", tenantID, err)
			}
			return plogotlp.NewExportResponse(), status.Errorf(codes.Unavailable, "resolving tenant %q: %v", tenantID, err)
		}
		reconciler := s.reconcilerFor(tenantID)
		sls := rls.At(i).ScopeLogs()
		var routeErr error
		batchErr := engine.Batch(func(b *change.Batch) {
			for j := 0; j < sls.Len() && routeErr == nil; j++ {
				recs := sls.At(j).LogRecords()
				for k := 0; k < recs.Len(); k++ {
					lr := recs.At(k)
					ok, drop, err := routeRecordVocab(b, lr, producer, !s.acceptUnknown)
					if ok && s.acceptUnknown && err == nil {
						if typ, tok := strAttr(lr.Attributes(), attrEntityType); tok && !model.IsKnownEntityType(typ) {
							s.metrics.unknownTypeAccepted()
						}
					}
					dropped = append(dropped, drop...)
					if err != nil {
						// A contract violation is permanent and per-record:
						// reject this record (reported via partial success,
						// below) and keep its valid siblings — a retry could
						// never make it valid, and failing the export would
						// poison the whole producer stream (#109). Its embedded
						// relationships are skipped with it.
						if errors.Is(err, errInvalidRecord) {
							rejected++
							if rejectMsg == "" {
								rejectMsg = err.Error()
							}
							continue
						}
						routeErr = err
						break
					}
					// Embedded relationships ride on entity-state events (spec PR
					// #4836); reconcile them additively alongside routeRecord.
					edrop, eerr := reconciler.handle(b, lr)
					dropped = append(dropped, edrop...)
					if eerr != nil {
						routeErr = eerr
						break
					}
					if ok {
						handled++
					} else {
						skipped++
					}
				}
			}
		})
		// Routing and flush failures are retriable: the batch is a staged unit
		// of work, so a failed flush leaves no trace in the projection and
		// nothing was broadcast — the producer's retry re-classifies every
		// observation against durable state and regenerates the lost events.
		if routeErr != nil {
			// Contract violations were already split off as per-record partial
			// success; what reaches here is engine-internal, hence retriable.
			return plogotlp.NewExportResponse(), status.Errorf(codes.Unavailable, "routing log record: %v", routeErr)
		}
		if batchErr != nil {
			return plogotlp.NewExportResponse(), status.Errorf(codes.Unavailable, "ingest batch: %v", batchErr)
		}
	}
	if len(dropped) > 0 {
		// Non-scalar attribute values are dropped (producers must send flat scalar
		// maps) — surface it, never lose data silently.
		s.logger.Warn("dropped non-scalar attribute values at ingest boundary", "keys", dropped)
	}
	if skipped > 0 {
		s.logger.Debug("otlp export processed", "entity_events", handled, "ignored", skipped)
	}
	s.metrics.addRecords("handled", handled)
	s.metrics.addRecords("ignored", skipped)
	s.metrics.addRecords("rejected", rejected)
	s.metrics.addDroppedValues(len(dropped))
	resp = plogotlp.NewExportResponse()
	if rejected > 0 {
		// OTLP partial success: the export as a whole is accepted (no retry),
		// the rejected records are reported back to the producer.
		ps := resp.PartialSuccess()
		ps.SetRejectedLogRecords(int64(rejected))
		ps.SetErrorMessage(rejectMsg)
		s.logger.Warn("rejected entity records violating the wire contract",
			"rejected", rejected, "first_error", rejectMsg)
	}
	return resp, nil
}
