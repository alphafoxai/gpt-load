package mirasim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

const (
	emailCodeResource   = "/auth/code"
	emailVerifyResource = "/auth/verify"
	emailLoginTimeout   = 20 * time.Second
	maxAdminResponse    = 64 * 1024
)

var emailAddress = regexp.MustCompile(`^[^@\s]+@[^@\s.]+(\.[^@\s.]+)+$`)

func (*driver) SendEmailLoginCode(ctx context.Context, driverState []byte, email string) error {
	state, email, _, err := emailLoginInput(driverState, email, "")
	if err != nil {
		return err
	}
	return requestEmailCode(ctx, state.AdminURL, email)
}

func (*driver) ExchangeEmailLoginCode(ctx context.Context, driverState []byte, email, code string) (string, string, error) {
	state, email, code, err := emailLoginInput(driverState, email, code)
	if err != nil {
		return "", "", err
	}
	return verifyEmailCode(ctx, state.AdminURL, email, code)
}

func emailLoginInput(driverState []byte, email, code string) (oauthDriverState, string, string, error) {
	var state oauthDriverState
	if json.Unmarshal(driverState, &state) != nil || strings.TrimSpace(state.AdminURL) == "" {
		return oauthDriverState{}, "", "", errInvalidOAuthState
	}
	email, err := normalizeLoginEmail(email)
	if err != nil {
		return oauthDriverState{}, "", "", err
	}
	if strings.TrimSpace(code) != "" {
		code, err = normalizeLoginCode(code)
		if err != nil {
			return oauthDriverState{}, "", "", err
		}
	}
	return state, email, code, nil
}

func requestEmailCode(ctx context.Context, adminURL, email string) error {
	_, err := postAdminJSON(ctx, adminURL, emailCodeResource, map[string]string{"email": email})
	return err
}

func verifyEmailCode(ctx context.Context, adminURL, email, code string) (string, string, error) {
	raw, err := postAdminJSON(ctx, adminURL, emailVerifyResource, map[string]string{"email": email, "code": code})
	if err != nil {
		return "", "", err
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return "", "", subscriptionruntime.ErrEmailLoginRejected
	}
	accessToken := strings.TrimSpace(payload.AccessToken)
	refreshToken := strings.TrimSpace(payload.RefreshToken)
	if accessToken == "" || refreshToken == "" {
		return "", "", subscriptionruntime.ErrEmailLoginRejected
	}
	return accessToken, refreshToken, nil
}

func postAdminJSON(ctx context.Context, adminURL, resource string, body map[string]string) ([]byte, error) {
	base, err := adminBaseURL(adminURL)
	if err != nil {
		return nil, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + resource
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, emailLoginTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, base.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, subscriptionruntime.ErrEmailLoginUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: emailLoginTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, subscriptionruntime.ErrEmailLoginUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAdminResponse+1))
	if err != nil || len(raw) > maxAdminResponse {
		return nil, subscriptionruntime.ErrEmailLoginRejected
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, subscriptionruntime.ErrEmailLoginRejected
	}
	return raw, nil
}

func normalizeLoginEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 254 || !emailAddress.MatchString(value) {
		return "", fmt.Errorf("invalid Mirasim account email address")
	}
	return value, nil
}

func normalizeLoginCode(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("invalid Mirasim sign-in code")
	}
	return value, nil
}
