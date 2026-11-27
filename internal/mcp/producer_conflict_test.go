package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

type fakeConflicts map[string][]string

func (f fakeConflicts) ConflictingHosts(producer string) []string {
	return f[producer]
}

func TestGetEntityNamesProducerConflict(t *testing.T) {
	g, st := newFixture()
	agent := model.Entity{ID: "01SVC_AGENT", Type: model.TypeServiceInstance,
		Identity: []model.KeyValue{{Key: "service.instance.id", Value: model.StringValue("agent-a")}}}
	g.entities[agent.ID] = agent
	s := New(g, st)
	s.now = func() time.Time { return time.Date(2026, 5, 29, 20, 0, 0, 0, time.UTC) }
	s.SetProducerConflicts(fakeConflicts{"agent-a": {"host-a", "host-b"}})

	_, out, err := s.getEntity(t.Context(), nil, GetEntityInput{EntityID: string(agent.ID)})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if out.ProducerConflict == nil {
		t.Fatalf("expected producer conflict block, got nil")
	}
	want := []string{"host-a", "host-b"}
	if len(out.ProducerConflict.HostIDs) != len(want) {
		t.Fatalf("host IDs = %v, want %v", out.ProducerConflict.HostIDs, want)
	}
	for i, h := range want {
		if out.ProducerConflict.HostIDs[i] != h {
			t.Fatalf("host IDs = %v, want %v", out.ProducerConflict.HostIDs, want)
		}
	}
	for _, h := range want {
		if !strings.Contains(out.ProducerConflict.Message, h) {
			t.Fatalf("message %q missing %q", out.ProducerConflict.Message, h)
		}
	}
	if !strings.Contains(strings.ToLower(out.ProducerConflict.Message), "several machines") {
		t.Fatalf("message %q missing %q", out.ProducerConflict.Message, "several machines")
	}

	if got := s.producerConflictFor(agent, "2026-05-29T13:00:00Z"); got != nil {
		t.Fatalf("expected no producer conflict for as-of read, got %+v", got)
	}
}

func TestGetEntityNoProducerConflictWhenSingleHostOrUnwired(t *testing.T) {
	g, st := newFixture()
	agent := model.Entity{ID: "01SVC_AGENT", Type: model.TypeServiceInstance,
		Identity: []model.KeyValue{{Key: "service.instance.id", Value: model.StringValue("agent-a")}}}
	g.entities[agent.ID] = agent

	cases := []struct {
		name      string
		conflicts ProducerConflicts
		entityID  model.EntityID
	}{
		{name: "empty conflicts", conflicts: fakeConflicts{}, entityID: agent.ID},
		{name: "unwired server", conflicts: nil, entityID: agent.ID},
		{name: "host entity", conflicts: fakeConflicts{"agent-a": {"host-a", "host-b"}}, entityID: "01HOST_WEB"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(g, st)
			s.now = func() time.Time { return time.Date(2026, 5, 29, 20, 0, 0, 0, time.UTC) }
			s.SetProducerConflicts(tc.conflicts)

			_, out, err := s.getEntity(t.Context(), nil, GetEntityInput{EntityID: string(tc.entityID)})
			if err != nil {
				t.Fatalf("getEntity: %v", err)
			}
			if out.ProducerConflict != nil {
				t.Fatalf("expected nil producer conflict, got %+v", out.ProducerConflict)
			}
		})
	}
}
