package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/annotations"
	"github.com/toise-dev/toise/internal/auth"
)

// annotateOverTransport calls annotate_entity through the real MCP protocol with
// the server session connected under serverCtx, and reports what the tool
// answered.
func annotateOverTransport(t *testing.T, serverCtx context.Context) (refusal string) {
	t.Helper()
	s := newTestServer()
	store, err := annotations.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open annotations: %v", err)
	}
	defer func() { _ = store.Close() }()
	s.SetAnnotations(store)

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ss, err := s.srv.Connect(serverCtx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Wait() }()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "probe", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "annotate_entity",
		Arguments: map[string]any{
			"entity_id":   "01HOST_WEB",
			"annotations": map[string]any{"owner": "sre"},
		},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !res.IsError {
		return ""
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			return tc.Text
		}
	}
	return "tool reported an error with no text"
}

// TestWriteDecisionOnTheSessionContextReachesTheTool is the guarantee the stdio
// wiring rests on, and it is not obvious: stdio runs no HTTP middleware, so the
// write decision is recorded once on the context handed to the MCP server, and
// nothing proves by inspection that the SDK carries that context down to a tool
// handler. Without this, the stdio knob would be decorative and annotate_entity
// would be refused there with no way to see why.
func TestWriteDecisionOnTheSessionContextReachesTheTool(t *testing.T) {
	if refusal := annotateOverTransport(t, auth.WithWritable(context.Background(), true)); refusal != "" {
		t.Errorf("a write decision recorded on the session context must reach the tool, got refusal: %s", refusal)
	}
	refusal := annotateOverTransport(t, auth.WithWritable(context.Background(), false))
	if refusal == "" {
		t.Fatal("a refused write decision must refuse the annotation")
	}
	// The refusal has to name the no-authentication cause too: on a hardened
	// instance with no token at all, "use a full-role token" sends the operator
	// after a credential that would not help.
	if !strings.Contains(refusal, "allow_unauthenticated_writes") {
		t.Errorf("the refusal must name the knob that actually governs it, got: %s", refusal)
	}
	if refusal := annotateOverTransport(t, context.Background()); refusal == "" {
		t.Error("a context carrying no decision at all must refuse the annotation")
	}
}
