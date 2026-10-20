// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/testpg"
)

// The integration test builds a synthetic library: the tables and columns the
// .drp format names (docs 22), created from the model with invented values.
// Nothing in it comes from a Resolve library.

const (
	idProject   = "00000000-0000-4000-8000-000000000001"
	idPool      = "00000000-0000-4000-8000-000000000002"
	idConfig    = "00000000-0000-4000-8000-000000000003"
	idRoot      = "00000000-0000-4000-8000-000000000010"
	idFolderA   = "00000000-0000-4000-8000-000000000011"
	idFolderB   = "00000000-0000-4000-8000-000000000012"
	idClip      = "00000000-0000-4000-8000-000000000020"
	idGallery   = "00000000-0000-4000-8000-000000000030"
	idView      = "00000000-0000-4000-8000-000000000031"
	idRef       = "00000000-0000-4000-8000-000000000032"
	idStill     = "00000000-0000-4000-8000-000000000033"
	idVerFull   = "00000000-0000-4000-8000-000000000034"
	idVerTrack  = "00000000-0000-4000-8000-000000000035"
	idThumb     = "00000000-0000-4000-8000-000000000036"
	idGallery2  = "00000000-0000-4000-8000-000000000040"
	idView2     = "00000000-0000-4000-8000-000000000041"
	idRef2      = "00000000-0000-4000-8000-000000000042"
	idStill2    = "00000000-0000-4000-8000-000000000043"
	idVerFull2  = "00000000-0000-4000-8000-000000000044"
	idVerTrack2 = "00000000-0000-4000-8000-000000000045"
	idThumb2    = "00000000-0000-4000-8000-000000000046"
	idPrjThumb  = "00000000-0000-4000-8000-000000000050"
	idWeird     = "00000000-0000-4000-8000-000000000060"
)

var fakeJPEG = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xff, 0xd9}

type synthDB struct {
	t    *testing.T
	ctx  context.Context
	c    *testpg.Cluster
	name string
	conn *pgx.Conn
}

