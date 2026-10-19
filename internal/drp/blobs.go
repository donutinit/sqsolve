// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf16"

	"github.com/klauspost/compress/zstd"
)

// Resolve stores per-object extras in FieldsBlob. Version 1 is a Qt
// QDataStream map of UTF-16 keys to QVariant values; version 2 wraps a
// protobuf message, raw (0x80) or zstd-compressed (0x81). Only the pieces the
// exporter needs are decoded here: the child order of media pool folders
// (version 2, protobuf field 2, itself a version 1 map) and the node trees of
// grades for the OFX context rewrite.

var (
	zstdDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	zstdEncoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1), zstd.WithZeroFrames(true))
)

// payload returns the protobuf bytes of a version 2 payload and whether it was compressed.
func payload(b []byte) (raw []byte, compressed bool, err error) {
	if len(b) == 0 {
		return nil, false, errors.New("carga vacía")
	}
	switch b[0] {
	case 0x80:
		return b[1:], false, nil
	case 0x81:
		raw, err = zstdDecoder.DecodeAll(b[1:], nil)
		return raw, true, err
	}
	return nil, false, fmt.Errorf("carga con marca 0x%02x", b[0])
}

func encodePayload(raw []byte, compressed bool) []byte {
	if !compressed {
		return append([]byte{0x80}, raw...)
	}
	return append([]byte{0x81}, zstdEncoder.EncodeAll(raw, nil)...)
}

// protoField returns the first length-delimited value of field number n in a
// protobuf message, walking only the top level.
func protoField(b []byte, n uint64) ([]byte, bool) {
	i := 0
	for i < len(b) {
		key, k := binary.Uvarint(b[i:])
		if k <= 0 {
			return nil, false
		}
		i += k
		field, wire := key>>3, key&7
		switch wire {
		case 0:
			_, k := binary.Uvarint(b[i:])
			if k <= 0 {
				return nil, false
			}
			i += k
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			ln, k := binary.Uvarint(b[i:])
			if k <= 0 || uint64(len(b)-i-k) < ln {
				return nil, false
			}
			i += k
			if field == n {
				return b[i : i+int(ln)], true
			}
			i += int(ln)
		default:
			return nil, false
		}
	}
	return nil, false
}

// qdsMap decodes a QDataStream QMap<QString, QVariant> preceded by its
// version marker (1), keeping only string values.
func qdsMap(b []byte) (map[string]string, error) {
	r := bytes.NewReader(b)
	var version, n uint32
	if err := binary.Read(r, binary.BigEndian, &version); err != nil || version != 1 {
		return nil, errors.New("mapa QDataStream con versión desconocida")
	}
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i := uint32(0); i < n; i++ {
		key, err := qdsString(r)
		if err != nil {
			return nil, err
		}
		var typ uint32
		if err := binary.Read(r, binary.BigEndian, &typ); err != nil {
			return nil, err
		}
		if _, err := r.ReadByte(); err != nil { // is-null flag
			return nil, err
		}
		switch typ {
		case 1: // bool
			if _, err := r.ReadByte(); err != nil {
				return nil, err
			}
		case 2, 3: // int, uint
			r.Seek(4, 1)
		case 4, 5: // qlonglong, qulonglong
			r.Seek(8, 1)
		case 6: // double
			r.Seek(8, 1)
		case 10: // QString
			s, err := qdsString(r)
			if err != nil {
				return nil, err
			}
			out[key] = s
		case 12: // QByteArray
			var ln uint32
			if err := binary.Read(r, binary.BigEndian, &ln); err != nil {
				return nil, err
			}
			if ln != 0xffffffff {
				r.Seek(int64(ln), 1)
			}
		default:
			return nil, fmt.Errorf("QVariant de tipo %d no previsto", typ)
		}
	}
	return out, nil
}

func qdsString(r *bytes.Reader) (string, error) {
	var ln uint32
	if err := binary.Read(r, binary.BigEndian, &ln); err != nil {
		return "", err
	}
	if ln == 0xffffffff {
		return "", nil
	}
	if ln%2 != 0 || int(ln) > r.Len() {
		return "", errors.New("cadena QDataStream inválida")
	}
	buf := make([]byte, ln)
	if _, err := r.Read(buf); err != nil {
		return "", err
	}
	u := make([]uint16, ln/2)
	for i := range u {
		u[i] = binary.BigEndian.Uint16(buf[2*i:])
	}
	return string(utf16.Decode(u)), nil
}

// childOrder returns the ordered child folder ids stored in a media pool
// folder's FieldsBlob (version 2, protobuf field 2, a map of index to id).
// It returns nil when the blob has no such map.
func childOrder(fieldsBlob []byte) ([]string, error) {
	if len(fieldsBlob) < 8 || binary.BigEndian.Uint32(fieldsBlob) != 2 {
		return nil, nil
	}
	ln := binary.BigEndian.Uint32(fieldsBlob[4:])
	if int(ln) > len(fieldsBlob)-8 {
		return nil, errors.New("FieldsBlob truncado")
	}
	raw, _, err := payload(fieldsBlob[8 : 8+ln])
	if err != nil {
		return nil, err
	}
	f2, ok := protoField(raw, 2)
	if !ok {
		return nil, nil
	}
	m, err := qdsMap(f2)
	if err != nil {
		return nil, err
	}
	type kv struct {
		i  int
		id string
	}
	var items []kv
	for k, v := range m {
		i, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("índice de carpeta %q", k)
		}
		items = append(items, kv{i, v})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].i < items[b].i })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out, nil
}

var ofxContext = regexp.MustCompile(`OfxImageEffectContextFilter_[0-9a-f-]{36}_`)

// rewriteOFXContext renames OFX effect contexts inside a grade's node tree to
// the id of the version that owns it, as the Resolve exporter does for media
// pool clips. The body is returned unchanged when it has no such context.
func rewriteOFXContext(body []byte, versionID string) ([]byte, error) {
	if len(body) == 0 || (body[0] != 0x80 && body[0] != 0x81) {
		return body, nil
	}
	raw, compressed, err := payload(body)
	if err != nil {
		return nil, err
	}
	if !ofxContext.Match(raw) {
		return body, nil
	}
	repl := []byte("OfxImageEffectContextFilter_" + versionID + "_")
	out := ofxContext.ReplaceAll(raw, repl)
	if bytes.Equal(out, raw) {
		return body, nil
	}
	return encodePayload(out, compressed), nil
}
