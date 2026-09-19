package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// fingerprintOf is what a consumer reads off an answer and carries to the next
// call (ADR 0035).
func fingerprintOf(t *testing.T, s *Server, id string) string {
	t.Helper()
	_, out, err := s.getEntity(context.Background(), nil, GetEntityInput{EntityID: id})
	if err != nil {
		t.Fatalf("getEntity(%q): %v", id, err)
	}
	if out.Entity.IdentityFingerprint == "" {
		t.Fatalf("entity %q carries no identity_fingerprint", id)
	}
	return out.Entity.IdentityFingerprint
}

// Every answer carries the fingerprint, and it is derived from identity rather
// than from the local id — so a consumer never has to compute it.
func TestEntityAnswersCarryTheFingerprint(t *testing.T) {
	s := newTestServer()
	fp := fingerprintOf(t, s, "01HOST_WEB")

	if !strings.HasPrefix(fp, "host:") {
		t.Errorf("fingerprint %q is not type-prefixed", fp)
	}
	if strings.Contains(fp, "01HOST_WEB") {
		t.Errorf("fingerprint %q leaks the local id; it must derive from identity", fp)
	}
}

// The point of the change: a tool takes the fingerprint wherever it takes an id,
// and answers the same thing.
func TestToolsAcceptFingerprintWhereTheyAcceptAnID(t *testing.T) {
	s := newTestServer()
	fp := fingerprintOf(t, s, "01HOST_WEB")
	ctx := context.Background()

	_, byID, err := s.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity by id: %v", err)
	}
	_, byFP, err := s.getEntity(ctx, nil, GetEntityInput{EntityID: fp})
	if err != nil {
		t.Fatalf("getEntity by fingerprint: %v", err)
	}
	if byID.Entity.ID != byFP.Entity.ID || byID.Entity.Label != byFP.Entity.Label {
		t.Errorf("the two handles answered differently: id=%+v fingerprint=%+v", byID.Entity, byFP.Entity)
	}

	_, nID, err := s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getNeighbors by id: %v", err)
	}
	_, nFP, err := s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: fp})
	if err != nil {
		t.Fatalf("getNeighbors by fingerprint: %v", err)
	}
	if nID.Count != nFP.Count || nID.Total != nFP.Total {
		t.Errorf("neighbors differ: by id %d/%d, by fingerprint %d/%d", nID.Count, nID.Total, nFP.Count, nFP.Total)
	}
}

// A fingerprint that names nothing fails loudly, and is never mistaken for an
// unknown id that would simply return empty.
func TestUnknownFingerprintIsAnError(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	if _, _, err := s.getEntity(ctx, nil, GetEntityInput{EntityID: "host:0000deadbeef"}); err == nil {
		t.Error("an unknown fingerprint did not error")
	}
	if _, _, err := s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "host:0000deadbeef"}); err == nil {
		t.Error("an unknown fingerprint did not error on get_neighbors")
	}
}

// The fingerprint is the same on every node; the id is not. This is the whole
// reason the handle exists, so it is asserted rather than assumed.
func TestFingerprintIsTheSameAcrossNodes(t *testing.T) {
	ident := []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}}
	nodeA := model.Entity{ID: "01NODEA_LOCAL", Type: model.TypeHost, Identity: ident}
	nodeB := model.Entity{ID: "01NODEB_LOCAL", Type: model.TypeHost, Identity: ident}

	if nodeA.ID == nodeB.ID {
		t.Fatal("the two nodes share an id; the test proves nothing")
	}
	if entityOut(nodeA, false).IdentityFingerprint != entityOut(nodeB, false).IdentityFingerprint {
		t.Error("the same entity got different fingerprints on two nodes")
	}
}
