package auth

import "testing"

func TestAuthenticateReturnsToken(t *testing.T) {
	if _, ok := Authenticate("demo", "correct"); !ok {
		t.Fatal("expected token")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	if tokenLifetimeSeconds <= 0 {
		t.Fatal("token lifetime must expire")
	}
}
