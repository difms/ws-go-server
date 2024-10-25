package ws

import (
	"fmt"
	"net/http"

	"ws-go-server/auth"
)

// JWTMiddleware verifies the token and attaches userID to the request header
func JWTMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.URL.Query().Get("token")
		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

		token := authHeader
		user, err := auth.AuthenticateWithSanctum(token)
		if err != nil {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		r.Header.Set("UserID", fmt.Sprintf("%d", user.ID))
		next.ServeHTTP(w, r)
	}
}
