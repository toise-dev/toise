// Command producer-systemd maps systemd service units to Toise entities: one
// "service" per unit (identity {host.id, service.name}, descriptive active_state
// and sub_state), the host, and a runs_on edge. Linux only — it shells out to
// `systemctl`.
//
// "service" is not part of the built-in vocabulary, so run toise-server with
// --accept-unknown-types:
//
//	./bin/toise-server --data-dir /tmp/toise-data --debug-ui --accept-unknown-types
//	go run ./examples/producer-systemd --endpoint 127.0.0.1:4317
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/toise-dev/toise/pkg/emit"
)

func main() {
	endpoint := flag.String("endpoint", "127.0.0.1:4317", "toise-server OTLP/gRPC endpoint")
	interval := flag.Duration("interval", time.Minute, "liveness interval advertised to Toise")
	heartbeat := flag.Duration("heartbeat", 15*time.Second, "how often to re-scan systemd and re-assert; keep below --interval")
	flag.Parse()

	if err := run(*endpoint, *interval, *heartbeat); err != nil {
		log.Fatalf("producer-systemd: %v", err)
	}
}

func run(endpoint string, interval, heartbeat time.Duration) error {
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("reading hostname: %w", err)
	}
	client, err := emit.New(emit.Options{
		Endpoint:          endpoint,
		ServiceName:       "producer-systemd",
		ServiceInstanceID: "producer-systemd@" + hostname,
	})
	if err != nil {
		return fmt.Errorf("dialing %s: %w", endpoint, err)
	}
	defer func() { _ = client.Close() }()

	host := emit.Entity{Type: "host", ID: map[string]string{"host.id": hostname}, Interval: interval}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	prev := map[string]emit.Entity{}

	scanAndEmit := func() error {
		services, err := listServices(hostname, interval)
		if err != nil {
			return err
		}
		batch := []emit.Entity{host}
		cur := map[string]emit.Entity{}
		for _, s := range services {
			cur[s.ID["service.name"]] = s
			batch = append(batch, s)
		}
		if err := client.State(ctx, batch...); err != nil {
			return fmt.Errorf("emitting state: %w", err)
		}
		var gone []emit.Entity
		for name, s := range prev {
			if _, ok := cur[name]; !ok {
				gone = append(gone, s)
			}
		}
		if len(gone) > 0 {
			if err := client.Delete(ctx, gone...); err != nil {
				log.Printf("deleting %d departed services failed: %v", len(gone), err)
			}
		}
		prev = cur
		return nil
	}

	if err := scanAndEmit(); err != nil {
		return err
	}
	log.Printf("emitting host %q and its systemd services; heartbeat every %s", hostname, heartbeat)

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := scanAndEmit(); err != nil {
				log.Printf("scan failed (will retry): %v", err)
			}
		case <-ctx.Done():
			all := []emit.Entity{}
			for _, s := range prev {
				all = append(all, s)
			}
			if len(all) > 0 {
				delCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = client.Delete(delCtx, all...)
				cancel()
			}
			log.Print("deleted services; bye")
			return nil
		}
	}
}

// listServices maps loaded service units to entities. Identity is the stable
// {host.id, service.name}; active_state/sub_state are descriptive (they change as
// units start and stop).
func listServices(hostname string, interval time.Duration) ([]emit.Entity, error) {
	out, err := exec.Command("systemctl", "list-units", "--type=service",
		"--all", "--plain", "--no-legend", "--no-pager").Output()
	if err != nil {
		return nil, fmt.Errorf("running `systemctl list-units` (Linux only): %w", err)
	}
	var entities []emit.Entity
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// Columns: UNIT LOAD ACTIVE SUB DESCRIPTION...
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		unit, active, sub := f[0], f[2], f[3]
		entities = append(entities, emit.Entity{
			Type: "service",
			ID:   map[string]string{"host.id": hostname, "service.name": unit},
			Attributes: map[string]string{
				"active_state": active,
				"sub_state":    sub,
			},
			Interval: interval,
			Relationships: []emit.Relationship{{
				Type:       "runs_on",
				TargetType: "host",
				TargetID:   map[string]string{"host.id": hostname},
			}},
		})
	}
	return entities, nil
}
