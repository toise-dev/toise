// Command producer-uptime probes a list of URLs on an interval and emits each as
// a Toise entity whose descriptive state (up, status code, latency) flips as the
// site goes up or down. The identity is the stable URL; the health is descriptive.
//
// "service.endpoint" is not part of the built-in vocabulary, so run toise-server
// with --accept-unknown-types:
//
//	./bin/toise-server --data-dir /tmp/toise-data --debug-ui --accept-unknown-types
//	go run ./examples/producer-uptime --endpoint 127.0.0.1:4317 \
//	    --urls https://example.com,https://httpstat.us/503
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/toise-dev/toise/pkg/emit"
)

func main() {
	endpoint := flag.String("endpoint", "127.0.0.1:4317", "toise-server OTLP/gRPC endpoint")
	urls := flag.String("urls", "", "comma-separated URLs to probe (required)")
	probe := flag.Duration("interval", 15*time.Second, "how often to probe each URL")
	timeout := flag.Duration("timeout", 5*time.Second, "per-probe HTTP timeout")
	flag.Parse()

	list := splitURLs(*urls)
	if len(list) == 0 {
		log.Fatal("producer-uptime: --urls is required (comma-separated)")
	}
	if err := run(*endpoint, list, *probe, *timeout); err != nil {
		log.Fatalf("producer-uptime: %v", err)
	}
}

func splitURLs(s string) []string {
	var out []string
	for _, u := range strings.Split(s, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func run(endpoint string, urls []string, probe, timeout time.Duration) error {
	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("reading hostname: %w", err)
	}
	client, err := emit.New(emit.Options{
		Endpoint:          endpoint,
		ServiceName:       "producer-uptime",
		ServiceInstanceID: "producer-uptime@" + host,
	})
	if err != nil {
		return fmt.Errorf("dialing %s: %w", endpoint, err)
	}
	defer func() { _ = client.Close() }()

	httpClient := &http.Client{Timeout: timeout}
	// The probe loop is also the heartbeat, so the liveness interval is a few
	// probes' worth of slack: a couple of missed probes must not expire the entity.
	liveness := 3 * probe

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	probeAll := func() {
		batch := make([]emit.Entity, 0, len(urls))
		for _, u := range urls {
			batch = append(batch, probeOne(httpClient, u, liveness))
		}
		if err := client.State(ctx, batch...); err != nil {
			log.Printf("emitting state failed (will retry): %v", err)
		}
	}

	probeAll()
	log.Printf("probing %d URL(s) every %s", len(urls), probe)

	ticker := time.NewTicker(probe)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			probeAll()
		case <-ctx.Done():
			delCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			gone := make([]emit.Entity, 0, len(urls))
			for _, u := range urls {
				gone = append(gone, emit.Entity{Type: "service.endpoint", ID: map[string]string{"url": u}})
			}
			_ = client.Delete(delCtx, gone...)
			log.Print("deleted endpoints; bye")
			return nil
		}
	}
}

// probeOne issues one GET and maps the result to an entity. Identity is the URL;
// up / status_code / latency_ms are descriptive (they change every probe).
func probeOne(c *http.Client, url string, liveness time.Duration) emit.Entity {
	start := time.Now()
	resp, err := c.Get(url)
	latency := time.Since(start)

	up := err == nil
	status := 0
	if resp != nil {
		status = resp.StatusCode
		_ = resp.Body.Close()
		up = status < 500
	}
	return emit.Entity{
		Type: "service.endpoint",
		ID:   map[string]string{"url": url},
		Attributes: map[string]string{
			"up":          strconv.FormatBool(up),
			"status_code": strconv.Itoa(status),
			"latency_ms":  strconv.FormatInt(latency.Milliseconds(), 10),
		},
		Interval: liveness,
	}
}
