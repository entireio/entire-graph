//go:build goexperiment.jsonv2

package sem

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"io"
)

// encodeCacheValue writes value as json.NewEncoder(w) with SetEscapeHTML(false) would: the v1
// encoding followed by one newline. It streams instead of buffering.
//
// json.Encoder.Encode marshals the whole value into one buffer before its single Write, so
// persisting a snapshot used to hold the snapshot AND its complete uncompressed encoding at once.
// On large repositories that encoding is gigabytes. MarshalWrite with the same v1 options flushes
// to w as the buffer fills, so peak memory is the snapshot plus a bounded buffer. The bytes are
// identical; TestEncodeCacheValueMatchesV1Encoder pins that.
// cacheEncodeStreams reports whether encodeCacheValue streams (true on jsonv2 toolchains).
const cacheEncodeStreams = true

func encodeCacheValue(w io.Writer, value any) error {
	if err := jsonv2.MarshalWrite(w, value, json.DefaultOptionsV1(), jsontext.EscapeForHTML(false)); err != nil {
		return err
	}
	_, err := w.Write([]byte{'\n'})
	return err
}

// decodeCacheValue reads one JSON value as json.NewDecoder(r).Decode followed
// by a trailing-data check would: extraneous data after the value is an error,
// and the trailing newline encodeCacheValue writes is whitespace, which is
// allowed. UnmarshalRead decodes incrementally from the reader instead of
// buffering the whole document the way the v1 decoder's internal scan does —
// on a large cached search snapshot the v1 decode was measured at 3.1s of an
// 8s warm query.
func decodeCacheValue(r io.Reader, value any) error {
	return jsonv2.UnmarshalRead(r, value, json.DefaultOptionsV1())
}
