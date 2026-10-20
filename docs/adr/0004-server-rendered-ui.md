# ADR 0004. Server-rendered UI

**Status:** Accepted (2026-10-18), updated during implementation: no htmx.

**Context.** A modern panel with live states is needed, but the project wants to avoid the npm
supply chain and a second application to maintain.

**Decision.** `html/template` with standard forms and the POST, redirect, GET pattern; a
one-time flash notice in a cookie for the result. Job progress arrives over Server-Sent Events.
The only JavaScript is `app.js`, our own and dependency-free (copy, confirm by typing, live
progress). Assets embedded in the binary. The REST API is a sibling adapter over the same
services.

During implementation it became clear that htmx added nothing that forms and a few dozen lines
of JavaScript did not solve, so it is not vendored.

**Alternatives.** SPA with Svelte, React or Vue; `templ`; htmx (`06-stack.md` §2).

**Consequences.** No Node in the build and no third-party JavaScript. The content security
policy forbids inline styles and scripts: bars and gauges use `<meter>` and `<progress>`, and
the protection timeline is SVG with attributes.
