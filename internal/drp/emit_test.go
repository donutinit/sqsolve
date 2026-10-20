// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"errors"
	"strings"
	"testing"
)

func testGraph(t *testing.T) (*Model, *graph) {
	t.Helper()
	m, err := DefaultModel()
	if err != nil {
		t.Fatal(err)
	}
	return m, newGraph()
}

func (g *graph) add(r *row) *row {
	if g.rows[r.table] == nil {
		g.rows[r.table] = map[string]*row{}
	}
	g.rows[r.table][r.id] = r
	return r
}

func render(t *testing.T, e *emitter, header, table, id string) string {
	t.Helper()
	doc, err := e.document(header, table, id)
	if err != nil {
		t.Fatalf("document %s %s: %v", table, id, err)
	}
	return string(doc)
}

func expect(t *testing.T, doc string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(doc, p) {
			t.Errorf("missing %q in:\n%s", p, doc)
		}
	}
}

func reject(t *testing.T, doc string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(doc, p) {
			t.Errorf("unexpected %q in:\n%s", p, doc)
		}
	}
}

func TestEmitScalarsAndNestedObject(t *testing.T) {
	m, g := testGraph(t)
	g.add(&row{table: "SM_Config", id: "c1", class: "SM_Config", vals: map[string]any{
		"Name": "a & b", "ConfigId": int32(3), "DbSavedTime": int64(5), "FieldsBlob": []byte{0xab, 0xcd}, "MultiSysData": "m1",
	}})
	g.add(&row{table: "Sm2MultiSysData", id: "m1", class: "Sm2MultiSysData", vals: map[string]any{"DbSavedTime": int64(7)}})
	e := &emitter{m: m, g: g}
	doc := render(t, e, `<!--DbAppVer="x" DbPrjVer="1"-->`, "SM_Config", "c1")
	if !strings.HasPrefix(doc, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!--DbAppVer=\"x\" DbPrjVer=\"1\"-->\n<SM_Config DbId=\"c1\">\n") {
		t.Errorf("header:\n%s", doc)
	}
	if !strings.HasSuffix(doc, "</SM_Config>\n") {
		t.Errorf("document does not end with a newline:\n%s", doc)
	}
	expect(t, doc,
		" <FieldsBlob>abcd</FieldsBlob>\n",
		" <User/>\n", // null uuid listed as empty
		" <ConfigId>3</ConfigId>\n",
		" <Name>a &amp; b</Name>\n",
		" <LockSysId/>\n", // null text is an empty element
		" <MultiSysData>\n  <Sm2MultiSysData DbId=\"m1\">\n   <FieldsBlob/>\n   <LockSysId/>\n   <DbSavedTime>7</DbSavedTime>\n  </Sm2MultiSysData>\n </MultiSysData>\n")
	doc = render(t, e, "", "SM_Config", "c1")
	if strings.Contains(doc, "DbAppVer") || !strings.HasPrefix(doc, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<SM_Config DbId=\"c1\">\n") {
		t.Errorf("without header:\n%s", doc)
	}
	if len(e.notes) != 0 {
		t.Errorf("notes: %v", e.notes)
	}
}

