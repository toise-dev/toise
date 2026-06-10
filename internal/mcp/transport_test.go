package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// startHTTP serves the MCP server over the real Streamable HTTP transport and
// returns a connected SDK client session — the exact stack a production MCP
// client (Claude Code/Desktop over HTTP) drives. This layer was at 0% coverage
// and is where the only MCP production incident lived (#64).
func startHTTP(t *testing.T) *mcpsdk.ClientSession {
	t.Helper()
	srv := httptest.NewServer(newTestServer().HTTPHandler())
	t.Cleanup(srv.Close)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "transport-test", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatalf("connect over streamable http: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestStreamableHTTPSession pins the full transport round trip: initialize
// handshake, tool listing, and tool calls with structured output, over real
// HTTP with real session management.
func TestStreamableHTTPSession(t *testing.T) {
	session := startHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 9 {
		t.Fatalf("tools over http = %d, want 9", len(tools.Tools))
	}

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "describe_schema", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call describe_schema: %v", err)
	}
	if res.IsError {
		t.Fatalf("describe_schema returned a tool error: %+v", res.Content)
	}
	raw, merr := json.Marshal(res.StructuredContent)
	if merr != nil {
		t.Fatal(merr)
	}
	if !strings.Contains(string(raw), "host") {
		t.Errorf("describe_schema structured output missing fixture types: %s", raw)
	}

	res, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "find_entities", Arguments: map[string]any{"type": "host"}})
	if err != nil {
		t.Fatalf("call find_entities: %v", err)
	}
	var out FindEntitiesOutput
	raw, _ = json.Marshal(res.StructuredContent)
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("decoding find_entities output: %v", uerr)
	}
	if out.Total != 2 {
		t.Errorf("find_entities over http total = %d, want 2", out.Total)
	}

	// A tool-level error surfaces as an MCP tool error, not a transport error.
	res, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_entity", Arguments: map[string]any{"id": "ghost"}})
	if err != nil {
		t.Fatalf("transport must carry tool errors in-band: %v", err)
	}
	if !res.IsError {
		t.Error("unknown entity must be a tool error over the transport")
	}
}

// TestStreamableHTTPGETStreamStaysOpen pins the #64 regression shape: the
// client's GET listening stream must be accepted (200/SSE), never 405 — a 405
// makes every SDK client treat its session as dead.
func TestStreamableHTTPGETStreamStaysOpen(t *testing.T) {
	srv := httptest.NewServer(newTestServer().HTTPHandler())
	t.Cleanup(srv.Close)

	// Initialize by hand to obtain the session id the GET stream must carry.
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize = %d, want 200", resp.StatusCode)
	}
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("initialize did not return a session id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	greq, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	greq.Header.Set("Accept", "text/event-stream")
	greq.Header.Set("Mcp-Session-Id", sid)
	gresp, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatalf("GET listening stream: %v", err)
	}
	defer func() { _ = gresp.Body.Close() }()
	if gresp.StatusCode == http.StatusMethodNotAllowed {
		t.Fatal("GET listening stream answered 405: SDK clients treat the session as dead (#64)")
	}
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("GET listening stream = %d, want 200", gresp.StatusCode)
	}
}

// TestConcurrentToolCalls is the bounded stress the audit asked for: parallel
// sessions and tool calls over the real transport under -race — the suite was
// previously 100% sequential, so -race validated almost nothing here.
func TestConcurrentToolCalls(t *testing.T) {
	session := startHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const workers, calls = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, workers*calls)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < calls; i++ {
				if _, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
					Name: "find_entities", Arguments: map[string]any{"type": "host"},
				}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent tool call: %v", err)
	}
}
