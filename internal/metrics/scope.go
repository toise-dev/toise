package metrics

import (
	"net/http"
	"sort"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"google.golang.org/protobuf/proto"

	"github.com/prometheus/client_golang/prometheus"
)

// tenantLabel is the label every per-tenant series carries. A family whose
// metrics do not carry it is server-wide, and server-wide is exactly what a
// scoped caller must not see: open tenants, store size and the Go runtime say
// how many other clients there are and how big they are.
const tenantLabel = "tenant"

// scopeInfoName is emitted on every scoped answer so that a tenant with no
// series yet is distinguishable from a broken exporter. Without it the two are
// the same empty body, and an empty answer that cannot say why it is empty is
// the defect this release spent itself correcting.
const scopeInfoName = "toise_metrics_scope_info"

// TenantScope reports which tenants' series a request may read: the tenant id
// when it may read one, or all=true when it may read every one. The empty id
// with all=false means none, and is served as an answer, not as an error.
type TenantScope func(*http.Request) (tenantID string, all bool)

// serveScoped writes the exposition filtered to one tenant's series.
func serveScoped(w http.ResponseWriter, r *http.Request, g prometheus.Gatherer, tenantID string) {
	mfs, err := g.Gather()
	if err != nil && len(mfs) == 0 {
		http.Error(w, "gathering metrics", http.StatusInternalServerError)
		return
	}
	out := make([]*dto.MetricFamily, 0, len(mfs)+1)
	for _, mf := range mfs {
		kept := make([]*dto.Metric, 0, len(mf.GetMetric()))
		for _, m := range mf.GetMetric() {
			if labelValue(m, tenantLabel) == tenantID {
				kept = append(kept, m)
			}
		}
		if len(kept) == 0 {
			continue
		}
		// A fresh family rather than a copy of mf: a dto.MetricFamily is a
		// protobuf message and copying one by value is what go vet rejects.
		out = append(out, &dto.MetricFamily{
			Name:   mf.Name,
			Help:   mf.Help,
			Type:   mf.Type,
			Metric: kept,
		})
	}
	out = append(out, scopeInfo(tenantID))
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })

	contentType := expfmt.Negotiate(r.Header)
	w.Header().Set("Content-Type", string(contentType))
	enc := expfmt.NewEncoder(w, contentType)
	for _, mf := range out {
		if err := enc.Encode(mf); err != nil {
			// The status line is already written; the scrape will fail on a
			// truncated body, which is the honest outcome.
			return
		}
	}
}

// scopeInfo is the witness that a filter was applied and to whom.
func scopeInfo(tenantID string) *dto.MetricFamily {
	return &dto.MetricFamily{
		Name: proto.String(scopeInfoName),
		Help: proto.String("Always 1, labeled with the tenant this answer was scoped to: an empty answer with this series is a tenant without metrics, an empty answer without it is a broken scrape."),
		Type: dto.MetricType_GAUGE.Enum(),
		Metric: []*dto.Metric{{
			Label: []*dto.LabelPair{{
				Name:  proto.String(tenantLabel),
				Value: proto.String(tenantID),
			}},
			Gauge: &dto.Gauge{Value: proto.Float64(1)},
		}},
	}
}

func labelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}
