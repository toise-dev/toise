// Command producer-minimal is the smallest useful Toise producer: it emits a
// host and a service.listener that runs on it, keeps them alive on a heartbeat,
// and deletes them on Ctrl-C. It uses only the built-in entity vocabulary, so it
// works against a stock toise-server with no extra flags.
//
//	go run ./examples/producer-minimal --endpoint 127.0.0.1:4317
//
// Watch the result in the debug UI, or over MCP with find_entities / get_neighbors.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/toise-dev/toise/pkg/emit"
)

func main() {
	endpoint := flag.String("endpoint", "127.0.0.1:4317", "toise-server OTLP/gRPC endpoint")
	interval := flag.Duration("interval", time.Minute, "liveness interval advertised to Toise (the entity expires if not re-asserted within it)")
	heartbeat := flag.Duration("heartbeat", 20*time.Second, "how often to re-assert; keep it well below --interval")
	flag.Parse()

	if err := run(*endpoint, *interval, *heartbeat); err != nil {
		log.Fatalf("producer-minimal: %v", err)
	}
}

func run(endpoint string, interval, heartbeat time.Duration) error {
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("reading hostname: %w", err)
	}

	client, err := emit.New(emit.Options{
		Endpoint: endpoint,
		// ServiceInstanceID is this producer's stable liveness key: Toise
		// reference-counts an entity's liveness per producer (ADR 0019).
		ServiceName:       "producer-minimal",
		ServiceInstanceID: "producer-minimal@" + hostname,
	})
	if err != nil {
		return fmt.Errorf("dialing %s: %w", endpoint, err)
	}
	defer func() { _ = client.Close() }()

	// Identity is the exact set of identifying attributes; keep volatile values
	// (status, metrics) out of it and in Attributes instead.
	host := emit.Entity{
		Type:     "host",
		ID:       map[string]string{"host.id": hostname},
		Interval: interval,
	}
	listener := emit.Entity{
		Type:       "service.listener",
		ID:         map[string]string{"service.endpoint": hostname + ":8080/tcp"},
		Attributes: map[string]string{"listen.address": "0.0.0.0"},
		Interval:   interval,
		Relationships: []emit.Relationship{{
			Type:       "runs_on",
			TargetType: "host",
			TargetID:   map[string]string{"host.id": hostname},
		}},
	}
	entities := []emit.Entity{host, listener}

	// SIGINT/SIGTERM: stop the heartbeat, delete the entities, exit cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := client.State(ctx, entities...); err != nil {
		return fmt.Errorf("initial state: %w", err)
	}
	log.Printf("emitted host %q and its service.listener; heartbeat every %s (interval %s)", hostname, heartbeat, interval)

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := client.State(ctx, entities...); err != nil {
				log.Printf("heartbeat failed (will retry): %v", err)
			}
		case <-ctx.Done():
			// Use a fresh context: the signal already cancelled ctx.
			delCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.Delete(delCtx, entities...); err != nil {
				return fmt.Errorf("delete on shutdown: %w", err)
			}
			log.Print("deleted entities; bye")
			return nil
		}
	}
}
