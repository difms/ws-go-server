package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"ws-go-server/models"
)

// AuthenticateWithSanctum verifies the Sanctum token and retrieves user information
func AuthenticateWithSanctum(token string) (*models.User, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", "http://127.0.0.1:8000/api/getUser", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to authenticate: %s", resp.Status)
	}

	var user models.User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}
