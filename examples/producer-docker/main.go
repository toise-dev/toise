// Command producer-docker turns the local Docker state into Toise entities: one
// "container" entity per running container, the host they run on, and a runs_on
// edge. It refreshes on a heartbeat and deletes containers as they disappear.
//
// "container" is a built-in entity type, so this runs against a stock server:
//
//	./bin/toise-server --data-dir /tmp/toise-data --debug-ui
//	go run ./examples/producer-docker --endpoint 127.0.0.1:4317
//
// It shells out to `docker ps`, so it needs the docker CLI on PATH and a running
// daemon — no Go Docker SDK dependency.
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
	heartbeat := flag.Duration("heartbeat", 15*time.Second, "how often to re-scan docker and re-assert; keep below --interval")
	flag.Parse()

	if err := run(*endpoint, *interval, *heartbeat); err != nil {
		log.Fatalf("producer-docker: %v", err)
	}
}

func run(endpoint string, interval, heartbeat time.Duration) error {
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("reading hostname: %w", err)
	}
	client, err := emit.New(emit.Options{
		Endpoint:          endpoint,
		ServiceName:       "producer-docker",
		ServiceInstanceID: "producer-docker@" + hostname,
	})
	if err != nil {
		return fmt.Errorf("dialing %s: %w", endpoint, err)
	}
	defer func() { _ = client.Close() }()

	host := emit.Entity{Type: "host", ID: map[string]string{"host.id": hostname}, Interval: interval}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Track the containers asserted last round so we can delete the ones that
	// have since disappeared (a stopped/removed container would otherwise linger
	// until its liveness interval elapsed).
	prev := map[string]emit.Entity{}

	scanAndEmit := func() error {
		containers, err := listContainers(hostname, interval)
		if err != nil {
			return err
		}
		batch := []emit.Entity{host}
		cur := map[string]emit.Entity{}
		for _, c := range containers {
			cur[c.ID["container.id"]] = c
			batch = append(batch, c)
		}
		if err := client.State(ctx, batch...); err != nil {
			return fmt.Errorf("emitting state: %w", err)
		}
		var gone []emit.Entity
		for id, c := range prev {
			if _, ok := cur[id]; !ok {
				gone = append(gone, c)
			}
		}
		if len(gone) > 0 {
			if err := client.Delete(ctx, gone...); err != nil {
				log.Printf("deleting %d departed containers failed: %v", len(gone), err)
			}
		}
		prev = cur
		return nil
	}

	if err := scanAndEmit(); err != nil {
		return err
	}
	log.Printf("emitting host %q and its containers; heartbeat every %s", hostname, heartbeat)

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
			for _, c := range prev {
				all = append(all, c)
			}
			if len(all) > 0 {
				delCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = client.Delete(delCtx, all...)
				cancel()
			}
			log.Print("deleted containers; bye")
			return nil
		}
	}
}

// listContainers maps running containers to entities. Identity is the immutable
// container id; the image, name and state are descriptive (state changes).
func listContainers(hostname string, interval time.Duration) ([]emit.Entity, error) {
	out, err := exec.Command("docker", "ps", "--no-trunc",
		"--format", "{{.ID}}\t{{.Image}}\t{{.Names}}\t{{.State}}").Output()
	if err != nil {
		return nil, fmt.Errorf("running `docker ps` (is the daemon up?): %w", err)
	}
	var entities []emit.Entity
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		entities = append(entities, emit.Entity{
			Type: "container",
			ID:   map[string]string{"container.id": f[0]},
			Attributes: map[string]string{
				"container.image.name": f[1],
				"container.name":       f[2],
				"state":                f[3],
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
