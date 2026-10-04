package qoder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	signInTimeout = 15 * time.Minute
	pollEvery     = 2 * time.Second
	tokenTimeout  = 20 * time.Second
	day           = 24 * time.Hour
)

type statusError struct {
	op     string
	status int
}

func (e *statusError) Error() string {
	if e == nil {
		return "qoder request failed"
	}
	return fmt.Sprintf("qoder %s: status %d", e.op, e.status)
}

func (e *statusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

type deviceToken struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	UserName     string `json:"user_name"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    string `json:"expires_at"`
}

type jobToken struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type startedFlow struct {
	Site      string    `json:"site"`
	Verifier  string    `json:"verifier"`
	Nonce     string    `json:"nonce"`
	MachineID string    `json:"machine_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Failed    bool      `json:"failed,omitempty"`
}

func beginFlow(s site, now time.Time) (startedFlow, string, error) {
	raw := make([]byte, 64)
	if _, err := io.ReadFull(randReader, raw); err != nil {
		return startedFlow{}, "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	nonce := newUUID()
	machineID := newUUID()
	query := url.Values{}
	query.Set("challenge", challenge)
	query.Set("challenge_method", "S256")
	query.Set("nonce", nonce)
	query.Set("machine_id", machineID)
	query.Set("client_id", s.ClientID)
	if s.RedirectURI != "" {
		query.Set("redirect_uri", s.RedirectURI)
	}
	flow := startedFlow{
		Site: s.ID, Verifier: verifier, Nonce: nonce, MachineID: machineID,
		ExpiresAt: now.Add(signInTimeout),
	}
	return flow, s.Web + deviceSelectPath + "?" + query.Encode(), nil
}

func pollDeviceToken(ctx context.Context, client *http.Client, s site, flow startedFlow) (deviceToken, bool, error) {
	endpoint, err := url.Parse(s.OpenAPI + devicePollPath)
	if err != nil {
		return deviceToken{}, false, err
	}
	query := endpoint.Query()
	query.Set("nonce", flow.Nonce)
	query.Set("verifier", flow.Verifier)
	query.Set("challenge_method", "S256")
	endpoint.RawQuery = query.Encode()
	status, raw, err := doJSON(ctx, client, http.MethodGet, endpoint.String(), "", nil)
	if err != nil {
		return deviceToken{}, false, err
	}
	if status != http.StatusOK {
		return deviceToken{}, false, nil
	}
	var token deviceToken
	if json.Unmarshal(raw, &token) != nil || strings.TrimSpace(token.Token) == "" {
		return deviceToken{}, false, nil
	}
	return token, true, nil
}

func exchangeJobToken(ctx context.Context, client *http.Client, s site, device string) (jobToken, int, error) {
	status, raw, err := doJSON(ctx, client, http.MethodPost, s.OpenAPI+jobTokenPath, device, map[string]string{"clientId": s.ClientID})
	if err != nil {
		return jobToken{}, 0, err
	}
	if status != http.StatusOK {
		return jobToken{}, status, &statusError{op: "job token", status: status}
	}
	var token jobToken
	if json.Unmarshal(raw, &token) != nil || strings.TrimSpace(token.Token) == "" {
		return jobToken{}, status, fmt.Errorf("qoder job token: empty token")
	}
	return token, status, nil
}

func refreshJobToken(ctx context.Context, client *http.Client, s site, refresh string) (jobToken, error) {
	if strings.TrimSpace(refresh) == "" {
		return jobToken{}, &statusError{op: "job token refresh", status: http.StatusUnauthorized}
	}
	status, raw, err := doJSON(ctx, client, http.MethodPost, s.OpenAPI+jobRefreshPath, "", map[string]string{"refresh_token": refresh})
	if err != nil {
		return jobToken{}, err
	}
	if status != http.StatusOK {
		return jobToken{}, &statusError{op: "job token refresh", status: status}
	}
	var token jobToken
	if json.Unmarshal(raw, &token) != nil || strings.TrimSpace(token.Token) == "" || strings.TrimSpace(token.RefreshToken) == "" {
		return jobToken{}, fmt.Errorf("qoder job token refresh: incomplete token pair")
	}
	return token, nil
}

func refreshDeviceToken(ctx context.Context, client *http.Client, s site, refresh string) (deviceToken, error) {
	if strings.TrimSpace(refresh) == "" {
		return deviceToken{}, &statusError{op: "device token refresh", status: http.StatusUnauthorized}
	}
	status, raw, err := doJSON(ctx, client, http.MethodPost, s.OpenAPI+deviceRefreshPath, "", map[string]string{"refresh_token": refresh})
	if err != nil {
		return deviceToken{}, err
	}
	if status != http.StatusOK {
		return deviceToken{}, &statusError{op: "device token refresh", status: status}
	}
	var token deviceToken
	if json.Unmarshal(raw, &token) != nil || strings.TrimSpace(token.Token) == "" || strings.TrimSpace(token.RefreshToken) == "" {
		return deviceToken{}, fmt.Errorf("qoder device token refresh: incomplete token pair")
	}
	return token, nil
}

type userInfo struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

func fetchUserInfo(ctx context.Context, client *http.Client, s site, device string) userInfo {
	status, raw, err := doJSON(ctx, client, http.MethodGet, s.OpenAPI+userInfoPath, device, nil)
	if err != nil || status < 200 || status >= 300 {
		return userInfo{}
	}
	var info userInfo
	_ = json.Unmarshal(raw, &info)
	return info
}

func jobExpiry(token jobToken, now time.Time) time.Time {
	if token.ExpiresIn > 0 {
		return now.Add(time.Duration(token.ExpiresIn) * time.Millisecond)
	}
	return now.Add(day)
}

func deviceExpiry(token deviceToken, now time.Time) time.Time {
	if at, err := time.Parse(time.RFC3339, strings.TrimSpace(token.ExpiresAt)); err == nil {
		return at
	}
	if token.ExpiresIn > 0 {
		return now.Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return now.Add(day)
}

func doJSON(ctx context.Context, client *http.Client, method, endpoint, bearer string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if client == nil {
		client = &http.Client{Timeout: tokenTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

func completeFlow(ctx context.Context, client *http.Client, s site, flow startedFlow, device deviceToken, now time.Time) (Credential, error) {
	if strings.TrimSpace(device.UserID) == "" {
		return Credential{}, fmt.Errorf("qoder sign-in: missing user id")
	}
	info := fetchUserInfo(ctx, client, s, device.Token)
	name := info.Name
	if name == "" {
		name = device.UserName
	}
	value := Credential{
		Site: s.ID, UID: device.UserID, Name: name, Email: info.Email,
		DeviceToken: device.Token, DeviceRefresh: device.RefreshToken, MachineID: flow.MachineID,
	}
	job, status, err := exchangeJobToken(ctx, client, s, device.Token)
	if err == nil {
		value.AccessToken = job.Token
		value.RefreshToken = job.RefreshToken
		value.ExpiresAt = jobExpiry(job, now)
		if value.RefreshToken == "" {
			return Credential{}, fmt.Errorf("qoder job token: missing refresh token")
		}
		return value, nil
	}
	if s.DeviceChat && status >= 400 && status < 500 {
		if strings.TrimSpace(device.RefreshToken) == "" {
			return Credential{}, fmt.Errorf("qoder sign-in: missing device refresh token")
		}
		value.AccessToken = device.Token
		value.RefreshToken = device.RefreshToken
		value.ExpiresAt = deviceExpiry(device, now)
		value.DeviceChat = true
		return value, nil
	}
	return Credential{}, err
}
