package mirasim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	accountProfilePath = "/auth/me"
	profileTimeout     = 5 * time.Second
	// profileRefreshInterval is how long an account profile stays trusted. The
	// official client re-reads it on the same cadence, so a plan change is
	// noticed within one interval without asking the service on every call.
	profileRefreshInterval = 5 * time.Minute
)

// AccountProfile is what the authentication service reports about the signed-in
// account. It is authoritative where the token's own claims are only a hint.
type AccountProfile struct {
	Email           string
	Plan            string
	PlanExpiresAt   *int64
	PlanExpiryKnown bool
}

// FetchAccountProfile reads the account profile with the access token. It is a
// plain authenticated call to the authentication service, not a signed relay
// call: the profile describes the account rather than a conversation.
func (c *Client) FetchAccountProfile(ctx context.Context) (AccountProfile, error) {
	c.mu.Lock()
	if err := c.loadLocked(); err != nil {
		c.mu.Unlock()
		return AccountProfile{}, err
	}
	accessToken := c.accessToken
	adminURL := strings.TrimRight(strings.TrimSpace(c.storage.AdminURL), "/")
	c.mu.Unlock()
	if accessToken == "" {
		return AccountProfile{}, fmt.Errorf("Mirasim access token is unavailable")
	}
	if adminURL == "" {
		return AccountProfile{}, fmt.Errorf("Mirasim admin URL is unavailable")
	}
	requestCtx, cancel := context.WithTimeout(ctx, profileTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, adminURL+accountProfilePath, nil)
	if err != nil {
		return AccountProfile{}, fmt.Errorf("create Mirasim profile request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	client := &http.Client{Timeout: profileTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		return AccountProfile{}, fmt.Errorf("read Mirasim account profile: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody+1))
	if err != nil {
		return AccountProfile{}, fmt.Errorf("read Mirasim account profile: %w", err)
	}
	if len(body) > maxErrorBody {
		return AccountProfile{}, fmt.Errorf("Mirasim account profile exceeds %d bytes", maxErrorBody)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return AccountProfile{}, fmt.Errorf("Mirasim account profile returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Email   string          `json:"email"`
		Plan    string          `json:"plan"`
		PlanExp json.RawMessage `json:"plan_exp"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return AccountProfile{}, fmt.Errorf("decode Mirasim account profile: %w", err)
	}
	profile := AccountProfile{
		Email: safeProfileValue(payload.Email, 320),
		Plan:  safeProfileValue(payload.Plan, 128),
	}
	if len(payload.PlanExp) > 0 && string(payload.PlanExp) != "null" {
		profile.PlanExpiryKnown = true
		var number json.Number
		if err := json.Unmarshal(payload.PlanExp, &number); err == nil {
			if expiresAt, err := number.Int64(); err == nil && expiresAt > 0 {
				profile.PlanExpiresAt = &expiresAt
			}
		}
	}
	return profile, nil
}

func safeProfileValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit || strings.ContainsAny(value, "\x00\r\n") {
		return ""
	}
	return value
}
