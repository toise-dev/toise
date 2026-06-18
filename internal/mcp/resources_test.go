package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func readURI(t *testing.T, s *Server, uri string) (*mcpsdk.ReadResourceResult, error) {
	t.Helper()
	return s.readResource(context.Background(), &mcpsdk.ReadResourceRequest{
		Params: &mcpsdk.ReadResourceParams{URI: uri},
	})
}

func TestReadResourceSchema(t *testing.T) {
	s := newTestServer()
	res, err := readURI(t, s, "toise://schema")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if len(res.Contents) != 1 || res.Contents[0].MIMEType != "application/json" {
		t.Fatalf("unexpected schema contents: %+v", res.Contents)
	}
	var out DescribeSchemaOutput
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &out); err != nil {
		t.Fatalf("schema is not the describe_schema JSON: %v", err)
	}
	if out.TotalEntities != 3 || out.Description == "" {
		t.Errorf("schema resource must mirror describe_schema, got %+v", out)
	}
}

func TestReadResourceGuide(t *testing.T) {
	s := newTestServer()
	res, err := readURI(t, s, "toise://guide")
	if err != nil {
		t.Fatalf("read guide: %v", err)
	}
	if res.Contents[0].MIMEType != "text/markdown" || !strings.Contains(res.Contents[0].Text, "Querying Toise") {
		t.Errorf("unexpected guide: %+v", res.Contents[0])
	}
}

func TestReadResourceEntityTemplate(t *testing.T) {
	s := newTestServer()
	res, err := readURI(t, s, "toise://entity/01HOST_WEB")
	if err != nil {
		t.Fatalf("read entity: %v", err)
	}
	var out GetEntityOutput
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &out); err != nil {
		t.Fatalf("entity resource is not get_entity JSON: %v", err)
	}
	if out.Entity.Type != "host" {
		t.Errorf("entity resource mismatch: %+v", out.Entity)
	}
}

func TestReadResourceErrors(t *testing.T) {
	s := newTestServer()
	cases := []string{
		"toise://entity/",     // no id
		"toise://entity/nope", // unknown id
		"toise://bogus",       // unknown resource
		"http://x",            // wrong scheme
	}
	for _, uri := range cases {
		if _, err := readURI(t, s, uri); err == nil {
			t.Errorf("expected error reading %q", uri)
		}
	}
}
