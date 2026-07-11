package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func main() {
	// Login first. Never commit real credentials; provide them via environment.
	email := os.Getenv("TEST_API_EMAIL")
	password := os.Getenv("TEST_API_PASSWORD")
	if email == "" || password == "" {
		fmt.Println("TEST_API_EMAIL and TEST_API_PASSWORD are required")
		return
	}
	loginBody := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	resp, err := http.Post("http://localhost:8080/api/v1/auth/login", "application/json", strings.NewReader(loginBody))
	if err != nil {
		fmt.Printf("Login error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	var loginResp map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&loginResp)
	token, ok := loginResp["token"].(string)
	if !ok {
		fmt.Printf("No token in response: %v\n", loginResp)
		return
	}
	fmt.Printf("Login OK, token length: %d\n\n", len(token))

	// Test endpoints
	endpoints := []string{
		"/api/v1/auth/me",
		"/api/v1/dashboard/stats",
		"/api/v1/nodes",
		"/api/v1/vms",
		"/api/v1/templates",
		"/api/v1/storage/pools",
		"/api/v1/users",
		"/api/v1/audit-logs",
		"/api/v1/settings/profile",
		"/api/v1/notifications",
		"/api/v1/networks",
	}

	fmt.Printf("%-35s %-6s %s\n", "ENDPOINT", "STATUS", "RESPONSE (first 100 chars)")
	fmt.Println(strings.Repeat("-", 140))

	for _, ep := range endpoints {
		req, _ := http.NewRequest("GET", "http://localhost:8080"+ep, nil)
		req.Header.Set("Authorization", "Bearer "+token)

		client := &http.Client{}
		r, err := client.Do(req)
		if err != nil {
			fmt.Printf("%-35s ERROR  %v\n", ep, err)
			continue
		}

		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		bodyStr := string(body)
		if len(bodyStr) > 100 {
			bodyStr = bodyStr[:100]
		}
		fmt.Printf("%-35s %-6d %s\n", ep, r.StatusCode, bodyStr)
	}
}
