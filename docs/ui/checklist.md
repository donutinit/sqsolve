# UI checklist

Panel pages with `pagehead` (decided by what the reference renders: a `pagehead` header with a
title and actions, and `card` sections below it). Reference: `library.html`, the most recently
changed. 14 members out of 29 candidates in `internal/web` (templates, `static/app.css`,
`funcs.go`), served by `cmd/sqsolve serve`. Read on 2026-10-18 with the patterns phase of
`ui-consistency`.

Members: account, activity, backup, backups, dashboard, health, import, job, jobs, libraries,
library, library_export, library_new, restore, settings. Non-members (15): single-card
`card narrow` pages without `pagehead` (confirmations, access, kit, error, instructions); they
follow the same rules for badges, notices, dates and CSRF.

It is a snapshot: if this list and the code disagree, the code wins and the count is redone.

- [ ] Container: the page only defines `content`. `layout.html` provides the navigation, the
      single-read notice (`{{template "flash" .}}`) and `<main id="content">`. 14 of 14.
- [ ] Header: `<div class="pagehead">` with a single `<h1>`; the main action goes on the right,
      always inside `<div class="actions">`. `h1` in 14 of 14; action in the header in 6 of 14, 3
      of them loose until 2026-10-18 (now 6 of 6 in `div.actions`; no majority before, the
      reference decided, level 7).
- [ ] Sections: `<section class="card">` with `<h2>` as the title; never jump from `h1` to
      `h3`. 61 sections in 14 of 14 files; 52 of 61 with `h2`. The 9 without a title: the
      `flush` table that is the whole page (3), the main card under the `h1` (import, library_new
      and the progress one in job), a form with a `legend` (general settings), a `details` with
      its `summary` and a one-line notice.
- [ ] State: always the `badge` partial (`dict "tone" "label"`) or the ones that wrap it
      (`jobstate`, `librarystate`), which set text and shape (circle, diamond, square, outline).
      Never a hand-written `<span class="badge">`. Shared piece (level 4): before, 15 uses in 8
      of 14 files against 13 by hand in 9 files, 8 of them without a shape; fixed on 2026-10-18.
      Exception: the `a.badge` links of the top bar, written by hand because the partial does not
      make links, with their `span.dot`.
- [ ] In-page notice: `<div class="flash TONE" role="...">`, with `alert` only for `error` and
      `status` for the rest, the same rule as the `flash` partial (level 4). 10 notices in 6
      files; the 2 that had no `role` were fixed. Since 2026-10-19 the notice carries the same
      shape as the badges (`::before` per tone: circle, diamond, square, outline) and a thin
      border around it; the colored side stripe was removed. Since 2026-10-25 the notice is one
      block of text with the shape placed in its left padding, not a flex container: inline
      parts (a list of connections, `code`, a link) wrap inside it and long values break
      (PLAN 1.2.7, `TestNoticesAreOneTextBlock`).
- [ ] Targets: every control takes taps on at least 44 x 44 px at every width (WCAG 2.5.5).
      Buttons, small buttons, "Sign out", the account link, the badges of the top bar and
      "more" toggles keep their look and get an invisible `::after` area; sidebar links, tabs
      and fields are 44 px tall themselves. The area is not used inside containers that scroll
      sideways (it would add a scroll bar), and wrapped rows of table actions keep 14 px
      between them so areas do not overlap (`TestControlsHave44pxTargets`).
- [ ] Wide tables: inside a normal card they are wrapped in `<div class="scroll-x">` to scroll
      sideways on narrow screens (7 tables on 2026-10-19); `cards-on-mobile` tables that are the
      whole page go in `card flush`, which already scrolls.
- [ ] Tables shown as cards on narrow screens (`cards-on-mobile`): every element carries its
      table role (`table`, `rowgroup`, `row`, `columnheader`, `cell`), because changing the
      display of rows and cells makes some browsers drop the table semantics; the header is
      hidden only visually; each cell has `data-label`, drawn with empty alternative text so it
      is not read twice. 8 of 8 tables since 2026-10-22 (audit AUD-009), checked by
      `internal/web/a11y_test.go`.
- [ ] Action list with explanation: `<div class="actionrow">` with `h3`, a paragraph saying what
      it does and a short button ("Run"); never a long button that describes the action and
      wraps onto two lines (maintenance, 7 actions, fixed on 2026-10-19).
- [ ] Empty: a short sentence with `.muted` naming what is missing ("No events."), in the
      container element: `p` in a section or under the header (11), `li` in a list and `td
      colspan` in a table (5). Every list or table has its empty state (the audit log did not;
      fixed). The `empty` partial, with explanation and action, is only for the first use with no
      libraries (2 uses).
- [ ] Forms: `{{template "csrf" .CSRF}}` is the first field, mandatory in `multipart` forms
      because the server reads it before the file. 48 of 48 through the partial. Main button
      `<button type="submit">` with no class (21); secondary `ghost` (13); main destructive
      `danger`; secondary destructive `ghost danger` (2) and, inside tables, `ghost small
      danger`; `warn` for revert and for maintenance (2). Every destructive action asks for
      confirmation: `data-confirm` or typing the identifier (8 of 8; "Revoke" token did not ask
      for it, fixed).
- [ ] Dates: always `dt`, `ago` or `until`, which use the server's time zone; never `.Format` in
      a template. 37 of 37 in the pages, plus 4 in the partials. A `<time>` carries
      `datetime="{{iso ...}}"`.
- [ ] Words: one name for each thing across the whole panel, and state labels in English with an
      initial capital (never the internal code such as `ok` or `denied`): "Events" (not
      "Notifications", which is a severity), "Storage", "Connected" for the time of a session,
      "Trash" and "Purged", "PostgreSQL not responding", "Remove" the second factor ("Disabled"
      is only a disabled account), "OK", "Warning" and "Failed" for results, "the address
      workstations use". Fixed on 2026-10-18 after the review.
- [ ] Values: colors only through the `:root` variables in `app.css` (`--text`, `--muted`,
      `--ok`, `--warn`, `--bad`, `--run`, `--on-run`, `--on-danger`, and `--brand-in` and
      `--brand-out` only for the brand ports and the navigation icons), which also define the
      dark theme; three literal colors became variables on 2026-10-18. No inline styles, which
      the CSP blocks: 0 `style=`. Spacing has no base (literals from 1 to 56 px): reuse the
      ones already written, mostly 8, 10, 12 and 16 px. Typography in `rem`: `h1` 1.5rem weight
      650, `h2` 1.15rem weight 600 (1.07rem until 2026-10-19), `h3` 1rem weight 600, `.hint`
      help 0.87rem in `--muted`. Transitions with `--ease-out`, never the browser's `ease`.
- [ ] Not copied from `library.html`: its seven tabs, the 30-day timeline and the data grid for
      copying (`copy`), which only it has.

Searches: line-by-line grep of the exact names (`class="pagehead`, `<h1>`,
`<section class="card`, `<h2`, `class="tabs`, `{{template "badge"`, `class="badge`,
`class="flash`, `{{template "csrf"`, `name="csrf"`, `{{template "empty"`, `{{dt `, `{{ago `,
`{{until `, `style=`) over the 14 member files, each tried first on `library.html`. Left out,
with no convention to count: the tabs, which only 5 of 14 pages have, each with its own.
