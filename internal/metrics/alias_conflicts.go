package metrics

import "github.com/prometheus/client_golang/prometheus"

// AliasConflicts counts the identity equivalences refused because two speakers
// claimed the same alias value for different canonical values.
//
// It exists because the refusal itself is correct and invisible. When two
// entities each declare that the same value is their alias, neither is chosen
// and the substitution fails as though no alias existed — which is the right
// answer, since a wrong merge answers confidently about the wrong machine and
// costs a reader more than a gap. But the symptom a reader sees is "the
// adjacency never came back", and without this the cause is in no surface they
// would consult.
//
// The attribute key is a label and the alias VALUE is not. An identity value is
// inventory data and label cardinality would follow the fleet; identity keys
// are few and closed. The count is what alerts, the value is in the log line
// beside it.
//
// Plus the tenant, which 0.22 shipped without: a conflict an operator cannot
// attribute to a client sends them to the logs to find out which one, which is
// the errand this instrument exists to spare them. The attribution comes from
// the registry — an engine serves one tenant and holds no handle on which.
type AliasConflicts struct {
	refused *prometheus.CounterVec // tenant, attribute_key
	count   prometheus.Counter
}

// NewAliasConflicts builds the conflict instruments.
func NewAliasConflicts() *AliasConflicts {
	return &AliasConflicts{
		refused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_identity_alias_conflicts_total",
			Help: "Identity equivalences refused because two speakers claimed the same alias value for different canonical values, by tenant and identity attribute key. Neither claim is chosen, so every edge that resolved through that value stops resolving — which reads to a consumer as an adjacency that never came back. Expect either a genuinely duplicated value on the devices, or two speakers disagreeing about what they are looking at.",
		}, []string{"tenant", "attribute_key"}),
		// Deliberately NOT labeled by tenant, for the same reason Discards.count
		// is not: it must exist at zero from the first scrape, so an alert on an
		// increase needs no series to appear first.
		count: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "toise_identity_alias_conflicts_count",
			Help: "How many identity-alias conflicts this instance has refused since start. Published as zero too, so alerting on an increase needs no series to exist first.",
		}),
	}
}

// Collectors returns the underlying collectors for registration.
func (a *AliasConflicts) Collectors() []prometheus.Collector {
	return []prometheus.Collector{a.refused, a.count}
}

// Refused records one refused equivalence. A nil *AliasConflicts records
// nothing, so a caller without metrics needs no guard.
//
// An empty tenant is reported as "unknown" rather than dropped: a figure that
// is hard to attribute is still a figure, and the one case worth seeing is
// exactly the one whose provenance went missing.
func (a *AliasConflicts) Refused(tenantID, attributeKey, aliasValue string) {
	if a == nil {
		return
	}
	_ = aliasValue // deliberately not a label: see the type comment
	a.refused.WithLabelValues(tenantOrUnknown(tenantID), attributeKey).Inc()
	a.count.Inc()
}
