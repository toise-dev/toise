package change

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

type memAppender struct {
	evs []model.Event
}

func (a *memAppender) Append(evs ...model.Event) error {
	a.evs = append(a.evs, evs...)
	return nil
}

func entityObs(id string, producer string, interval time.Duration) EntityObservation {
	return EntityObservation{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(id)}},
		Interval: interval,
		Producer: producer,
	}
}

func newTestEngine(clk *time.Time) (*Engine, *memAppender) {
	app := &memAppender{}
	eng := New(projection.New(), app, WithClock(func() time.Time { return *clk }))
	return eng, app
}

func TestLivenessPauseOffByDefault(t *testing.T) {
	clk := time.Unix(1000, 0)
	eng, _ := newTestEngine(&clk)
	if _, err := eng.ObserveEntity(entityObs("host-1", "", 60*time.Second)); err != nil {
		t.Fatalf("ObserveEntity: %v", err)
	}
	clk = clk.Add(5 * time.Minute)
	res, err := eng.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Entities != 1 {
		t.Fatalf("expected 1 expired entity, got %d", res.Entities)
	}
	if res.Paused {
		t.Fatalf("pause should be off by default")
	}
}

func TestLivenessPauseResumeAndReArm(t *testing.T) {
	clk := time.Unix(1000, 0)
	eng, _ := newTestEngine(&clk)
	if _, err := eng.ObserveEntity(entityObs("host-1", "", 60*time.Second)); err != nil {
		t.Fatalf("ObserveEntity: %v", err)
	}
	eng.SetLivenessPause(2*time.Minute, time.Hour)

	// Advance past pauseAfter, sweep should pause and expire nothing.
	clk = clk.Add(5 * time.Minute)
	res, err := eng.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !res.Paused || res.Entities != 0 {
		t.Fatalf("expected paused sweep with 0 expired, got Paused=%v Entities=%d", res.Paused, res.Entities)
	}

	// Empty batch at 30 min proves the link is up; it re-arms the entity.
	clk = time.Unix(1000, 0).Add(30 * time.Minute)
	if err = eng.Batch(func(*Batch) {}); err != nil {
		t.Fatalf("Batch: %v", err)
	}
	// 30s later (still before new deadline) no expiry.
	clk = clk.Add(30 * time.Second)
	res, err = eng.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Entities != 0 || res.Paused {
		t.Fatalf("expected 0 expired after resume, got Entities=%d Paused=%v", res.Entities, res.Paused)
	}
	// 61s after resume, the original entity (not re-asserted) expires.
	clk = clk.Add(31 * time.Second) // now 30m+61s
	res, err = eng.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Entities != 1 || res.Paused {
		t.Fatalf("expected 1 expired entity after one interval, got Entities=%d Paused=%v", res.Entities, res.Paused)
	}
}

func TestLivenessPauseSecondEntityStays(t *testing.T) {
	clk := time.Unix(1000, 0)
	eng, _ := newTestEngine(&clk)
	if _, err := eng.ObserveEntity(entityObs("host-1", "", 60*time.Second)); err != nil {
		t.Fatalf("ObserveEntity host-1: %v", err)
	}
	// pause_after must exceed the longest interval in the tenant (host-2: 2 min),
	// or a quiet spell between two of its own heartbeats would pause expiry again.
	eng.SetLivenessPause(3*time.Minute, time.Hour)

	clk = clk.Add(5 * time.Minute)
	if _, err := eng.Sweep(); err != nil {
		t.Fatal(err)
	}
	// At resume time, observe a second entity with a longer interval.
	clk = time.Unix(1000, 0).Add(30 * time.Minute)
	if _, err := eng.ObserveEntity(entityObs("host-2", "", 2*time.Minute)); err != nil {
		t.Fatalf("ObserveEntity host-2: %v", err)
	}
	// 61s later: host-1 expires (re-armed to now+60s), host-2 stays.
	clk = clk.Add(61 * time.Second)
	res, err := eng.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Entities != 1 {
		t.Fatalf("expected 1 expired entity (host-1), got %d", res.Entities)
	}
	// host-2 was observed at resume with a 2 min interval: it lapses strictly
	// after resume + 2 min, so one second past that it expires.
	clk = clk.Add(60 * time.Second)
	res, err = eng.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Entities != 1 {
		t.Fatalf("expected host-2 to expire eventually, got %d", res.Entities)
	}
}

func TestLivenessPausePastMaxProceeds(t *testing.T) {
	clk := time.Unix(1000, 0)
	eng, _ := newTestEngine(&clk)
	if _, err := eng.ObserveEntity(entityObs("host-1", "", 60*time.Second)); err != nil {
		t.Fatalf("ObserveEntity: %v", err)
	}
	eng.SetLivenessPause(2*time.Minute, 10*time.Minute)

	clk = clk.Add(11 * time.Minute)
	res, err := eng.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Paused {
		t.Fatalf("expected no pause past pauseMax")
	}
	if res.Entities != 1 {
		t.Fatalf("expected 1 expired entity, got %d", res.Entities)
	}
}

func TestLivenessPauseSingleProducerSilentOtherActive(t *testing.T) {
	clk := time.Unix(1000, 0)
	eng, _ := newTestEngine(&clk)
	if _, err := eng.ObserveEntity(entityObs("host-1", "producer-a", 60*time.Second)); err != nil {
		t.Fatalf("ObserveEntity host-1: %v", err)
	}
	eng.SetLivenessPause(2*time.Minute, time.Hour)

	// Producer B keeps observing every minute, keeping lastIngest fresh.
	clk = clk.Add(30 * time.Second)
	if _, err := eng.ObserveEntity(entityObs("host-2", "producer-b", 60*time.Second)); err != nil {
		t.Fatalf("ObserveEntity host-2: %v", err)
	}
	clk = clk.Add(31 * time.Second) // 61 s: host-1 is one second past its deadline
	res, err := eng.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Paused {
		t.Fatalf("pause should not engage while another producer is active")
	}
	if res.Entities != 1 {
		t.Fatalf("expected host-1 to expire, got %d", res.Entities)
	}
}
