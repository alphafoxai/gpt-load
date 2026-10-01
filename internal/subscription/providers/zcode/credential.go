package zcode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func ParseCredentialJSON(raw []byte) (Credential, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var value Credential
	if err := dec.Decode(&value); err != nil {
		return Credential{}, fmt.Errorf("parse zcode credential: %w", err)
	}
	if !site(value.Site).valid() || strings.TrimSpace(value.APIKey) == "" || strings.TrimSpace(value.BaseURL) == "" {
		return Credential{}, fmt.Errorf("parse zcode credential: incomplete")
	}
	return value, nil
}

func MarshalCredential(value Credential) ([]byte, error) {
	return json.Marshal(value)
}

func runtimeCredential(value Credential) (subscriptionruntime.Credential, error) {
	canonical, err := MarshalCredential(value)
	if err != nil {
		return subscriptionruntime.Credential{}, err
	}
	identity := value.Site + ":" + firstString(value.Account, keyID(value.APIKey))
	secrets := []string{value.APIKey}
	if value.Token != "" && value.Token != value.APIKey {
		secrets = append(secrets, value.Token)
	}
	return subscriptionruntime.NewCredential(
		canonical, identity, subscriptionruntime.Account{Email: value.Account}, time.Time{}, false, secrets,
	), nil
}

func keyID(apiKey string) string {
	if id, _, ok := strings.Cut(apiKey, "."); ok && id != "" {
		return id
	}
	return apiKey
}
