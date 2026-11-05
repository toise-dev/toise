# web/cartography — the Cartography brick of the portal

This is the page the portal serves under `/cartography/`. It is **derived from
`examples/graph-viz`** and deliberately kept separate: the example stays a generic,
runnable example of a Toise consumer, and this build is specific to one deployment.

**It is not a drop-in replacement for the example**, and it is not meant to be run on
its own. Five things were removed on purpose, each one for a reason worth keeping in
mind if this file is ever re-derived from the example:

1. **No GraphQL text leaves the browser.** The relay holds the documents and accepts
   named persisted queries only — `{"id":"entities","variables":{…}}`. The example built
   every request by string interpolation, so each page of a paginated result was a
   different document and no fixed list could ever have matched it. The three documents,
   their variables and the validation the relay should apply are in
   `MANIFESTE-PORTAIL.md`.
2. **No subscriptions.** The example opened a websocket and fell back to polling; only
   the fallback remains. Polling also stops while the tab is hidden, because a portal tab
   stays open for hours and reloading the whole graph every six seconds for nobody is
   load on the server for nothing.
3. **No as-of timeline.** Every drag of it was a read at a past instant, whose cost
   follows the journal size and which answers 503 under ingestion load. Hiding it needed
   an explicit CSS rule: an author `display:flex` beats the browser's own `[hidden]`, so
   the attribute alone left the whole control clickable.
4. **No tenant, anywhere.** No `?tenant=` argument, no `X-Scope-OrgID` header, and no
   text field in the toolbar inviting a reader to type another tenant's id. The relay
   imposes the tenant from the portal session.
5. **A fixed endpoint.** `/cartography/graphql`, with the example's `?api=` override
   removed: in an authenticated frame, an endpoint settable from the URL lets a reader
   point the page somewhere else.

## What the deployment must place

`vendor/vis-network.min.js`, beside this page. **vis-network 9.1.9**, whose published
bundle has the integrity
`sha384-yxKDWWf0wwdUj/gPeuL11czrnKFQROnLgY8ll7En9NYoXibgg3C6NK/UDHNtUgWJ` — the page
still carries that hash even same-origin, so a substituted or re-minified file fails
loudly in the console instead of leaving a blank screen.

It is served locally rather than from a CDN because a portal host may have no egress,
and a CDN that cannot be reached is the first thing that would fail, silently.
