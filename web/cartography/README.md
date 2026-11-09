# web/cartography — the Cartography brick of the portal

This is the page the portal serves under `/cartography/`. It is **derived from
`examples/graph-viz`** and deliberately kept separate: the example stays a generic,
runnable example of a Toise consumer, and this build is specific to one deployment.

# web/cartography — the Cartography brick of the portal

This is the page the portal serves under `/cartography/`. It is **derived from
`examples/graph-viz`** and deliberately kept separate: the example stays a
generic, runnable example of a Toise consumer, and this build is specific to
one deployment.

**It is not a drop-in replacement for the example**, and it is not meant to be
run on its own. Four things were removed on purpose, each one for a reason
worth keeping in mind if this file is ever re-derived from the example:

1. **No GraphQL text leaves the browser.** The relay holds the documents and
   accepts named persisted queries only — `{"id":"entities","variables":{…}}`.
   The example built every request by string interpolation, so each page of a
   paginated result was a different document and no fixed list could ever have
   matched it. The three documents, their variables and the validation the
   relay should apply are in `MANIFESTE-PORTAIL.md`.
2. **No subscriptions.** The example opened a websocket and fell back to
   polling; only the fallback remains. Polling also stops while the tab is
   hidden, because a portal tab stays open for hours and reloading the whole
   graph every six seconds for nobody is load on the server for nothing.
3. **No tenant, anywhere.** No `?tenant=` argument, no `X-Scope-OrgID` header,
   and no text field in the toolbar inviting a reader to type another
   tenant's id. The relay imposes the tenant from the portal session.
4. **A fixed endpoint.** `/cartography/graphql`, with the example's `?api=`
   override removed: in an authenticated frame, an endpoint settable from the
   URL lets a reader point the page somewhere else.

## What came back, and at what price

The as-of timeline is back, but a read at a past instant is a **full replay of
the tenant's journal**, and replays are bounded for the whole process, across
all tenants and all surfaces. Measured in production: two hours back costs
**0.91 s** on a fleet of 2,328 entities and **4.67 s** on a fleet of 1,066 —
the smaller one is five times slower, because the cost follows the **journal**,
not the distance. Hence five limits:

- reads happen **on handle release**, never during the drag (the `change`
  event, not `input`, so the platform enforces it itself);
- **one replay in flight** at a time;
- instants snap to a **five-minute grid**, because producers report on tics of
  a few minutes;
- the served instant and the horizon are **displayed**, the horizon being the
  one the server announces;
- a **503** is read as "too loaded, retry", never as an empty fleet.

A failed read **restores the previous instant**, otherwise the page would
claim an instant it did not load. The brush that zoomed the density band
remains **removed**: it would select an arbitrary period while the profile
window is taken from a closed list.

## What the deployment must place

`vendor/vis-network.min.js`, beside this page. **vis-network 9.1.9**, whose published
bundle has the integrity
`sha384-yxKDWWf0wwdUj/gPeuL11czrnKFQROnLgY8ll7En9NYoXibgg3C6NK/UDHNtUgWJ` — the page
still carries that hash even same-origin, so a substituted or re-minified file fails
loudly in the console instead of leaving a blank screen.

It is served locally rather than from a CDN because a portal host may have no egress,
and a CDN that cannot be reached is the first thing that would fail, silently.
