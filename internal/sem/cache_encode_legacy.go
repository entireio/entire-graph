//go:build !goexperiment.jsonv2

package sem

import (
	"encoding/json"
	"errors"
	"io"
)

// encodeCacheValue is the fallback for toolchains built without the jsonv2 experiment, which
// has no streaming marshaler. It buffers the whole encoding, as persistence always did. See
// cache_encode_jsonv2.go.
// cacheEncodeStreams reports whether encodeCacheValue streams; this fallback buffers.
const cacheEncodeStreams = false

func encodeCacheValue(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

// decodeCacheValue is the v1 fallback: one value, then nothing but EOF. See
// cache_encode_jsonv2.go for the streaming variant and its contract.
func decodeCacheValue(r io.Reader, value any) error {
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("cache artifact has trailing data")
	}
	return nil
}
