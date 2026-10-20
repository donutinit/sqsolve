# 22. Project and grade export

Experimental feature (ADR 0019): sqsolve produces `.drp` (project) and `.drx` (grade) files by
reading the library, without going through Resolve. Off by default and marked "Experimental, no
guarantee" everywhere. This document collects what was observed in Resolve 21.1.1 on 2026-10-19,
the rules sqsolve applies and how to verify a new version.

## 1. What it exports and what it does not

| File | Contents | From where |
|---|---|---|
| `.drx` | The node tree of a gallery still and its thumbnail | PowerGrade albums and the stills of each project |
| album `.zip` | One `.drx` per still in the album | One album |
| `.drp` | The whole project: media pool, timelines, grades and gallery, without stills | One project |

Not exported:

- The image of a still. Resolve stores it as DPX in `<GalleryPath>/<project id>/<still
  id>.dpx` on the disk of the workstation that captured it; the database only holds the path.
- A `.drp` with stills. Resolve adds to that file a full-resolution JPEG of each still, which
  comes from the workstation's DPX.
- The thumbnails of timeline clips. Resolve renders them from the media on export and
  regenerates them when the project is opened; sqsolve writes the field empty.

## 2. How Resolve stores these files

A `.drx` is an XML document with the `Gallery::GyStill` row of the still and three nested
objects: two `ListMgt::LmVersion` (the clip version and the track version) whose `Body` field is
the node tree, and a `BtThumnail` with the 288x162 JPEG thumbnail.

A `.drp` is a zip with:

| Entry | Contents |
|---|---|
| `project.xml` | The `SM_Project` row with its configuration, thumbnails, groups and locks |
| `MediaPool/<root>/NNN_<folder>/.../MpFolder.xml` | One entry per media pool folder, in postorder (children before the parent), with a three-digit prefix for the order among siblings |
| `SeqContainer/<id>.xml` | The tracks and items of each timeline, sorted by id |
| `Gallery.xml` | The project gallery: albums and references to stills |

In all documents the element name is the row's `DbType`, the `DbId` attribute is its id and each
field is a child element, in a fixed order per class. Objects a row owns are nested; references
to other objects are written as text. After the XML declaration Resolve writes a comment
`<!--DbAppVer="21.1.1.0010" DbPrjVer="17"-->`. Resolve imports the file even if that comment is
missing (checked with `.drx` on 2026-10-19).

## 3. The model

`internal/drp/model.json` describes the serialization: 41 classes, 49 tables and 14
associations. For each class: its table, its fields in order and, for the non-scalar ones, their
type:

| Type | What it is | Where it comes from |
|---|---|---|
| `obj` | A nested object | The column of the same name holds its id |
| `list:text` | `<Element>` with text | An array column or an association table |
| `list:obj` | `<Element>` with a nested object | An association table or a reverse reference |

Association tables (`<Owner>_<Associate>`) have `DbOwner`, `DbAssociate`, `DbPropertyName` (the
field name) and `DbIndex` (the order). `LocableBlobSet` is the only list resolved by reverse
reference: the `BtLockableBlob` rows point to their map with `BtLockableBlobMap_id`.

The model was learned from 41 exports made by Resolve 21.1.1 with its scripting API: 34 from the
local library on disk and 5 from the `apolo` library on PostgreSQL, plus 2 with stills to learn
the `.drx`. It contains only class and field names that appear in any file exported by Resolve,
not the database schema.

## 4. Format rules and rewrites

Values:

- A null uuid is omitted, except `Gallery::GyStillRef.Ref0`, `Gallery::GyStillRef.Ref1`,
  `SM_Config.User`, `SM_Project.Folder` and `SM_Project.ProjRef`, which are written as an empty
  element.
- Booleans `true` and `false`.
- Decimals in the shortest representation that reproduces the value, with no exponent or `.0`.
  Checked only with values between 66 and 85784; outside that range, to be confirmed.
