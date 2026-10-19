// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

// Builders of synthetic blobs in the shapes Resolve uses. No value here comes
// from a real library.

func uvarint(v uint64) []byte {
	buf := make([]byte, binary.MaxVarintLen64)
	return buf[:binary.PutUvarint(buf, v)]
}

func pbVarint(field, v uint64) []byte { return append(uvarint(field<<3), uvarint(v)...) }

func pbFixed64(field uint64, v uint64) []byte {
	out := append(uvarint(field<<3|1), make([]byte, 8)...)
	binary.LittleEndian.PutUint64(out[len(out)-8:], v)
	return out
}

func pbFixed32(field uint64, v uint32) []byte {
	out := append(uvarint(field<<3|5), make([]byte, 4)...)
	binary.LittleEndian.PutUint32(out[len(out)-4:], v)
	return out
}

func pbBytes(field uint64, b []byte) []byte {
	out := append(uvarint(field<<3|2), uvarint(uint64(len(b)))...)
	return append(out, b...)
}

func qdsStr(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 4+2*len(u))
	binary.BigEndian.PutUint32(out, uint32(2*len(u)))
	for i, c := range u {
		binary.BigEndian.PutUint16(out[4+2*i:], c)
	}
	return out
}

type qdsEntry struct {
	key     string
	typ     uint32
	payload []byte
}

// qdsV1Map builds a version 1 FieldsBlob: a QDataStream map of key to QVariant.
func qdsV1Map(entries ...qdsEntry) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out, 1)
	binary.BigEndian.PutUint32(out[4:], uint32(len(entries)))
	for _, e := range entries {
		out = append(out, qdsStr(e.key)...)
		out = binary.BigEndian.AppendUint32(out, e.typ)
		out = append(out, 0) // not null
		out = append(out, e.payload...)
	}
	return out
}

// fieldsBlobV2 wraps a payload as a version 2 FieldsBlob.
func fieldsBlobV2(payload []byte) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out, 2)
	binary.BigEndian.PutUint32(out[4:], uint32(len(payload)))
	return append(out, payload...)
}

