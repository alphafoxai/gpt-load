package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"gpt-load/internal/channel/modules"
	"gpt-load/internal/channel/spec"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type clientKey struct{}

type apiClient struct {
	http  *http.Client
	sites []site
}

func WithClient(ctx context.Context, client *apiClient) context.Context {
	return context.WithValue(ctx, clientKey{}, client)
}

func clientFrom(ctx context.Context) *apiClient {
	if ctx != nil {
		if client, ok := ctx.Value(clientKey{}).(*apiClient); ok && client != nil {
			return client
		}
	}
	return &apiClient{http: &http.Client{Timeout: tokenTimeout}, sites: []site{siteGlobal, siteCN}}
}

func (client *apiClient) site(id string) (site, bool) {
	for _, candidate := range client.sites {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return siteByID(id)
}

type driver struct{}

func Implementations() subscriptionruntime.Implementations {
	return subscriptionruntime.Implementations{
		Drivers:          []subscriptionruntime.Driver{driver{}},
		ModelDiscoveries: []subscriptionruntime.ModelDiscovery{modelDiscovery{}},
	}
}

type modelDiscovery struct{}

func (modelDiscovery) ID() spec.UtilityID { return modules.QoderModelDiscovery }

func (driver) ID() spec.SubscriptionDriverID { return modules.QoderSubscriptionDriver }

func (modelDiscovery) DiscoverModels(ctx context.Context, credential subscriptionruntime.Credential, _ subscriptionruntime.Target) ([]string, error) {
	value, err := ParseCredentialJSON(credential.Canonical())
	if err != nil {
		return nil, err
	}
	s, ok := clientFrom(ctx).site(value.Site)
	if !ok {
		return nil, fmt.Errorf("qoder site is unknown")
	}
	httpClient := httpClientFromContext(ctx)
	listing, err := fetchListing(ctx, httpClient, s, value)
	if err != nil {
		var upstream *upstreamError
		if errors.As(err, &upstream) {
			return nil, &subscriptionruntime.UpstreamHTTPError{StatusCode: upstream.StatusCode()}
		}
		return nil, err
	}
	names := make([]string, 0, len(listing))
	for name := range listing {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (driver) Parse(raw []byte) (subscriptionruntime.Credential, error) {
	value, err := ParseCredentialJSON(raw)
	if err != nil {
		return subscriptionruntime.Credential{}, err
	}
	return runtimeCredential(value)
}

func (driver) Refresh(ctx context.Context, current subscriptionruntime.Credential) (subscriptionruntime.Credential, error) {
	value, err := ParseCredentialJSON(current.Canonical())
	if err != nil {
		return subscriptionruntime.Credential{}, err
	}
	s, ok := clientFrom(ctx).site(value.Site)
	if !ok {
		return subscriptionruntime.Credential{}, fmt.Errorf("qoder site is unknown")
	}
	client := clientFrom(ctx)
	now := time.Now()
	if value.DeviceChat {
		token, err := refreshDeviceToken(ctx, client.http, s, value.RefreshToken)
		if err != nil {
			return subscriptionruntime.Credential{}, err
		}
		value.AccessToken = token.Token
		value.RefreshToken = token.RefreshToken
		value.DeviceToken = token.Token
		value.DeviceRefresh = token.RefreshToken
		value.ExpiresAt = deviceExpiry(token, now)
		return runtimeCredential(value)
	}
	token, err := refreshJobToken(ctx, client.http, s, value.RefreshToken)
	if err != nil {
		return subscriptionruntime.Credential{}, err
	}
	value.AccessToken = token.Token
	value.RefreshToken = token.RefreshToken
	value.ExpiresAt = jobExpiry(token, now)
	return runtimeCredential(value)
}

func (driver) ClassifyRefreshFailure(err error) subscriptionruntime.RefreshFailureDecision {
	var status *statusError
	if errors.As(err, &status) {
		decision := subscriptionruntime.RefreshFailureDecision{StatusCode: status.StatusCode()}
		switch {
		case status.StatusCode() == http.StatusUnauthorized || status.StatusCode() == http.StatusForbidden:
			decision.Kind = subscriptionruntime.RefreshFailureReauthorizationRequired
		case status.StatusCode() == http.StatusTooManyRequests || status.StatusCode() >= http.StatusInternalServerError || status.StatusCode() == http.StatusRequestTimeout:
			decision.Kind = subscriptionruntime.RefreshFailureRetryable
		default:
			decision.Kind = subscriptionruntime.RefreshFailureOutcomeUnknown
		}
		return decision
	}
	return subscriptionruntime.RefreshFailureDecision{Kind: subscriptionruntime.RefreshFailureOutcomeUnknown}
}

func (driver) MatchesRefreshIdentity(current, refreshed subscriptionruntime.Credential) bool {
	left, errLeft := ParseCredentialJSON(current.Canonical())
	right, errRight := ParseCredentialJSON(refreshed.Canonical())
	if errLeft != nil || errRight != nil {
		return false
	}
	return sameAccount(left, right)
}

type deviceState struct {
	Flows []startedFlow `json:"flows"`
}

func (driver) BeginDeviceAuthorization(ctx context.Context) (subscriptionruntime.DeviceAuthorization, error) {
	client := clientFrom(ctx)
	now := time.Now().UTC()
	state := deviceState{}
	var providers []subscriptionruntime.AuthorizationProvider
	var firstURL string
	for _, candidate := range client.sites {
		if !candidate.valid() {
			continue
		}
		flow, page, err := beginFlow(candidate, now)
		if err != nil {
			return subscriptionruntime.DeviceAuthorization{}, err
		}
		state.Flows = append(state.Flows, flow)
		providers = append(providers, subscriptionruntime.AuthorizationProvider{
			ID: candidate.ID, Label: candidate.Name, URL: page,
		})
		if firstURL == "" {
			firstURL = page
		}
	}
	if len(state.Flows) == 0 {
		return subscriptionruntime.DeviceAuthorization{}, fmt.Errorf("qoder sign-in did not start")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return subscriptionruntime.DeviceAuthorization{}, err
	}
	return subscriptionruntime.DeviceAuthorization{
		VerificationURL: firstURL,
		UserCode:        "qoder",
		DriverState:     raw,
		ExpiresAt:       now.Add(signInTimeout),
		PollInterval:    pollEvery,
		Providers:       providers,
	}, nil
}

func (driver) PollDeviceAuthorization(ctx context.Context, raw []byte) (subscriptionruntime.DeviceAuthorizationPoll, error) {
	var state deviceState
	if json.Unmarshal(raw, &state) != nil || len(state.Flows) == 0 {
		return subscriptionruntime.DeviceAuthorizationPoll{}, fmt.Errorf("qoder sign-in state is invalid")
	}
	client := clientFrom(ctx)
	now := time.Now().UTC()
	pending := false
	for index := range state.Flows {
		flow := &state.Flows[index]
		if flow.Failed {
			continue
		}
		if !flow.ExpiresAt.IsZero() && !flow.ExpiresAt.After(now) {
			flow.Failed = true
			continue
		}
		s, ok := client.site(flow.Site)
		if !ok {
			flow.Failed = true
			continue
		}
		token, ready, err := pollDeviceToken(ctx, client.http, s, *flow)
		if err != nil {
			pending = true
			continue
		}
		if !ready {
			pending = true
			continue
		}
		value, err := completeFlow(ctx, client.http, s, *flow, token, now)
		if err != nil {
			var status *statusError
			if !errors.As(err, &status) || status.StatusCode() >= 500 {
				pending = true
				continue
			}
			flow.Failed = true
			continue
		}
		credential, err := runtimeCredential(value)
		if err != nil {
			return subscriptionruntime.DeviceAuthorizationPoll{}, err
		}
		return subscriptionruntime.DeviceAuthorizationPoll{
			Status: subscriptionruntime.DeviceAuthorizationAuthorized, Credential: credential,
		}, nil
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return subscriptionruntime.DeviceAuthorizationPoll{}, err
	}
	if pending {
		return subscriptionruntime.DeviceAuthorizationPoll{
			Status: subscriptionruntime.DeviceAuthorizationPending, DriverState: encoded, PollInterval: pollEvery,
		}, nil
	}
	expired := true
	for _, flow := range state.Flows {
		if flow.ExpiresAt.After(now) {
			expired = false
		}
	}
	if expired {
		return subscriptionruntime.DeviceAuthorizationPoll{Status: subscriptionruntime.DeviceAuthorizationExpired}, nil
	}
	return subscriptionruntime.DeviceAuthorizationPoll{Status: subscriptionruntime.DeviceAuthorizationDenied}, nil
}
