// Package mcp exposes Toise as a Model Context Protocol server, built on the
// official Go SDK (github.com/modelcontextprotocol/go-sdk). It is the path an
// LLM takes to reach Toise directly, alongside the sibling GraphQL API; both
// read from the same in-memory projection (current state) and event log
// (history), so the two surfaces report the same world. See ADR 0011.
//
// The server registers a small set of typed tools. Each tool's input and
// output are Go structs: the SDK infers and validates their JSON schema, so
// input validation is a property of the type rather than hand-written checks.
// Tool outputs carry human-readable labels alongside ids and types so the model
// can reason without a second lookup, and tool errors are plain, user-friendly
// messages rather than stack traces.
//
// The same server is served over two transports: stdio (for Claude Desktop and
// other local clients) and Streamable HTTP (for web-based LLM clients).
package mcp
