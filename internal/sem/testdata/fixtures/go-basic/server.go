package auth

import "net/http"

// RegisterRoutes wires the auth HTTP handlers.
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/login", LoginHandler)
}

// LoginHandler handles POST /login requests.
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	if CheckToken(r.Header.Get("Authorization")) {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
}
