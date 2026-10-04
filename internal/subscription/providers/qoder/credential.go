package qoder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

// Credential is one signed-in Qoder account. The job token (access_token)
// signs model calls. Qoder spends a refresh token once, so a refresh must
// replace the stored pair. On Qoder CN, when the site will not issue a job
// token, the device token itself is the chat token (device_chat).
type Credential struct {
	Site          string    `json:"site"`
	UID           string    `json:"uid"`
	Name          string    `json:"name,omitempty"`
	Email         string    `json:"email,omitempty"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	ExpiresAt     time.Time `json:"expires_at"`
	DeviceToken   string    `json:"device_token"`
	DeviceRefresh string    `json:"device_refresh,omitempty"`
	MachineID     string    `json:"machine_id"`
	DeviceChat    bool      `json:"device_chat,omitempty"`
}

func ParseCredentialJSON(raw []byte) (Credential, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var value Credential
	if err := dec.Decode(&value); err != nil {
		return Credential{}, fmt.Errorf("parse qoder credential: %w", err)
	}
	if _, ok := siteByID(value.Site); !ok || strings.TrimSpace(value.UID) == "" ||
		strings.TrimSpace(value.AccessToken) == "" || strings.TrimSpace(value.RefreshToken) == "" ||
		strings.TrimSpace(value.MachineID) == "" || value.ExpiresAt.IsZero() {
		return Credential{}, fmt.Errorf("parse qoder credential: incomplete")
	}
	return value, nil
}

func (value Credential) user() cosyUser {
	return cosyUser{
		UID: value.UID, Name: value.Name, Email: value.Email,
		Token: value.AccessToken, MachineID: value.MachineID,
	}
}

func runtimeCredential(value Credential) (subscriptionruntime.Credential, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return subscriptionruntime.Credential{}, err
	}
	display := value.Email
	if display == "" {
		display = value.Name
	}
	if display == "" {
		display = value.UID
	}
	secrets := uniqueSecrets(value.AccessToken, value.RefreshToken, value.DeviceToken, value.DeviceRefresh)
	return subscriptionruntime.NewCredential(
		canonical, value.Site+":"+value.UID,
		subscriptionruntime.Account{Email: display, ExpiresAt: value.ExpiresAt, ExpiresAtKnown: true},
		value.ExpiresAt, true, secrets,
	), nil
}

func uniqueSecrets(values ...string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func sameAccount(left, right Credential) bool {
	return left.Site == right.Site && left.UID == right.UID && left.MachineID == right.MachineID
}
