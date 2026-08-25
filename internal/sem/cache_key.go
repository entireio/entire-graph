package sem

import (
	"encoding/binary"
	"io"
)

// writeCacheKeyField writes an unambiguous typed field. Both the tag and value
// are length-prefixed, so attacker-controlled bytes (including NULs and bytes
// that look like later fields) cannot cross a serialization boundary.
func writeCacheKeyField(writer io.Writer, tag string, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(tag)))
	_, _ = writer.Write(length[:])
	_, _ = io.WriteString(writer, tag)
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func writeCacheKeyString(writer io.Writer, tag, value string) {
	writeCacheKeyField(writer, tag, []byte(value))
}
