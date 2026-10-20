# ADR 0001. License

**Status:** Accepted (2026-10-18).

**Context.** The project must be 100 % free software, self-hostable, and must prevent closed
derivatives. It is a server with a web UI, so the typical use of a modified version is over the
network.

**Decision.** Code under AGPL-3.0-only (version 3 only, without the "or any later version"
clause). Documentation under CC BY-SA 4.0. No CLA; contributions under the same license. The
panel links to the source code of the running version.

Version 3 only by Diego's decision (2026-10-18). Since there is no CLA, moving later to another
version of the license would require the agreement of every person who has contributed.

**Alternatives.** AGPL-3.0-or-later (admits future versions of the AGPL published by the FSF),
GPL-3.0 (does not cover use over the network), Apache-2.0 or MIT (allow closed derivatives),
MPL-2.0 (weak copyleft). Comparison in `18-documentation-and-license.md` §2.

**Consequences.** Dependencies limited to licenses compatible with AGPLv3, checked in CI. Some
companies will avoid the project; accepted.
