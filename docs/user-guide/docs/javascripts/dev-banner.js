/* Shows an "unreleased / development" banner only on the `dev` mike version.
   Material only warns about versions OLDER than latest, so we add our own for
   the in-progress docs. Version is detected from the URL, and the "latest" link
   is derived from the mike base path so it works both under `mike serve`
   (/dev/...) and the real deploy (/docs/dev/...). */
(function () {
  function init() {
    var isDev = location.pathname.split("/").indexOf("dev") !== -1;
    var existing = document.querySelector(".dev-banner");

    if (!isDev) {
      if (existing) existing.remove();
      return;
    }
    if (existing) return;

    var base = location.pathname.split("/dev/")[0]; // "" or "/docs"

    var banner = document.createElement("aside");
    banner.className = "md-banner dev-banner";

    var inner = document.createElement("div");
    inner.className = "md-banner__inner md-grid md-typeset";
    inner.appendChild(
      document.createTextNode(
        "You're reading the development docs (unreleased) — features may change before release. "
      )
    );

    var link = document.createElement("a");
    link.href = base + "/latest/";
    link.textContent = "View the latest release →";
    inner.appendChild(link);

    banner.appendChild(inner);

    var header = document.querySelector(".md-header");
    if (header) document.body.insertBefore(banner, header);
    else document.body.insertBefore(banner, document.body.firstChild);
  }

  if (typeof document$ !== "undefined" && document$.subscribe) {
    document$.subscribe(init);
  } else {
    document.addEventListener("DOMContentLoaded", init);
  }
})();
