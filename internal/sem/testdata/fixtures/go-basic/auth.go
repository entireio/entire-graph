package auth

import "strings"

// Token is an opaque authentication token.
type Token struct {
	Value string
}

// Validate reports whether the token has a non-empty value.
func (t Token) Validate() bool {
	return strings.TrimSpace(t.Value) != ""
}

// CheckToken builds a Token and validates it.
func CheckToken(value string) bool {
	t := Token{Value: value}
	return t.Validate()
}
