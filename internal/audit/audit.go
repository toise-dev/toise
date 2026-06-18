// Package audit records security-relevant operations — today the operator
// writes (annotate_entity) — to an append-only, exportable JSON-line stream,
// distinct from the producer event log (ADR 0028). It is off by default: a nil
// Auditor, or one built with no sink, is a no-op, so the zero-config and
// trusted-network postures are unchanged (ADR 0030). A write failure is logged,
// never returned — auditing must not fail the operation it records (which has
// already happened), but a gap must not be silent either.
package audit

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Event is one audited operation.
type Event struct {
	Time    time.Time `json:"time"`
	Tenant  string    `json:"tenant"`
	Surface string    `json:"surface"`          // "mcp" | "graphql"
	Action  string    `json:"action"`           // e.g. "annotate_entity"
	Target  string    `json:"target,omitempty"` // the affected entity id
}

// Auditor appends JSON-line audit records to a sink. Safe for concurrent use.
type Auditor struct {
	mu     sync.Mutex
	w      io.Writer
	logger *slog.Logger
}

// New returns an Auditor writing to w. A nil w disables auditing (returns nil):
// every Record on the result is then a no-op. A nil logger uses slog.Default.
func New(w io.Writer, logger *slog.Logger) *Auditor {
	if w == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Auditor{w: w, logger: logger}
}

// Enabled reports whether records are written. Safe on a nil receiver.
func (a *Auditor) Enabled() bool { return a != nil && a.w != nil }

// Record appends ev as one JSON line. No-op when disabled (incl. a nil
// receiver). A marshal/write error is logged at Warn, not returned: the audited
// operation already succeeded, so failing it would be wrong — but the gap is
// surfaced rather than swallowed.
func (a *Auditor) Record(ev Event) {
	if !a.Enabled() {
		return
	}
	line, err := json.Marshal(ev)
	if err != nil {
		a.logger.Warn("audit record marshal failed", "action", ev.Action, "err", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.w.Write(append(line, '\n')); err != nil {
		a.logger.Warn("audit record write failed (audit gap)", "action", ev.Action, "err", err)
	}
}
