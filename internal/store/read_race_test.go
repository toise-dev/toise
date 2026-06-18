package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/store"
)

// TestReadsSurviveConcurrentMaintenance pins the #161 contract: an index
// iterator opened before a coalesce/prune batch commits may still surface
// entries whose primary record the batch deleted, and the read must skip them
// instead of failing the whole query. A reader hammers ReadByEntity and
// ReadByTimeRange while an appender keeps feeding deletable heartbeats and a
// maintainer loops CoalesceHeartbeats + PruneOlderThan; no query may ever
// error. Run with -race.
func TestReadsSurviveConcurrentMaintenance(t *testing.T) {
	s, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	id := model.EntityID("hot")
	if err := s.Append(entEvt(id, model.EntityCreated, tsx(0))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 0; i < 500; i++ {
		if err := s.Append(entEvt(id, model.EntityUnchanged, tsx(int64(i+1)))); err != nil {
			t.Fatalf("seed heartbeat: %v", err)
		}
	}

	stop := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if aerr := s.Append(entEvt(id, model.EntityUnchanged, tsx(int64(1000+i)))); aerr != nil {
				errs <- fmt.Errorf("append: %w", aerr)
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, merr := s.CoalesceHeartbeats(); merr != nil {
				errs <- fmt.Errorf("coalesce: %w", merr)
				return
			}
			if _, _, merr := s.PruneOlderThan(tsx(1_000_000)); merr != nil {
				errs <- fmt.Errorf("prune: %w", merr)
				return
			}
		}
	}()

	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, rerr := s.ReadByEntity(ctx, id); rerr != nil {
			t.Errorf("ReadByEntity failed during maintenance: %v", rerr)
			break
		}
		if _, rerr := s.ReadByTimeRange(ctx, tsx(0), tsx(2_000_000)); rerr != nil {
			t.Errorf("ReadByTimeRange failed during maintenance: %v", rerr)
			break
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for werr := range errs {
		t.Fatalf("concurrent writer: %v", werr)
	}
}
