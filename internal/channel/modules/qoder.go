package modules

import (
	"gpt-load/internal/channel/spec"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
)

const QoderSubscriptionDriver spec.SubscriptionDriverID = "qoder"

// Qoder declares the subscription channel for a Qoder plan. Sign-in is the
// desktop client's device flow. Upstream chat is translated to Qoder's
// COSY-signed agent API and back to the caller's format.
func Qoder() spec.Module {
	return spec.Module{
		Definition: spec.Definition{
			ID:          spec.Qoder,
			Name:        "Qoder",
			Mark:        "QD",
			Icon:        "qoder",
			SearchTerms: []string{"subscription", "oauth", "qoder", "qoder-cn"},
			Description: "Qoder subscription",
			Connection: spec.Connection{
				Type:            spec.ConnectionSubscription,
				CredentialInput: "authorization",
				AuthorizationMethods: []spec.AuthorizationMethod{
					spec.AuthorizationDeviceOAuth,
				},
			},
			Provider: spec.ProviderBinding{
				ProviderKind:    spec.ProviderQoder,
				EndpointPolicy:  spec.EndpointSDKDefault,
				DefaultBaseURLs: []string{"https://api3.qoder.sh"},
			},
			Routes: []spec.Route{
				spec.NewRoute(protocol.OpenAICompletions, execution.OperationChatCompletion, execution.RouteNative),
				spec.NewRoute(protocol.Anthropic, execution.OperationChatCompletion, execution.RouteConverted),
				spec.NewResponsesCreateRoute(execution.RouteConverted, spec.ResponsesStoreHandlingStateless),
				spec.NewRoute(protocol.Gemini, execution.OperationChatCompletion, execution.RouteConverted),
			},
			Capabilities: spec.CapabilityBindings{
				SubscriptionDriver: QoderSubscriptionDriver,
			},
		},
	}
}