// folderOrderBlob builds the FieldsBlob of a media pool folder whose children
// are the given ids, in that order.
func folderOrderBlob(compressed bool, ids ...string) []byte {
	entries := make([]qdsEntry, len(ids))
	for i, id := range ids {
		entries[i] = qdsEntry{key: itoa(i), typ: 10, payload: qdsStr(id)}
	}
	proto := append(pbVarint(1, 7), pbBytes(2, qdsV1Map(entries...))...)
	proto = append(proto, pbVarint(3, 9)...)
	return fieldsBlobV2(encodePayload(proto, compressed))
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestQDSMap(t *testing.T) {
	blob := qdsV1Map(
		qdsEntry{"Flag", 1, []byte{1}},
		qdsEntry{"Int", 2, []byte{0, 0, 0, 5}},
		qdsEntry{"Long", 4, make([]byte, 8)},
		qdsEntry{"Dbl", 6, make([]byte, 8)},
		qdsEntry{"Bytes", 12, append([]byte{0, 0, 0, 3}, 'a', 'b', 'c')},
		qdsEntry{"NullBytes", 12, []byte{0xff, 0xff, 0xff, 0xff}},
		qdsEntry{"Nombre", 10, qdsStr("año ñ")},
		qdsEntry{"Vacío", 10, []byte{0xff, 0xff, 0xff, 0xff}},
	)
	m, err := qdsMap(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["Nombre"] != "año ñ" || m["Vacío"] != "" {
		t.Errorf("map = %v", m)
	}
	if _, err := qdsMap([]byte{0, 0, 0, 2, 0, 0, 0, 0}); err == nil {
		t.Error("version 2 accepted as a map")
	}
	if _, err := qdsMap(qdsV1Map(qdsEntry{"X", 99, nil})); err == nil {
		t.Error("unknown QVariant type accepted")
	}
	if _, err := qdsMap(nil); err == nil {
		t.Error("empty blob accepted")
	}
}

func TestProtoField(t *testing.T) {
	msg := append(pbVarint(1, 300), pbFixed64(4, 1)...)
	msg = append(msg, pbFixed32(5, 2)...)
	msg = append(msg, pbBytes(2, []byte("dos"))...)
	msg = append(msg, pbBytes(2, []byte("otra vez"))...)
	msg = append(msg, pbBytes(7, []byte("siete"))...)
	if got, ok := protoField(msg, 2); !ok || string(got) != "dos" {
		t.Errorf("field 2 = %q %v", got, ok)
	}
	if got, ok := protoField(msg, 7); !ok || string(got) != "siete" {
		t.Errorf("field 7 = %q %v", got, ok)
	}
	if _, ok := protoField(msg, 3); ok {
		t.Error("absent field found")
	}
	if _, ok := protoField([]byte{0x12, 0xff}, 2); ok {
		t.Error("truncated message accepted")
	}
	if _, ok := protoField([]byte{0x0f}, 1); ok {
		t.Error("wire type 7 accepted")
	}
}

func TestChildOrder(t *testing.T) {
	a, b := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	for _, compressed := range []bool{false, true} {
		got, err := childOrder(folderOrderBlob(compressed, b, a))
		if err != nil || len(got) != 2 || got[0] != b || got[1] != a {
			t.Errorf("compressed=%v: %v %v", compressed, got, err)
		}
	}
	for name, blob := range map[string][]byte{
		"nil":            nil,
		"v1":             qdsV1Map(qdsEntry{"0", 10, qdsStr(a)}),
		"v2 sin campo 2": fieldsBlobV2(encodePayload(pbVarint(1, 1), false)),
	} {
		if got, err := childOrder(blob); err != nil || got != nil {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	bad := fieldsBlobV2(encodePayload(pbBytes(2, qdsV1Map(qdsEntry{"x", 10, qdsStr(a)})), false))
	if _, err := childOrder(bad); err == nil {
		t.Error("non-numeric index accepted")
	}
	truncated := fieldsBlobV2(encodePayload(pbVarint(1, 1), false))
	binary.BigEndian.PutUint32(truncated[4:], 999)
	if _, err := childOrder(truncated); err == nil {
		t.Error("truncated blob accepted")
	}
}

func TestRewriteOFXContext(t *testing.T) {
	old, version := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	plain := pbBytes(1, []byte("sin efectos"))
	if got, err := rewriteOFXContext(append([]byte{0x80}, plain...), version); err != nil || string(got[1:]) != string(plain) {
		t.Errorf("no context: %v %v", got, err)
	}
	same := append([]byte{0x80}, plain...)
	if got, _ := rewriteOFXContext(same, version); &got[0] != &same[0] {
		t.Error("body without context was copied")
	}
	withCtx := pbBytes(1, []byte("OfxImageEffectContextFilter_"+old+"_ y OfxImageEffectContextFilter_"+old+"_"))
	for _, compressed := range []bool{false, true} {
		got, err := rewriteOFXContext(encodePayload(withCtx, compressed), version)
		if err != nil {
			t.Fatal(err)
		}
		raw, wasCompressed, err := payload(got)
		if err != nil || wasCompressed != compressed {
			t.Fatalf("payload: %v compressed=%v", err, wasCompressed)
		}
		if strings.Contains(string(raw), old) || strings.Count(string(raw), version) != 2 {
			t.Errorf("compressed=%v: %q", compressed, raw)
		}
	}
	for name, body := range map[string][]byte{"vacío": nil, "marca rara": {0x00, 0x01}} {
		if got, err := rewriteOFXContext(body, version); err != nil || !bytes.Equal(got, body) {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	if _, err := rewriteOFXContext([]byte{0x81, 0x00, 0x01}, version); err == nil {
		t.Error("bad zstd accepted")
	}
}
