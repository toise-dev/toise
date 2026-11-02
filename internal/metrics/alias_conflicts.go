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
type AliasConflicts struct {
	refused *prometheus.CounterVec // attribute_key
	count   prometheus.Counter
}

// NewAliasConflicts builds the conflict instruments.
func NewAliasConflicts() *AliasConflicts {
	return &AliasConflicts{
		refused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_identity_alias_conflicts_total",
			Help: "Identity equivalences refused because two speakers claimed the same alias value for different canonical values, by identity attribute key. Neither claim is chosen, so every edge that resolved through that value stops resolving — which reads to a consumer as an adjacency that never came back. Expect either a genuinely duplicated value on the devices, or two speakers disagreeing about what they are looking at.",
		}, []string{"attribute_key"}),
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
func (a *AliasConflicts) Refused(attributeKey, aliasValue string) {
	if a == nil {
		return
	}
	_ = aliasValue // deliberately not a label: see the type comment
	a.refused.WithLabelValues(attributeKey).Inc()
	a.count.Inc()
}
