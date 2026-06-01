// Package debugui serves a minimal, server-rendered HTML view over Toise's read
// model — the same in-memory projection (current state) and event log (history)
// the GraphQL and MCP surfaces use. It is an operator's window into the live
// graph: a dashboard of what exists, entity browsing and detail (identity,
// attributes, neighbors, history), and a recent-changes view. See ADR 0012.
//
// The UI is deliberately minimal: html/template rendered on the server, no
// client-side framework, no external assets or fonts, no JavaScript beyond a
// progressive-enhancement filter submit. It is a debug aid, not a product
// surface, and like the rest of phase 1 it carries no authentication (ADR 0014).
package debugui
