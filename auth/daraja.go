package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
)

// tokenResponse mirrors the JSON body returned by the Daraja OAuth endpoint.
// ExpiresIn is a string in the Daraja response — parsed to int64 by GetAccessToken.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
}

// fetchToken makes a GET request to the Daraja OAuth endpoint and returns
// a raw Bearer token and its expiry duration as strings.
//
// Parsing expires_in to an integer and caching are the caller's responsibility
// — this function is deliberately thin and returns exactly what Daraja sends.
//
// The Authorization header uses HTTP Basic auth with the consumerKey and
// consumerSecret base64-encoded as "consumerKey:consumerSecret".
func (m *Manager) fetchToken(ctx context.Context) (token, expiresIn string, err error) {
	// Build Basic auth header — base64(consumerKey:consumerSecret)
	credentials := base64.StdEncoding.EncodeToString(
		[]byte(m.cfg.ConsumerKey + ":" + m.cfg.ConsumerSecret),
	)

	url := m.cfg.Environment.baseURL() + "/oauth/v1/generate?grant_type=client_credentials"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("fetchToken: failed to build request: %w", err)
	}

	req.Header.Set("Authorization", "Basic "+credentials)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetchToken: request failed: %w", err)
	}
	// Deferred immediately after confirming resp is non-nil — ensures the
	// connection is released regardless of status code or decode outcome.
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fetchToken: unexpected status %d", resp.StatusCode)
	}

	var result tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("fetchToken: failed to decode response: %w", err)
	}

	return result.AccessToken, result.ExpiresIn, nil
}
