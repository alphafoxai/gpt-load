package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gpt-load/internal/channel/modules"
	"gpt-load/internal/channel/spec"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type clientKey struct{}

func WithClient(ctx context.Context, client *Client) context.Context {
	return context.WithValue(ctx, clientKey{}, client)
}

func clientFrom(ctx context.Context) *Client {
	if ctx != nil {
		if client, ok := ctx.Value(clientKey{}).(*Client); ok && client != nil {
			return client
		}
	}
	return NewClient(ProductionEndpoints())
}

type driver struct{}

func Implementations() subscriptionruntime.Implementations {
	return subscriptionruntime.Implementations{Drivers: []subscriptionruntime.Driver{driver{}}}
}

func (driver) ID() spec.SubscriptionDriverID { return modules.ZCodeSubscriptionDriver }

func (driver) Parse(raw []byte) (subscriptionruntime.Credential, error) {
	value, err := ParseCredentialJSON(raw)
	if err != nil {
		return subscriptionruntime.Credential{}, err
	}
	return runtimeCredential(value)
}

func (driver) Refresh(_ context.Context, current subscriptionruntime.Credential) (subscriptionruntime.Credential, error) {
	// A coding-plan key does not expire. Refresh confirms the stored credential
	// is still the one we issued and returns it unchanged.
	return driver{}.Parse(current.Canonical())
}

func (driver) ClassifyRefreshFailure(error) subscriptionruntime.RefreshFailureDecision {
	return subscriptionruntime.RefreshFailureDecision{Kind: subscriptionruntime.RefreshFailureOutcomeUnknown}
}

type storedFlow struct {
	Site      string    `json:"site"`
	PollAuth  string    `json:"poll_auth"`
	DeviceID  string    `json:"device_id"`
	FlowID    string    `json:"flow_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Failed    bool      `json:"failed,omitempty"`
}

type deviceState struct {
	Flows []storedFlow `json:"flows"`
}

func (driver) BeginDeviceAuthorization(ctx context.Context) (subscriptionruntime.DeviceAuthorization, error) {
	client := clientFrom(ctx)
	now := time.Now().UTC()
	var flows []startedFlow
	var providers []subscriptionruntime.AuthorizationProvider
	var firstErr error
	for _, candidate := range []site{siteBigModel, siteZAI} {
		flow, err := client.beginFlow(ctx, candidate, now)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		flows = append(flows, flow)
		providers = append(providers, subscriptionruntime.AuthorizationProvider{
			ID: string(flow.Site), Label: flow.Site.label(), URL: flow.URL,
		})
	}
	if len(flows) == 0 {
		if firstErr == nil {
			firstErr = errors.New("ZCode sign-in did not start")
		}
		return subscriptionruntime.DeviceAuthorization{}, firstErr
	}
	state := deviceState{Flows: make([]storedFlow, 0, len(flows))}
	expires := flows[0].ExpiresAt
	every := flows[0].PollEvery
	for _, flow := range flows {
		state.Flows = append(state.Flows, storedFlow{
			Site: string(flow.Site), PollAuth: flow.PollAuth, DeviceID: flow.DeviceID,
			FlowID: flow.FlowID, ExpiresAt: flow.ExpiresAt,
		})
		if flow.ExpiresAt.Before(expires) {
			expires = flow.ExpiresAt
		}
		if flow.PollEvery > every {
			every = flow.PollEvery
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return subscriptionruntime.DeviceAuthorization{}, err
	}
	if limit := now.Add(29 * time.Minute); expires.After(limit) {
		expires = limit
	}
	return subscriptionruntime.DeviceAuthorization{
		VerificationURL: flows[0].URL,
		UserCode:        "zcode",
		DriverState:     raw,
		ExpiresAt:       expires,
		PollInterval:    every,
		Providers:       providers,
	}, nil
}

func (driver) PollDeviceAuthorization(ctx context.Context, raw []byte) (subscriptionruntime.DeviceAuthorizationPoll, error) {
	var state deviceState
	if json.Unmarshal(raw, &state) != nil || len(state.Flows) == 0 {
		return subscriptionruntime.DeviceAuthorizationPoll{}, fmt.Errorf("zcode sign-in state is invalid")
	}
	client := clientFrom(ctx)
	now := time.Now().UTC()
	pending := false
	var lastErr error
	for index := range state.Flows {
		flow := &state.Flows[index]
		if flow.Failed {
			continue
		}
		if !flow.ExpiresAt.IsZero() && !flow.ExpiresAt.After(now) {
			flow.Failed = true
			continue
		}
		polled, err := client.pollFlow(ctx, startedFlow{
			Site: site(flow.Site), PollAuth: flow.PollAuth, DeviceID: flow.DeviceID, FlowID: flow.FlowID,
		})
		if err != nil {
			var api *apiError
			if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 && api.Status != 408 && api.Status != 429 {
				flow.Failed = true
				lastErr = err
				continue
			}
			pending = true
			lastErr = err
			continue
		}
		switch polled.Status {
		case "", "pending":
			pending = true
		case "failed":
			flow.Failed = true
		case "ready":
			value, err := client.complete(ctx, startedFlow{Site: site(flow.Site), DeviceID: flow.DeviceID}, polled)
			if err != nil {
				flow.Failed = true
				lastErr = err
				continue
			}
			credential, err := runtimeCredential(value)
			if err != nil {
				return subscriptionruntime.DeviceAuthorizationPoll{}, err
			}
			return subscriptionruntime.DeviceAuthorizationPoll{
				Status: subscriptionruntime.DeviceAuthorizationAuthorized, Credential: credential,
			}, nil
		default:
			flow.Failed = true
			lastErr = fmt.Errorf("ZCode sign-in: unexpected answer %s", polled.Status)
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return subscriptionruntime.DeviceAuthorizationPoll{}, err
	}
	if pending {
		return subscriptionruntime.DeviceAuthorizationPoll{
			Status: subscriptionruntime.DeviceAuthorizationPending, DriverState: encoded, PollInterval: 2 * time.Second,
		}, nil
	}
	if lastErr != nil && strings.Contains(lastErr.Error(), "expired") {
		return subscriptionruntime.DeviceAuthorizationPoll{Status: subscriptionruntime.DeviceAuthorizationExpired}, nil
	}
	return subscriptionruntime.DeviceAuthorizationPoll{Status: subscriptionruntime.DeviceAuthorizationDenied}, nil
}
