// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ExportInfo describes a finished export.
type ExportInfo struct {
	// Name is the project name or the still label.
	Name string
	// Files is the number of documents written.
	Files int
	// Bytes is the size written to the caller.
	Bytes int64
	// Notes lists deviations from what Resolve would write.
	Notes []string
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// ExportProject writes the .drp of a project.
func (s *Session) ExportProject(ctx context.Context, id string, w io.Writer) (*ExportInfo, error) {
	id, ok := ValidID(id)
	if !ok {
		return nil, fmt.Errorf("%w: invalid project identifier", ErrNotFound)
	}
	g := newGraph()
	if err := s.load(ctx, g, "SM_Project", []string{id}); err != nil {
		return nil, err
	}
	prj := g.get("SM_Project", id)
	if prj == nil {
		return nil, fmt.Errorf("%w: project %s", ErrNotFound, id)
	}
	name, _ := prj.vals["ProjectName"].(string)
	pool, _ := prj.vals["MediaPool"].(string)
	if pool == "" {
		pool, _ = prj.vals["pMediaPool"].(string)
	}
	folders, err := s.folderTree(ctx, pool)
	if err != nil {
		return nil, err
	}
	var folderIDs []string
	for _, f := range folders {
		folderIDs = append(folderIDs, f.id)
	}
	if err := s.load(ctx, g, "Sm2MpFolder", folderIDs); err != nil {
		return nil, err
	}
	var seqIDs []string
	for _, r := range g.ofTable("Sm2Sequence") {
		seqIDs = append(seqIDs, r.id)
	}
	containers, err := s.containersOf(ctx, seqIDs)
	if err != nil {
		return nil, err
	}
	if err := s.load(ctx, g, "Sm2SequenceContainer", containers); err != nil {
		return nil, err
	}
	gallery, _ := prj.vals["pGallery"].(string)
	if gallery == "" {
		if gallery, err = s.galleryOf(ctx, id); err != nil {
			return nil, err
		}
	}
	if gallery != "" {
		if err := s.load(ctx, g, "Gallery::GyGallery", []string{gallery}); err != nil {
			return nil, err
		}
	}

	e := &emitter{m: s.m, g: g, projectName: name}
	e.hasContent = len(g.associates("SM_Project_Sm2Timeline", "TimelineHandleVec", id)) > 0
	for _, f := range folders {
		if len(g.associates("Sm2MpFolder_Sm2MpMedia", "MediaVec", f.id)) > 0 {
			e.hasContent = true
		}
	}
	prjVer, _ := prj.vals["ProjectVersion"].(int32)
	header := s.Header(int(prjVer))

	cw := &countingWriter{w: w}
	zw := zip.NewWriter(cw)
	now := time.Now()
	info := &ExportInfo{Name: name}
	add := func(path string, table, id string) error {
		doc, err := e.document(header, table, id)
		if err != nil {
			return err
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: path, Method: zip.Deflate, Modified: now, NonUTF8: true})
		if err != nil {
			return err
		}
		if _, err := fw.Write(doc); err != nil {
			return err
		}
		info.Files++
		return nil
	}
	if err := add("project.xml", "SM_Project", id); err != nil {
		return nil, err
	}
	byParent := map[string][]*folder{}
	var roots []*folder
	for _, f := range folders {
		if f.parent == "" {
			roots = append(roots, f)
		} else {
			byParent[f.parent] = append(byParent[f.parent], f)
		}
	}
	e.inMediaPool = true
	var walk func(f *folder, path string) error
	walk = func(f *folder, path string) error {
		kids := e.orderFolders(g, f, byParent[f.id])
		for i, k := range kids {
			if err := walk(k, fmt.Sprintf("%s/%03d_%s", path, i, k.name)); err != nil {
				return err
			}
		}
		return add(path+"/MpFolder.xml", "Sm2MpFolder", f.id)
	}
	for _, r := range roots {
		if err := walk(r, "MediaPool/"+r.name); err != nil {
			return nil, err
		}
	}
	e.inMediaPool = false
	for _, c := range containers {
		if err := add("SeqContainer/"+c+".xml", "Sm2SequenceContainer", c); err != nil {
			return nil, err
		}
	}
	if gallery != "" {
		if err := add("Gallery.xml", "Gallery::GyGallery", gallery); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	info.Bytes = cw.n
	info.Notes = e.notes
	return info, nil
}

type folder struct {
	id, name, parent string
	saved            int64
}

// folderTree lists the media pool folders of a pool: the roots and every descendant.
func (s *Session) folderTree(ctx context.Context, pool string) ([]*folder, error) {
	if pool == "" {
		return nil, nil
	}
	const cols = `SELECT "Sm2MpFolder_id"::text, COALESCE("Name", ''), COALESCE("MpFolder"::text, ''), COALESCE("DbSavedTime", 0) FROM "Sm2MpFolder" WHERE `
	scan := func(q string, args ...any) ([]*folder, error) {
		rows, err := s.tx.Query(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("SQS-E005 reading Sm2MpFolder: %w", err)
		}
		defer rows.Close()
		var out []*folder
		for rows.Next() {
			f := &folder{}
			if err := rows.Scan(&f.id, &f.name, &f.parent, &f.saved); err != nil {
				return nil, err
			}
			out = append(out, f)
		}
		return out, rows.Err()
	}
	all, err := scan(cols+`("MpFolder" IS NULL) AND ("Sm2MediaPool_id"::text = $1 OR "MediaPool"::text = $1) ORDER BY 1`, pool)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	level := all
	for len(level) > 0 {
		var ids []string
		for _, f := range level {
			if !seen[f.id] {
				seen[f.id] = true
				ids = append(ids, f.id)
			}
		}
		if len(ids) == 0 {
			break
		}
		level = nil
		for _, chunk := range chunks(ids) {
			kids, err := scan(cols+`"MpFolder"::text = ANY($1) ORDER BY 1`, chunk)
			if err != nil {
				return nil, err
			}
			for _, k := range kids {
				if !seen[k.id] {
					level = append(level, k)
					all = append(all, k)
				}
			}
		}
	}
	return all, nil
}

// orderFolders returns the children of f in the order Resolve keeps in the
// parent's FieldsBlob, or by name (with a note) when that order is missing.
func (e *emitter) orderFolders(g *graph, f *folder, kids []*folder) []*folder {
	if len(kids) < 2 {
		return kids
	}
	var order []string
	if r := g.get("Sm2MpFolder", f.id); r != nil {
		if blob, ok := r.vals["FieldsBlob"].([]byte); ok {
			var err error
			if order, err = childOrder(blob); err != nil {
				e.note("folder %q: could not read the order of its subfolders (%v)", f.name, err)
			}
		}
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	complete := len(order) > 0
	for _, k := range kids {
		if _, ok := pos[k.id]; !ok {
			complete = false
		}
	}
	out := append([]*folder(nil), kids...)
	if complete {
		sort.SliceStable(out, func(i, j int) bool { return pos[out[i].id] < pos[out[j].id] })
		return out
	}
	e.note("folder %q: its subfolders are in alphabetical order because Resolve did not store the order", f.name)
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (s *Session) containersOf(ctx context.Context, seqIDs []string) ([]string, error) {
	var out []string
	for _, chunk := range chunks(seqIDs) {
		rows, err := s.tx.Query(ctx, `SELECT "Sm2SequenceContainer_id"::text FROM "Sm2SequenceContainer" WHERE "Sm2Sequence_id"::text = ANY($1)`, chunk)
		if err != nil {
			return nil, fmt.Errorf("SQS-E005 reading Sm2SequenceContainer: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Session) galleryOf(ctx context.Context, projectID string) (string, error) {
	var id string
	err := s.tx.QueryRow(ctx, `SELECT "Gallery::GyGallery_id"::text FROM "Gallery::GyGallery" WHERE "SM_Project_id"::text = $1 ORDER BY 1 LIMIT 1`, projectID).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return "", nil
		}
		return "", fmt.Errorf("SQS-E005 reading Gallery::GyGallery: %w", err)
	}
	return id, nil
}

// ExportStill writes the .drx of a gallery still: its node tree and thumbnail.
func (s *Session) ExportStill(ctx context.Context, id string, w io.Writer) (*ExportInfo, error) {
	id, ok := ValidID(id)
	if !ok {
		return nil, fmt.Errorf("%w: invalid still identifier", ErrNotFound)
	}
	g := newGraph()
	if err := s.load(ctx, g, "Gallery::GyStill", []string{id}); err != nil {
		return nil, err
	}
	st := g.get("Gallery::GyStill", id)
	if st == nil {
		return nil, fmt.Errorf("%w: still %s", ErrNotFound, id)
	}
	e := &emitter{m: s.m, g: g}
	doc, err := e.document(s.Header(0), "Gallery::GyStill", id)
	if err != nil {
		return nil, err
	}
	n, err := w.Write(doc)
	if err != nil {
		return nil, err
	}
	label, _ := st.vals["Label"].(string)
	return &ExportInfo{Name: label, Files: 1, Bytes: int64(n), Notes: e.notes}, nil
}

// ExportAlbum writes a zip with one .drx per still of a gallery album.
func (s *Session) ExportAlbum(ctx context.Context, viewID string, w io.Writer) (*ExportInfo, error) {
	viewID, ok := ValidID(viewID)
	if !ok {
		return nil, fmt.Errorf("%w: invalid album identifier", ErrNotFound)
	}
	albums, err := s.albums(ctx, viewID)
	if err != nil {
		return nil, err
	}
	if len(albums) == 0 || len(albums[0].Stills) == 0 {
		return nil, fmt.Errorf("%w: album %s", ErrNotFound, viewID)
	}
	album := albums[0]
	var ids []string
	for _, st := range album.Stills {
		ids = append(ids, st.ID)
	}
	g := newGraph()
	if err := s.load(ctx, g, "Gallery::GyStill", ids); err != nil {
		return nil, err
	}
	e := &emitter{m: s.m, g: g}
	header := s.Header(0)
	cw := &countingWriter{w: w}
	zw := zip.NewWriter(cw)
	now := time.Now()
	info := &ExportInfo{Name: album.Name}
	for _, st := range album.Stills {
		doc, err := e.document(header, "Gallery::GyStill", st.ID)
		if err != nil {
			return nil, err
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: StillFileName(st.Label, st.ID), Method: zip.Deflate, Modified: now})
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(doc); err != nil {
			return nil, err
		}
		info.Files++
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	info.Bytes = cw.n
	info.Notes = e.notes
	return info, nil
}

// Download runs one export into memory and closes the session before it
// returns, so the REPEATABLE READ snapshot and the locks it holds on the
// library's tables are released before the file goes to a client that may
// read it slowly (docs 22 §9). kind is "project", "still" or "album".
func (s *Session) Download(ctx context.Context, kind, id string) (*ExportInfo, []byte, error) {
	defer s.Close(ctx)
	var buf bytes.Buffer
	var info *ExportInfo
	var err error
	switch kind {
	case "project":
		info, err = s.ExportProject(ctx, id, &buf)
	case "still":
		info, err = s.ExportStill(ctx, id, &buf)
	case "album":
		info, err = s.ExportAlbum(ctx, id, &buf)
	default:
		err = fmt.Errorf("SQS-E006 unknown export kind %q", kind)
	}
	if err != nil {
		return nil, nil, err
	}
	return info, buf.Bytes(), nil
}

// FileName names a download of kind ("project", "still" or "album") with
// prefix (the library identifier, plus the backup date for an export from a
// backup) and the exported object's name.
func FileName(kind, prefix, id string, info *ExportInfo) string {
	switch kind {
	case "project":
		return prefix + "_" + SafeName(info.Name, "project") + ".drp"
	case "still":
		return StillFileName(info.Name, strings.ToLower(id))
	}
	return prefix + "_" + SafeName(info.Name, "album") + ".zip"
}

// StillFileName names a .drx after its label and the start of its id, since
// labels repeat inside an album.
func StillFileName(label, id string) string {
	return SafeName(label, "still") + "-" + id[:8] + ".drx"
}

// SafeName keeps a label usable as a file name on every system.
func SafeName(s, fallback string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteByte('_')
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ". ")
	if out == "" {
		return fallback
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}
