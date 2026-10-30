package change

import (
	"time"

	"github.com/toise-dev/toise/internal/canonical"
	"github.com/toise-dev/toise/internal/model"
)

// aliasKey names one value of one identity attribute key.
type aliasKey struct {
	attributeKey string
	aliasValue   string
}

// aliasClaimKey names one speaker-asserted equivalence: this alias value is
// another name for this canonical value.
type aliasClaimKey struct {
	alias          aliasKey
	canonicalValue string
}

type identityAliasIndex struct {
	// speakers holds, per claim, the producers still asserting it with their
	// expiry deadline — the same per-speaker reference counting the engine
	// already applies to entities and relations.
	speakers map[aliasClaimKey]map[string]liveRef
	// byAlias answers "is this alias unambiguous" in one lookup: the set of
	// canonical values currently claimed for an alias value.
	byAlias map[aliasKey]map[string]struct{}
}

func newIdentityAliasIndex() identityAliasIndex {
	return identityAliasIndex{
		speakers: make(map[aliasClaimKey]map[string]liveRef),
		byAlias:  make(map[aliasKey]map[string]struct{}),
	}
}

// record adds or refreshes one speaker's claim. isNew reports a claim seen for
// the first time; conflict reports the transition of the alias from one
// canonical candidate to several — the moment the alias becomes ambiguous.
func (idx *identityAliasIndex) record(claim aliasClaimKey, speaker string, ref liveRef) (isNew, conflict bool) {
	speakers, ok := idx.speakers[claim]
	if !ok {
		speakers = make(map[string]liveRef)
		idx.speakers[claim] = speakers
		isNew = true
	}
	speakers[speaker] = ref
	candidates := idx.byAlias[claim.alias]
	if candidates == nil {
		candidates = make(map[string]struct{})
		idx.byAlias[claim.alias] = candidates
	}
	if _, dup := candidates[claim.canonicalValue]; !dup {
		candidates[claim.canonicalValue] = struct{}{}
		if len(candidates) == 2 {
			conflict = true
		}
	}
	return isNew, conflict
}

// resolve answers the unambiguous canonical value for an alias value. Any
// cardinality other than exactly one resolves nothing.
func (idx *identityAliasIndex) resolve(k aliasKey) (string, bool) {
	candidates := idx.byAlias[k]
	if len(candidates) != 1 {
		return "", false
	}
	for v := range candidates {
		return v, true
	}
	return "", false
}

// live reports whether any speaker still asserts the claim.
func (idx *identityAliasIndex) live(claim aliasClaimKey) bool {
	return len(idx.speakers[claim]) > 0
}

// sweep drops expired speaker references; a claim whose last speaker lapsed
// disappears from the index. A vanished equivalence retracts nothing already
// committed: a committed edge has its own liveness and expires on its own.
func (idx *identityAliasIndex) sweep(now time.Time) {
	for claim, speakers := range idx.speakers {
		for key, ref := range speakers {
			if !ref.deadline.IsZero() && now.After(ref.deadline) {
				delete(speakers, key)
			}
		}
		if len(speakers) == 0 {
			delete(idx.speakers, claim)
			delete(idx.byAlias[claim.alias], claim.canonicalValue)
			if len(idx.byAlias[claim.alias]) == 0 {
				delete(idx.byAlias, claim.alias)
			}
		}
	}
}

// aliasClaimFromObservation extracts the equivalence a same_as observation
// asserts, if it is well-formed and believed: readable confidence at or above
// the canonical threshold, same entity type on both endpoints, same identity
// key set differing on exactly one key, both values of that key strings. The
// to value is the alias of the from value for that key.
func aliasClaimFromObservation(obs RelationObservation) (aliasClaimKey, bool) {
	conf, ok := observationConfidence(obs.Attributes)
	if !ok || conf < canonical.DefaultThreshold {
		return aliasClaimKey{}, false
	}
	if obs.From.Type != obs.To.Type {
		return aliasClaimKey{}, false
	}
	from := identityValueMap(obs.From.Identity)
	to := identityValueMap(obs.To.Identity)
	if len(from) != len(to) {
		return aliasClaimKey{}, false
	}
	diffKey := ""
	diffs := 0
	for k, fv := range from {
		tv, ok := to[k]
		if !ok {
			return aliasClaimKey{}, false
		}
		if fv.String() != tv.String() {
			diffs++
			diffKey = k
		}
	}
	if diffs != 1 {
		return aliasClaimKey{}, false
	}
	fv, tv := from[diffKey], to[diffKey]
	if fv.Kind() != model.KindString || tv.Kind() != model.KindString {
		return aliasClaimKey{}, false
	}
	return aliasClaimKey{
		alias:          aliasKey{attributeKey: diffKey, aliasValue: tv.Str()},
		canonicalValue: fv.Str(),
	}, true
}

