package compiler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxMessageBytes = 8 << 20
const maxHeaderBytes = 8 << 10

// ReadMessage reads the official LSP Content-Length framing with independent
// header and body ceilings. The reader must be reused across messages.
func ReadMessage(reader *bufio.Reader) (json.RawMessage, error) {
	length := -1
	headerBytes := 0
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		headerBytes += len(line)
		if headerBytes > maxHeaderBytes {
			return nil, errors.New("compiler protocol header limit")
		}
		if !bytes.HasSuffix(line, []byte("\r\n")) {
			return nil, errors.New("invalid compiler protocol header")
		}
		if len(line) == 2 {
			break
		}
		for _, b := range line {
			if b > 127 {
				return nil, errors.New("non-ASCII compiler header")
			}
		}
		name, value, ok := strings.Cut(string(line[:len(line)-2]), ":")
		if !ok {
			return nil, errors.New("invalid compiler header field")
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(name) {
		case "content-length":
			if length >= 0 {
				return nil, errors.New("duplicate compiler content length")
			}
			length, err = strconv.Atoi(value)
			if err != nil || length < 0 || length > MaxMessageBytes {
				return nil, errors.New("compiler protocol body limit")
			}
		case "content-type":
			media, params, err := mime.ParseMediaType(value)
			if err != nil || media != "application/vscode-jsonrpc" {
				return nil, errors.New("unsupported compiler content type")
			}
			charset := strings.ToLower(params["charset"])
			if charset != "" && charset != "utf-8" && charset != "utf8" {
				return nil, errors.New("unsupported compiler encoding")
			}
		}
	}
	if length < 0 {
		return nil, errors.New("missing compiler content length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		return nil, errors.New("invalid compiler JSON")
	}
	return body, nil
}

func WriteMessage(writer io.Writer, body json.RawMessage) error {
	if len(body) > MaxMessageBytes || !utf8.Valid(body) || !json.Valid(body) {
		return errors.New("invalid compiler message")
	}
	frame := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)))
	frame = append(frame, body...)
	n, err := writer.Write(frame)
	if err == nil && n != len(frame) {
		return io.ErrShortWrite
	}
	return err
}
