package graphql_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/toise-dev/toise/internal/graphql"
)

// TestWebsocketSubscriptionUpgrades is a regression guard for the subscription
// transport. The per-request timeout used to wrap the entire handler in
// http.TimeoutHandler, whose ResponseWriter is not an http.Hijacker, so every
// WebSocket upgrade failed with HTTP 500 ("response does not implement
// http.Hijacker"). The upgrade must reach the gqlgen handler untouched and
// complete the graphql-transport-ws handshake.
func TestWebsocketSubscriptionUpgrades(t *testing.T) {
	s := newStack(t)
	ts := httptest.NewServer(graphql.NewHandler(s.res, graphql.Config{}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket upgrade failed (Hijacker regression): %v", err)
	}
	defer func() { _ = conn.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101 Switching Protocols", resp.StatusCode)
	}

	// graphql-transport-ws handshake: connection_init -> connection_ack.
	if err := conn.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		t.Fatalf("write connection_init: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ack struct {
		Type string `json:"type"`
	}
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("read connection_ack: %v", err)
	}
	if ack.Type != "connection_ack" {
		t.Fatalf("first message type = %q, want connection_ack", ack.Type)
	}
}