// observationConfidence reads a confidence off an observation's attributes with
// the same rule canonical.Confidence applies to a relation: KindDouble and
// KindInt in [0,1], everything else — including a "1.0" string — is no belief.
func observationConfidence(attrs []model.KeyValue) (float64, bool) {
	for _, kv := range attrs {
		if kv.Key != canonical.ConfidenceKey {
			continue
		}
		switch kv.Value.Kind() {
		case model.KindDouble:
			c := kv.Value.Double()
			return c, c >= 0 && c <= 1
		case model.KindInt:
			c := float64(kv.Value.Int())
			return c, c >= 0 && c <= 1
		default:
			return 0, false
		}
	}
	return 0, false
}

func identityValueMap(identity []model.KeyValue) map[string]model.Value {
	m := make(map[string]model.Value, len(identity))
	for _, kv := range identity {
		m[kv.Key] = kv.Value
	}
	return m
}

// aliasDeadline is the expiry of one speaker's equivalence: the observation's
// own interval, else the source's re-emit cadence, else zero — and a zero
// deadline never expires, the convention for an assertion with no refresh
// promise.
func aliasDeadline(now time.Time, obs RelationObservation) time.Time {
	switch {
	case obs.Interval > 0:
		return now.Add(obs.Interval)
	case obs.SourceInterval > 0:
		return now.Add(obs.SourceInterval)
	default:
		return time.Time{}
	}
}

// recordAliasLocked records the equivalence a same_as observation asserts, if
// any, under the speaker that asserted it. The caller must hold obsMu.
func (e *Engine) recordAliasLocked(obs RelationObservation) {
	claim, ok := aliasClaimFromObservation(obs)
	if !ok {
		return
	}
	speaker := refKey(obs.Producer, e.currentScope())
	_, conflict := e.aliases.record(claim, speaker, liveRef{deadline: aliasDeadline(e.now(), obs)})
	if conflict && e.aliasConflictReporter != nil {
		e.aliasConflictReporter(claim.alias.attributeKey, claim.alias.aliasValue)
	}
	// A parked edge may have been waiting on the alias value this claim just
	// made resolvable. Re-index the waited-on hashes, and retry at once: the
	// entity that completes the edge may have arrived BEFORE the equivalence
	// did, in which case its observation has already been and gone and no
	// later arrival would ever trigger the replay.
	if len(e.pending) > 0 {
		e.rebuildPendingHashes()
		e.flushPending()
	}
}

// substituteAliases replaces every unambiguously-aliased string value in an
// identity with its canonical value, returning nil when nothing changed.
// Substitution is exact-value only: no case folding, no separator or whitespace
// normalization, no substring.
func (e *Engine) substituteAliases(identity []model.KeyValue) []model.KeyValue {
	var out []model.KeyValue
	changed := false
	for _, kv := range identity {
		if kv.Value.Kind() == model.KindString {
			if canonicalValue, ok := e.aliases.resolve(aliasKey{attributeKey: kv.Key, aliasValue: kv.Value.Str()}); ok {
				out = append(out, model.KeyValue{Key: kv.Key, Value: model.StringValue(canonicalValue)})
				changed = true
				continue
			}
		}
		out = append(out, kv)
	}
	if !changed {
		return nil
	}
	return out
}

// resolveEndpoint resolves one endpoint: exact identity first, then — only on
// failure — one retry with unambiguous aliases substituted. The bool reports
// whether substitution produced the result.
func (e *Engine) resolveEndpoint(ref EndpointRef) (model.EntityID, bool) {
	if id, ok := e.matchIdentity(ref.Type, ref.Identity); ok {
		return id, false
	}
	sub := e.substituteAliases(ref.Identity)
	if sub == nil {
		return "", false
	}
	if id, ok := e.matchIdentity(ref.Type, sub); ok {
		return id, true
	}
	return "", false
}

// aliasBeliefLive reports whether a parked same_as edge's equivalence is still
// asserted in the index: its belief was retained even though the edge itself
// is being given up on.
func (e *Engine) aliasBeliefLive(obs RelationObservation) bool {
	if obs.Type != model.RelSameAs {
		return false
	}
	claim, ok := aliasClaimFromObservation(obs)
	return ok && e.aliases.live(claim)
}

// dropPendingRelation gives up on a parked edge past its TTL. A same_as whose
// equivalence is still live in the alias index is not a lost adjacency: its
// belief was retained, so it is not counted as a discard.
func (e *Engine) dropPendingRelation(obs RelationObservation) {
	if e.aliasBeliefLive(obs) {
		e.logger.Warn("dropping relation: endpoint did not arrive within the reconciliation TTL; the alias belief is retained in the identity alias index",
			"relation_type", obs.Type, "from_type", obs.From.Type, "to_type", obs.To.Type)
		return
	}
	e.reportDiscard(obs)
	e.logger.Warn("dropping relation: endpoints did not arrive within the reconciliation TTL",
		"relation_type", obs.Type, "from_type", obs.From.Type, "to_type", obs.To.Type)
}
