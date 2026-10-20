# ADR 0019. Experimental export of projects and grades

**Status:** Accepted (2026-10-19, Diego: the experimental export ships in 1.0, off by default and marked "Experimental"). Narrows ADR 0007 without superseding it.

**Context.** Diego wants to download from the panel the grades (`.drx`, the node tree of a
gallery still, PowerGrades included) and the projects (`.drp`) of a library without opening
Resolve. ADR 0007 forbids reading Resolve tables. A study on 2026-10-19 against two real
libraries (`docs/22` §2) showed that both files are the direct serialization of rows of the
library and that they can be reproduced from PostgreSQL: 34 of 34 projects of the local on-disk
library and 5 of 5 of the `apolo` library came out with no differences against what Resolve
21.1.1 exports, and Resolve imported a `.drp` with 11 timelines and several `.drx` generated this
way.

**Decision.** sqsolve reads, never writes, a fixed set of gallery and project tables
(`internal/drp/model.json`), only to produce `.drp` and `.drx`, only if the administrator enables
the feature in Settings (off by default) and always marked "Experimental, no guarantee". The
read runs as `sqsolve_admin` inside a `READ ONLY` transaction with `statement_timeout`. Anything
not observed in a real export makes sqsolve refuse (`SQS-E004`) instead of guessing; a schema
that does not match the model turns the feature off (`SQS-E001`) until the verification kit of
`docs/22` §6 confirms it.

The dependency `github.com/klauspost/compress` v1.20.3 (BSD-3, published 2026-10-12) is
authorized, only its `zstd` package: it is needed to read the order of the media pool folders
and to rewrite the node tree of OFX effects the way Resolve does. Go does not ship zstd and the
project does not use cgo.

**Alternatives.** Letting Resolve export through its scripting API
(`ProjectManager.ExportProject`, `GalleryStillAlbum.ExportStills`): faithful, but it requires a
dedicated workstation with GPU and Studio license, and `SetCurrentDatabase` closes whichever
project is open. Serving the still images: discarded, the DPX files live on the disk of the
workstation that captured them, not in the database. Keeping ADR 0007 without exception: leaves
out a feature Diego asked for that requires writing nothing.

**Consequences.** New `SQS-E` family of codes. The embedded model describes the `.drp` format
(class and field names that appear in any file exported by Resolve), not the database; tests
use a synthetic schema that only repeats those names. Each new Resolve version goes through the
verification kit before being marked verified in `internal/compat/fingerprints.json`. The
`internal/drp` package is not extended to other tables or to writes without another ADR.