func TestEmitListsAndVectors(t *testing.T) {
	m, g := testGraph(t)
	g.add(&row{table: "SM_Project", id: "p1", class: "SM_Project", vals: map[string]any{
		"ProjectName": "Test", "StreamIdVector": "{0,1}", "StreamCursorVector": "{1,1}", "GangPlayHeadVector": "{}",
		"UpToDate": true, "CurrentGroup": nil, "Folder": nil, "LockableBlobMap": "lb1",
	}})
	g.add(&row{table: "BtThumnail", id: "t1", class: "BtThumnail", vals: map[string]any{"ImgWidth": int32(240), "Buffer": []byte{0xff, 0xd8}}})
	g.add(&row{table: "BtThumnail", id: "t2", class: "BtThumnail", vals: map[string]any{"ImgWidth": int32(480)}})
	g.assoc[assocKey("BtThumnail_SM_Project", "Thumbnails", "p1")] = []string{"t1", "t2"}
	g.assoc[assocKey("SM_Project_Sm2Timeline", "TimelineHandleVec", "p1")] = []string{"tl1", "tl2"}
	g.add(&row{table: "BtLockableBlobMap", id: "lb1", class: "Sm2LockableBlobMap", vals: map[string]any{}})
	g.add(&row{table: "BtLockableBlob", id: "b1", class: "Sm2MediaPoolLockableBlob", vals: map[string]any{"BlobOwner": "mp1", "DbSavedTime": int64(1)}})
	g.backref[assocKey("BtLockableBlob", "BtLockableBlobMap_id", "lb1")] = []string{"b1"}

	e := &emitter{m: m, g: g, projectName: "Test"}
	doc := render(t, e, "", "SM_Project", "p1")
	expect(t, doc,
		" <Thumbnails>\n  <Element>\n   <BtThumnail DbId=\"t1\">\n    <FieldsBlob/>\n    <ImgWidth>240</ImgWidth>\n    <ImgHeight/>\n    <Buffer>ffd8</Buffer>\n   </BtThumnail>\n  </Element>\n  <Element>\n   <BtThumnail DbId=\"t2\">",
		" <TimelineHandleVec>\n  <Element>tl1</Element>\n  <Element>tl2</Element>\n </TimelineHandleVec>\n",
		" <StreamIdVector>\n  <Element>0</Element>\n  <Element>1</Element>\n </StreamIdVector>\n",
		" <StreamCursorVector>\n  <Element>0</Element>\n  <Element>0</Element>\n </StreamCursorVector>\n",
		" <GangPlayHeadVector/>\n",
		" <StreamPlayHeadVector/>\n", // absent column value: empty vector
		" <Folder/>\n",
		" <UpToDate>true</UpToDate>\n", // no content: the stored value stays
		" <FolderMap/>\n",              // a list the exporter never saw filled
		" <LockableBlobMap>\n  <Sm2LockableBlobMap DbId=\"lb1\">\n   <FieldsBlob/>\n   <LocableBlobSet>\n    <Element>\n     <Sm2MediaPoolLockableBlob DbId=\"b1\">\n      <FieldsBlob/>\n      <BlobOwner>mp1</BlobOwner>\n      <DbSavedTime>1</DbSavedTime>\n     </Sm2MediaPoolLockableBlob>\n    </Element>\n   </LocableBlobSet>\n  </Sm2LockableBlobMap>\n </LockableBlobMap>\n")
	reject(t, doc, "<CurrentGroup", "<User>", "<User/>", "<ProjRef>", "<CommonConfig")
	expect(t, doc, " <ProjRef/>\n")

	g.assoc[assocKey("BtThumnail_SM_Project", "Thumbnails", "p1")] = nil
	e.hasContent = true
	doc = render(t, e, "", "SM_Project", "p1")
	expect(t, doc, " <Thumbnails/>\n", " <UpToDate>false</UpToDate>\n")
}

func TestEmitOverrides(t *testing.T) {
	m, g := testGraph(t)
	g.add(&row{table: "Sm2TiItem", id: "a1", class: "Sm2TiAudioClip", vals: map[string]any{
		"RenderTextEnabled": true, "RenderTextGanged": true, "RenderTextPrefixed": true, "MediaStartTime": 13336.656666666666,
	}})
	g.add(&row{table: "Sm2TiItem", id: "v1", class: "Sm2TiVideoClip", vals: map[string]any{"Thumbnail": "t9", "RenderTextEnabled": true}})
	g.add(&row{table: "BtThumnail", id: "t9", class: "BtThumnail", vals: map[string]any{}})
	g.add(&row{table: "SM_Config", id: "c1", class: "SM_Config", vals: map[string]any{"Name": "viejo"}})
	g.add(&row{table: "Sm2MpMedia", id: "m1", class: "Sm2MpVideoClip", vals: map[string]any{"SlateTC": []byte("-68719476737"), "FieldsBlob": []byte("-1")}})

	e := &emitter{m: m, g: g, projectName: "Test"}
	expect(t, render(t, e, "", "Sm2TiItem", "a1"),
		" <RenderTextEnabled>false</RenderTextEnabled>\n <RenderTextGanged>false</RenderTextGanged>\n <RenderTextPrefixed>false</RenderTextPrefixed>\n",
		" <MediaStartTime>13336.656666666666</MediaStartTime>\n")
	video := render(t, e, "", "Sm2TiItem", "v1")
	expect(t, video, " <RenderTextEnabled>true</RenderTextEnabled>\n", " <Thumbnail/>\n")
	reject(t, video, "<BtThumnail")
	expect(t, render(t, e, "", "SM_Config", "c1"), " <Name>Test.Cfg</Name>\n")
	e.projectName = ""
	expect(t, render(t, e, "", "SM_Config", "c1"), " <Name>viejo</Name>\n")
	media := render(t, e, "", "Sm2MpMedia", "m1")
	expect(t, media, " <SlateTC>-68719476737</SlateTC>\n", " <FieldsBlob>2d31</FieldsBlob>\n", " <MatteVec/>\n")
	reject(t, media, "<Video>", "<PTZRPreset>")
}

