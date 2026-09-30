//go:build !goexperiment.jsonv2

package sem

import (
	"encoding/json"
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
