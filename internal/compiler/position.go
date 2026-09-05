package compiler

import (
	"errors"
	"unicode/utf8"
)

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// PositionAt maps exact UTF-8 byte boundaries to mandatory LSP UTF-16 positions.
func PositionAt(source string, offset int) (Position, error) {
	if offset < 0 || offset > len(source) || !utf8.ValidString(source) {
		return Position{}, errors.New("invalid compiler source offset")
	}
	position := Position{}
	for at := 0; at < offset; {
		r, size := utf8.DecodeRuneInString(source[at:])
		if r == '\r' && at+1 < len(source) && source[at+1] == '\n' {
			size = 2
		}
		if at+size > offset {
			return Position{}, errors.New("compiler offset splits encoding or line ending")
		}
		if r == '\r' || r == '\n' {
			position.Line++
			position.Character = 0
		} else if r > 0xffff {
			position.Character += 2
		} else {
			position.Character++
		}
		at += size
	}
	return position, nil
}

// OffsetAt refuses out-of-range positions instead of clamping them to a nearby
// declaration. This stricter evidence-import rule avoids speculative mapping.
func OffsetAt(source string, position Position) (int, error) {
	if position.Line < 0 || position.Character < 0 || !utf8.ValidString(source) {
		return 0, errors.New("invalid compiler position")
	}
	current := Position{}
	for at := 0; at <= len(source); {
		if current == position {
			return at, nil
		}
		if at == len(source) || current.Line > position.Line {
			break
		}
		r, size := utf8.DecodeRuneInString(source[at:])
		if r == '\r' && at+1 < len(source) && source[at+1] == '\n' {
			size = 2
		}
		if r == '\r' || r == '\n' {
			current.Line++
			current.Character = 0
		} else if r > 0xffff {
			current.Character += 2
		} else {
			current.Character++
		}
		at += size
	}
	return 0, errors.New("compiler position is not an exact source boundary")
}