// newSynthDB creates a database with every table of the model.
func newSynthDB(t *testing.T, c *testpg.Cluster, name string) *synthDB {
	t.Helper()
	ctx := context.Background()
	admin, err := c.Connect(ctx, "postgres", c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	admin.Close(ctx)
	conn, err := c.Connect(ctx, "postgres", c.SuperPass, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })
	m, err := DefaultModel()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range m.TableNames() {
		cols := make([]string, 0, len(m.Tables[table]))
		for col := range m.Tables[table] {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		defs := make([]string, len(cols))
		for i, col := range cols {
			defs[i] = ident(col) + " " + m.Tables[table][col]
		}
		if _, err := conn.Exec(ctx, "CREATE TABLE "+ident(table)+" ("+strings.Join(defs, ", ")+")"); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
	}
	return &synthDB{t: t, ctx: ctx, c: c, name: name, conn: conn}
}

func (d *synthDB) exec(sql string, args ...any) {
	d.t.Helper()
	if _, err := d.conn.Exec(d.ctx, sql, args...); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
}

// insert adds one row from a column map.
func (d *synthDB) insert(table string, vals map[string]any) {
	d.t.Helper()
	cols := make([]string, 0, len(vals))
	for c := range vals {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	names := make([]string, len(cols))
	marks := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		names[i] = ident(c)
		marks[i] = fmt.Sprintf("$%d", i+1)
		args[i] = vals[c]
	}
	d.exec("INSERT INTO "+ident(table)+" ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...)
}

func (d *synthDB) assoc(table, owner, prop string, index int, associate string) {
	d.t.Helper()
	d.insert(table, map[string]any{"DbOwner": owner, "DbAssociate": associate, "DbPropertyName": prop, "DbIndex": int32(index)})
}

// open starts a session on a fresh connection, since a session owns its connection.
func (d *synthDB) open() (*Session, error) {
	conn, err := d.c.Connect(d.ctx, "postgres", d.c.SuperPass, d.name)
	if err != nil {
		d.t.Fatal(err)
	}
	s, err := Open(d.ctx, conn)
	if err != nil {
		conn.Close(d.ctx)
	}
	return s, err
}

func (d *synthDB) mustOpen() *Session {
	d.t.Helper()
	s, err := d.open()
	if err != nil {
		d.t.Fatal(err)
	}
	d.t.Cleanup(func() { s.Close(d.ctx) })
	return s
}

func fakeBody(label string) []byte {
	return encodePayload(pbBytes(1, []byte("nodes of "+label)), false)
}

func (d *synthDB) still(id, label, verFull, verTrack, thumb string, created time.Time) {
	d.insert("ListMgt::LmVersion", map[string]any{"ListMgt::LmVersion_id": verFull, "DbType": "ListMgt::LmVersion", "HasCorrection": true, "VerType": "0", "ImplVersion": int32(1), "Body": fakeBody(label + " clip")})
	d.insert("ListMgt::LmVersion", map[string]any{"ListMgt::LmVersion_id": verTrack, "DbType": "ListMgt::LmVersion", "HasCorrection": false, "VerType": "1", "ImplVersion": int32(1), "Body": fakeBody(label + " track")})
	d.insert("BtThumnail", map[string]any{"BtThumnail_id": thumb, "DbType": "BtThumnail", "ImgWidth": int32(288), "ImgHeight": int32(162), "Buffer": fakeJPEG, "Gallery::GyStill_id": id})
	d.insert("Gallery::GyStill", map[string]any{"Gallery::GyStill_id": id, "DbType": "Gallery::GyStill", "Label": label, "RecTC": "01:00:00:00", "SrcTC": "02:00:00:00",
		"Width": int32(1920), "Height": int32(1080), "BitDepth": "10", "PAR": 1.0, "CreateTime": created, "pClipFullVer": verFull, "pTrackVer": verTrack, "Vsr": thumb, "SrcType": int32(1)})
}

func (d *synthDB) seedProject() {
	d.insert("Sm2MediaPool", map[string]any{"Sm2MediaPool_id": idPool, "DbType": "Sm2MediaPool", "StereoSource": int32(2)})
	d.insert("SM_Config", map[string]any{"SM_Config_id": idConfig, "DbType": "SM_Config", "Name": "x", "ConfigId": int32(0)})
	d.insert("SM_Project", map[string]any{"SM_Project_id": idProject, "DbType": "SM_Project", "ProjectName": "Test", "ProjectVersion": int32(17),
		"MediaPool": idPool, "CommonConfig": idConfig, "UpToDate": true, "StreamCursorVector": []int32{1}, "StreamIdVector": []int32{0}, "LastModTimeInSecs": int64(1792418582)})
	d.insert("Sm2MpFolder", map[string]any{"Sm2MpFolder_id": idRoot, "DbType": "Sm2MpFolder", "Name": "Master", "Sm2MediaPool_id": idPool, "MediaPool": idPool,
		"FieldsBlob": folderOrderBlob(true, idFolderB, idFolderA)})
	d.insert("Sm2MpFolder", map[string]any{"Sm2MpFolder_id": idFolderA, "DbType": "Sm2MpFolder", "Name": "aaa", "MpFolder": idRoot, "Sm2MediaPool_id": idPool})
	d.insert("Sm2MpFolder", map[string]any{"Sm2MpFolder_id": idFolderB, "DbType": "Sm2MpFolder", "Name": "bbb", "MpFolder": idRoot, "Sm2MediaPool_id": idPool})
	d.insert("Sm2MpMedia", map[string]any{"Sm2MpMedia_id": idClip, "DbType": "Sm2MpVideoClip", "Name": "clip & co", "MpFolder": idFolderA, "SlateTC": []byte("-68719476737")})
	d.assoc("Sm2MpFolder_Sm2MpMedia", idFolderA, "MediaVec", 0, idClip)
	// The project's gallery album.
	d.insert("Gallery::GyGallery", map[string]any{"Gallery::GyGallery_id": idGallery, "DbType": "Gallery::GyGallery", "SM_Project_id": idProject})
	d.insert("Gallery::GyView", map[string]any{"Gallery::GyView_id": idView, "DbType": "Gallery::GyView", "ViewName": "current", "Gallery::GyGallery_id": idGallery})
	d.assoc("Gallery::GyGallery_Gallery::GyView", idGallery, "Views", 0, idView)
	d.still(idStill, "1.1", idVerFull, idVerTrack, idThumb, time.Date(2026, 10, 19, 13, 37, 24, 830_000_000, time.UTC))
	d.insert("Gallery::GyStillRef", map[string]any{"Gallery::GyStillRef_id": idRef, "DbType": "Gallery::GyStillRef", "Ref0": idStill, "Gallery::GyView_id": idView, "Label": "1.1"})
	d.assoc("Gallery::GyStillRef_Gallery::GyView", idView, "Stills", 0, idRef)
	// A PowerGrade album: a gallery without project.
	d.insert("Gallery::GyGallery", map[string]any{"Gallery::GyGallery_id": idGallery2, "DbType": "Gallery::GyGallery"})
	d.insert("Gallery::GyView", map[string]any{"Gallery::GyView_id": idView2, "DbType": "Gallery::GyView", "ViewName": "PowerGrade", "Gallery::GyGallery_id": idGallery2})
	d.assoc("Gallery::GyGallery_Gallery::GyView", idGallery2, "Views", 0, idView2)
	d.still(idStill2, "2.1", idVerFull2, idVerTrack2, idThumb2, time.Date(2026, 10, 1, 3, 22, 55, 0, time.UTC))
	d.insert("Gallery::GyStillRef", map[string]any{"Gallery::GyStillRef_id": idRef2, "DbType": "Gallery::GyStillRef", "Ref0": idStill2, "Gallery::GyView_id": idView2, "Label": "2.1"})
	d.assoc("Gallery::GyStillRef_Gallery::GyView", idView2, "Stills", 0, idRef2)
	// Project thumbnail.
	d.insert("BtThumnail", map[string]any{"BtThumnail_id": idPrjThumb, "DbType": "BtThumnail", "ImgWidth": int32(240), "ImgHeight": int32(135), "Buffer": fakeJPEG, "SM_Project_id": idProject})
	d.assoc("BtThumnail_SM_Project", idProject, "Thumbnails", 0, idPrjThumb)
}

func zipEntries(t *testing.T, data []byte) (map[string]string, []string, []*zip.File) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{}
	var names []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		docs[f.Name] = b.String()
		names = append(names, f.Name)
	}
	return docs, names, zr.File
}