func TestEmitBodyRewriteOnlyInMediaPool(t *testing.T) {
	m, g := testGraph(t)
	old, version := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	body := encodePayload(pbBytes(1, []byte("OfxImageEffectContextFilter_"+old+"_")), false)
	g.add(&row{table: "ListMgt::LmVersion", id: version, class: "ListMgt::LmVersion", vals: map[string]any{"Body": body, "HasCorrection": true}})
	e := &emitter{m: m, g: g}
	expect(t, render(t, e, "", "ListMgt::LmVersion", version), " <Body>"+hexOf(body)+"</Body>\n", " <HasCorrection>true</HasCorrection>\n")
	e.inMediaPool = true
	doc := render(t, e, "", "ListMgt::LmVersion", version)
	want, _ := rewriteOFXContext(body, version)
	expect(t, doc, " <Body>"+hexOf(want)+"</Body>\n")
	reject(t, doc, hexOf([]byte(old)))
	g.rows["ListMgt::LmVersion"][version].vals["Body"] = []byte{0x81, 0x00, 0x01}
	if _, err := e.document("", "ListMgt::LmVersion", version); err == nil || !strings.Contains(err.Error(), "SQS-E006") {
		t.Errorf("bad body: %v", err)
	}
}

func hexOf(b []byte) string {
	s, _ := formatScalar(b, false)
	return s
}

func TestEmitMissingAndUnknown(t *testing.T) {
	m, g := testGraph(t)
	g.add(&row{table: "SM_Config", id: "c1", class: "SM_Config", vals: map[string]any{"MultiSysData": "nope"}})
	g.add(&row{table: "SM_Project", id: "p1", class: "SM_Project", vals: map[string]any{}})
	g.assoc[assocKey("BtThumnail_SM_Project", "Thumbnails", "p1")] = []string{"gone", "t1"}
	g.add(&row{table: "BtThumnail", id: "t1", class: "BtThumnail", vals: map[string]any{}})
	e := &emitter{m: m, g: g}
	doc := render(t, e, "", "SM_Config", "c1")
	reject(t, doc, "<MultiSysData")
	doc = render(t, e, "", "SM_Project", "p1")
	expect(t, doc, " <Thumbnails>\n  <Element>\n   <BtThumnail DbId=\"t1\">")
	reject(t, doc, "gone")
	if len(e.notes) != 2 || !strings.Contains(e.notes[0], "nope") || !strings.Contains(e.notes[1], "gone") {
		t.Errorf("notes: %v", e.notes)
	}

	g.add(&row{table: "Sm2MpMedia", id: "w1", class: "Sm2MpWeird", vals: map[string]any{}})
	if _, err := e.document("", "Sm2MpMedia", "w1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("unknown class: %v", err)
	}
	if _, err := e.document("", "Sm2MpMedia", "absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("absent root: %v", err)
	}
	g.add(&row{table: "Sm2MpMedia", id: "n1", vals: map[string]any{"Name": "no DbType"}})
	if _, err := e.document("", "Sm2MpMedia", "n1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("row without class: %v", err)
	}
}

func TestEmitDepthLimit(t *testing.T) {
	m, err := ParseModel([]byte(`{"classes": {"A": {"table": "A", "fields": ["Next"], "kinds": {"Next": "obj"}, "children": {"Next": ["A"]}}},
		"tables": {"A": {"A_id": "uuid", "Next": "uuid", "DbType": "character varying(128)"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	g := newGraph()
	g.add(&row{table: "A", id: "a1", class: "A", vals: map[string]any{"Next": "a1"}})
	e := &emitter{m: m, g: g}
	_, err = e.document("", "A", "a1")
	if err == nil || !strings.Contains(err.Error(), "SQS-E006") || !strings.Contains(err.Error(), "levels") {
		t.Errorf("cycle: %v", err)
	}
	if e.depth != 0 {
		t.Errorf("depth not restored: %d", e.depth)
	}
}
