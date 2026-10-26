# PoC-03. Schema fingerprint

Date: 2026-10-19. Server: mininas, sqsolve 0.1.5, PostgreSQL 18.6. Workstation: Linux, Resolve
Studio 21.1.1.10, library `apolo` (136 tables), operated through Resolve's scripting API.

## Procedure and literal results

1. Fingerprint saved before: `sha256:2bf9e02acd6f8389b240fddf0cb312b8a7b6a72dff48d8101609e6a80f150fee`,
   136 tables (API `GET /libraries/{id}`).
2. Normal use from Resolve: `CreateProject("poc03_huella")`, `CreateEmptyTimeline`,
   `SetSetting("timelineFrameRate", "25")`, `SaveProject`, `CloseProject`; then `LoadProject`,
   more saves and `DeleteProject`.
3. "Back up now" (the fingerprint is recalculated on every backup): manual backup
   `01M5BDTRHX...`, `ok`, `l1_ok`.
4. Fingerprint after: `sha256:2bf9e02acd6f8389b240fddf0cb312b8a7b6a72dff48d8101609e6a80f150fee`,
   136 tables. Unchanged.

The part about "upgrading the library with a new version of Resolve" could not be done: only
21.1.1 is available on the workstation. It remains REPORTED that an upgrade changes the schema.

## Decisions it enables

- The fingerprint is stable under normal use (creating, editing, saving and deleting projects and
  timelines): a fingerprint change means a structure change, not a data change. Upgrade
  detection by fingerprint (`docs/07` §9) is validated in its "no false positives" half.
- What is missing (a version upgrade that does change the schema) is not recorded as of 1.2.2:
  until it is, the upgrade window in `docs/14` §5 remains mandatory in the guide.

## Status

VERIFIED: stable under use. REPORTED, not tested as of 1.2.2: changes when Resolve is upgraded.