- Dates with three decimals (`2026-10-19T13:37:24.830`).
- `bytea` in hexadecimal, except `Sm2MpMedia.SlateTC`, which stores text.
- `{a,b}` arrays are written as one `<Element>` per value.
- Escaping of `&`, `<`, `>` and `"` like Qt's XML writer. For `"`: to be confirmed, it did not
  appear in any export.

Rewrites that Resolve's exporter does and sqsolve repeats:

- `SM_Project.UpToDate` is `false` when the project has media or timelines.
- `RenderTextEnabled`, `RenderTextGanged` and `RenderTextPrefixed` of `Sm2TiAudioClip` are
  `false`.
- `SM_Config.Name` is `<project name>.Cfg`.
- `SM_Project.StreamCursorVector` is left as zeros.
- Inside `ListMgt::LmVersion.Body`, the uuid of `OfxImageEffectContextFilter_<uuid>_` is
  rewritten with the version id, only in the media pool (4 cases observed).
- The order of media pool subfolders is in field 2 of the version 2 `FieldsBlob` of the parent
  folder: zstd, protobuf and a QDataStream map from index to id. If it is missing, sqsolve sorts
  by name and notes it in the download.

## 5. What is not reproduced

Values that not even Resolve keeps equal between two consecutive exports, so sqsolve writes what
is in the database: `LastChangedTime` inside the `FieldsBlob` of `ListMgt::LmVersionTable`, a
subfield of `Body`, `SetupBA` of `SM_Config`, `RenderCacheBA` and `AuxRenderCacheBA`. The order
of `LocableBlobSet` comes out by hash in Resolve; sqsolve sorts it by id. The thumbnails of
timeline clips are empty (§1).

Guards: the exporter refuses with `SQS-E004` if the project uses `FolderMap`, the old
`GroupList` of `SM_Project`, `FaceTaggingClipVec`, `FaceTaggingPeopleVec`, `PowerNodes` (shared
nodes), `SmartFolderVec`, `MatteVec` (external mattes), `GeometryTrackVec`,
`FusionCompHolderItems` or `LayersVec`. None appeared filled in an export, so it is not known how
Resolve writes them.

## 6. Validation and verification kit

Results of 2026-10-19 with Resolve 21.1.1:

- 34 of 34 projects of the local library and 5 of 5 of `apolo` with no canonical differences
  (the decoded blobs, discounting the volatile parts of §5).
- 388 and 70 XML documents identical byte for byte except the volatile parts.
- Imported into Resolve: a `.drp` with 11 timelines and `.drx` files with and without the
  version comment.

Kit for a new Resolve version:

1. Export the test projects with sqsolve:

   ```
   SQSOLVE_DRP_DSN=postgres://user:password@server/library \
   SQSOLVE_DRP_OUT=/path/output \
   SQSOLVE_DRP_PROJECTS='folder/project,other' \
   go test ./internal/drp -run TestCorpus -v
   ```

2. Export the same projects from Resolve with the scripting API:
   `ProjectManager.ExportProject(name, path, False)`.
3. Compare the XML documents of both zips discounting the volatile parts of §5; any other
   difference is a format change that must be understood before continuing.
4. Import into Resolve a `.drp` and a `.drx` generated by sqsolve and open them.
5. Record the fingerprint in `internal/compat/fingerprints.json`:

   ```json
   "sha256:...": {
     "resolve": "21.1.1",
     "app_version": "21.1.1.0010",
     "project_version": 17,
     "verified": ["drx", "drp"],
     "checked": "2026-10-19"
   }
   ```

   `app_version` and `project_version` come from the comment Resolve writes (§2); `verified`
   lists what passed the kit.

Fingerprint verified today: `sha256:2bf9e02acd6f8389b240fddf0cb312b8a7b6a72dff48d8101609e6a80f150fee`,
Resolve 21.1.1 (`21.1.1.0010`), `DbPrjVer` 17.

## 7. UI, API and permissions

