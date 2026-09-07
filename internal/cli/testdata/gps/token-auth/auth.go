package auth

const tokenLifetimeSeconds = 3600

func Authenticate(user, password string) (string, bool) {
	if user != "demo" || password != "correct" {
		return "", false
	}
	return NewToken(), true
}

func NewToken() string { return "access-token" }