func TestExportSyntheticLibrary(t *testing.T) {
	c := testpg.Start(t)
	d := newSynthDB(t, c, "drp_test")
	d.seedProject()
	ctx := d.ctx

	s := d.mustOpen()
	if s.KnownOK || !strings.HasPrefix(s.Fingerprint, "sha256:") {
		t.Errorf("synthetic schema must be unknown: %v %s", s.KnownOK, s.Fingerprint)
	}

	projects, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != idProject || projects[0].Name != "Test" || projects[0].Path != "" ||
		!bytes.Equal(projects[0].Thumb, fakeJPEG) || projects[0].Modified.Unix() != 1792418582 {
		t.Fatalf("projects: %+v", projects)
	}

	albums, err := s.Albums(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 2 {
		t.Fatalf("albums: %+v", albums)
	}
	if a := albums[0]; a.ProjectID != "" || a.Name != "PowerGrade" || a.ViewID != idView2 || len(a.Stills) != 1 || a.Stills[0].ID != idStill2 || a.Stills[0].Label != "2.1" {
		t.Errorf("power grade album: %+v", a)
	}
	if a := albums[1]; a.ProjectID != idProject || a.ProjectName != "Test" || a.Name != "current" || len(a.Stills) != 1 ||
		a.Stills[0].Created != "2026-10-19T13:37:24.830" || a.Stills[0].Width != 1920 || !bytes.Equal(a.Stills[0].Thumb, fakeJPEG) {
		t.Errorf("project album: %+v", a)
	}

	var buf bytes.Buffer
	info, err := s.ExportProject(ctx, idProject, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Test" || info.Files != 5 || info.Bytes != int64(buf.Len()) || len(info.Notes) != 0 {
		t.Errorf("info: %+v", info)
	}
	docs, names, files := zipEntries(t, buf.Bytes())
	want := []string{"project.xml", "MediaPool/Master/000_bbb/MpFolder.xml", "MediaPool/Master/001_aaa/MpFolder.xml", "MediaPool/Master/MpFolder.xml", "Gallery.xml"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("zip entries:\n%v\nwant\n%v", names, want)
	}
	for _, f := range files {
		if f.Flags&0x800 != 0 {
			t.Errorf("%s carries the UTF-8 flag", f.Name)
		}
	}
	prj := docs["project.xml"]
	if !strings.HasPrefix(prj, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<SM_Project DbId=\""+idProject+"\">\n") {
		t.Errorf("project.xml head:\n%.200s", prj)
	}
	for _, p := range []string{"<UpToDate>false</UpToDate>", "<Name>Test.Cfg</Name>", "<ProjectVersion>17</ProjectVersion>",
		"<StreamCursorVector>\n  <Element>0</Element>\n </StreamCursorVector>", "<Thumbnails>\n  <Element>\n   <BtThumnail DbId=\"" + idPrjThumb + "\">",
		"<CommonConfig>\n  <SM_Config DbId=\"" + idConfig + "\">"} {
		if !strings.Contains(prj, p) {
			t.Errorf("project.xml lacks %q", p)
		}
	}
	if strings.Contains(prj, "DbAppVer") || strings.Contains(prj, "<pGallery") {
		t.Error("project.xml has a header for an unknown schema or a scalar gallery pointer")
	}
	folderA := docs["MediaPool/Master/001_aaa/MpFolder.xml"]
	for _, p := range []string{"<Sm2MpFolder DbId=\"" + idFolderA + "\">", "<Name>aaa</Name>", "<MediaVec>\n  <Element>\n   <Sm2MpVideoClip DbId=\"" + idClip + "\">",
		"<Name>clip &amp; co</Name>", "<SlateTC>-68719476737</SlateTC>", "<MatteVec/>"} {
		if !strings.Contains(folderA, p) {
			t.Errorf("folder aaa lacks %q", p)
		}
	}
	if !strings.Contains(docs["Gallery.xml"], "<Gallery::GyGallery DbId=\""+idGallery+"\">") || !strings.Contains(docs["Gallery.xml"], "<ViewName>current</ViewName>") {
		t.Errorf("Gallery.xml:\n%s", docs["Gallery.xml"])
	}

	buf.Reset()
	info, err = s.ExportStill(ctx, strings.ToUpper(idStill), &buf)
	if err != nil {
		t.Fatal(err)
	}
	still := buf.String()
	if info.Name != "1.1" || info.Files != 1 || !strings.HasPrefix(still, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Gallery::GyStill DbId=\""+idStill+"\">\n") ||
		strings.Count(still, "<ListMgt::LmVersion DbId=\"") != 2 || !strings.Contains(still, "<Buffer>"+hexOf(fakeJPEG)+"</Buffer>") ||
		!strings.Contains(still, "<CreateTime>2026-10-19T13:37:24.830</CreateTime>") || !strings.Contains(still, "<PAR>1</PAR>") {
		t.Errorf("still %+v:\n%s", info, still)
	}

	buf.Reset()
	info, err = s.ExportAlbum(ctx, idView2, &buf)
	if err != nil {
		t.Fatal(err)
	}
	_, names, _ = zipEntries(t, buf.Bytes())
	if info.Name != "PowerGrade" || info.Files != 1 || len(names) != 1 || names[0] != "2.1-"+idStill2[:8]+".drx" {
		t.Errorf("album %+v: %v", info, names)
	}

	if _, err := s.ExportProject(ctx, "00000000-0000-4000-8000-0000000000ff", &buf); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project: %v", err)
	}
	if _, err := s.ExportStill(ctx, "not-a-uuid", &buf); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad still id: %v", err)
	}
	if _, err := s.ExportAlbum(ctx, idView, &buf); err != nil {
		t.Errorf("project album: %v", err)
	}
	if _, err := s.ExportAlbum(ctx, "00000000-0000-4000-8000-0000000000ff", &buf); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing album: %v", err)
	}
	s.Close(ctx)

	// A feature the exporter never saw makes it refuse instead of dropping data.
	d.assoc("BtVideoInfo_Sm2MpMedia", idClip, "MatteVec", 0, idClip)
	s = d.mustOpen()
	if _, err := s.ExportProject(ctx, idProject, &buf); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "MatteVec") {
		t.Errorf("guard: %v", err)
	}
	s.Close(ctx)
	d.exec(`DELETE FROM "BtVideoInfo_Sm2MpMedia"`)

	// So does an object class the model does not know.
	d.insert("Sm2MpMedia", map[string]any{"Sm2MpMedia_id": idWeird, "DbType": "Sm2MpWeird", "MpFolder": idFolderA})
	d.assoc("Sm2MpFolder_Sm2MpMedia", idFolderA, "MediaVec", 1, idWeird)
	s = d.mustOpen()
	if _, err := s.ExportProject(ctx, idProject, &buf); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "Sm2MpWeird") {
		t.Errorf("unknown class: %v", err)
	}
	s.Close(ctx)

	// A schema that drifted from the model is refused at Open.
	d.exec(`ALTER TABLE "SM_Project" DROP COLUMN "ProjectName"`)
	if _, err := d.open(); !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), "SM_Project.ProjectName") {
		t.Errorf("contract: %v", err)
	}
}