**Export** tab in the library detail, with the notice "Experimental: no guarantee" and the
fingerprint status: "verified with Resolve 21.1.1" or "not verified". A library can hold
hundreds of projects, so the catalog shows one view at a time, each with a search box:

- **Projects**: table with thumbnail, name, folder, modification date, how many stills the
  project has and a "Download .drp" button. The still count links to the stills of that project
  alone.
- **Stills**: PowerGrade albums and the album of each project, collapsed, with their
  thumbnails, a ".drx" button per still and "Download album (.zip)". Narrowed to one project
  the albums come expanded; a search that leaves five project albums or fewer expands them too.

Query string: `view=projects|stills` (projects by default), `q=` (case-insensitive substring of
the project name or folder, or of the album name, project name or still label), and
`project=<id>`, which narrows the stills view to that project. The same applies to the export
from a backup.

| Web route | Action |
|---|---|
| `GET /libraries/{id}/export` | Tab; `?view=`, `?q=` and `?project=` as above |
| `GET /libraries/{id}/export/projects/{pid}` | `.drp` download |
| `GET /libraries/{id}/export/stills/{sid}` | `.drx` download |
| `GET /libraries/{id}/export/albums/{vid}` | `.zip` download |
| `GET /backups/{id}/export` | Export from a backup: state and catalog |
| `POST /backups/{id}/export/prepare` | Restores the backup into a temporary database (`backup.export` job) |
| `POST /backups/{id}/export/close` | Drops the temporary database |
| `GET /backups/{id}/export/projects/{pid}`, `.../stills/{sid}`, `.../albums/{vid}` | Downloads from the backup |

**Export from a backup.** To recover a project or a grade as it was in a backup, without
restoring the library: the `backup.export` job verifies the file (L1) and restores it into a
temporary database `sqsolve_export_<id>` owned by `sqsolve_admin` with a limit of 4 connections;
the workspace (`export_workspaces`) stays `ready` for two hours from the last use and the export
session reads it just like a live library. "Close" drops it; the nightly cleanup drops expired
ones and a start drops any orphaned `sqsolve_export_*`. File names carry the library identifier
and the backup date. Each download is recorded in the audit log as `backup.export`.

| API route | Minimum scope |
|---|---|
| `GET /libraries/{id}/projects` | `read` |
| `GET /libraries/{id}/projects/{pid}/export` | `export` |
| `GET /libraries/{id}/grades` | `read` |
| `GET /libraries/{id}/grades/{sid}/export` | `export` |
| `GET /libraries/{id}/grades/albums/{vid}/export` | `export` |
| `GET /backups/{id}/export` | `read`: workspace state and, if ready, projects and albums |
| `POST /backups/{id}/export` | `export`: prepares (202 with the job) |
| `DELETE /backups/{id}/export` | `export`: closes |
| `GET /backups/{id}/export/projects/{pid}`, `.../grades/{sid}`, `.../grades/albums/{vid}` | `export` |

`library.export` action for operator and administrator; `export` token scope. Each download is
recorded in the audit log with type, id, name, files, bytes, fingerprint and the export notes.

## 8. Codes

| Code | Meaning |
|---|---|
| `SQS-E001` | The library does not have the schema the model knows |
| `SQS-E002` | The feature is disabled in Settings |
| `SQS-E003` | The project, still or album does not exist |
| `SQS-E004` | The project uses a feature the export does not reproduce |
| `SQS-E005` | The library is unavailable or the read failed |
| `SQS-E006` | An unexpected value or format |
| `SQS-E007` | The backup is not prepared for export (the temporary database is missing or expired) |

## 9. Limits and risks

- The read runs as `sqsolve_admin` inside a `READ ONLY` transaction with a `statement_timeout` of
  60 s.
- There is no queued job: the download is synchronous and a large project takes seconds.
- A new Resolve version that changes the schema turns the feature off with `SQS-E001` until the
  kit of §6 verifies it; one that changes the format without changing the schema is only detected
  with the kit (risk T-16 of `19-risks.md`).
