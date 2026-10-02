package modules

import (
	"gpt-load/internal/channel/spec"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
)

const ZCodeSubscriptionDriver spec.SubscriptionDriverID = "zcode"

// ZCode declares the subscription channel for a GLM Coding Plan reached
// through ZCode's polled sign-in. Upstream requests are Anthropic Messages.
func ZCode() spec.Module {
	return spec.Module{
		Definition: spec.Definition{
			ID:          spec.ZCode,
			Name:        "ZCode",
			Mark:        "ZC",
			Icon:        "zhipu",
			SearchTerms: []string{"subscription", "oauth", "zcode", "glm", "zhipu", "bigmodel"},
			Description: "ZCode GLM Coding Plan",
			Connection: spec.Connection{
				Type:            spec.ConnectionSubscription,
				CredentialInput: "authorization",
				AuthorizationMethods: []spec.AuthorizationMethod{
					spec.AuthorizationDeviceOAuth,
				},
			},
			Params: []spec.Field{{
				Key: "base_url", Label: "API root URL", InputKind: spec.InputURL,
				Normalizer: spec.NormalizeOptionalHTTPSBaseURL,
			}},
			Provider: spec.ProviderBinding{
				ProviderKind:    spec.ProviderZCode,
				EndpointPolicy:  spec.EndpointSDKDefault,
				DefaultBaseURLs: []string{"https://open.bigmodel.cn/api/anthropic"},
			},
			Routes: []spec.Route{
				spec.NewRoute(protocol.Anthropic, execution.OperationChatCompletion, execution.RouteNative),
				spec.NewRoute(protocol.OpenAICompletions, execution.OperationChatCompletion, execution.RouteConverted),
				spec.NewResponsesCreateRoute(execution.RouteConverted, spec.ResponsesStoreHandlingStateless),
				spec.NewRoute(protocol.Gemini, execution.OperationChatCompletion, execution.RouteConverted),
			},
			Capabilities: spec.CapabilityBindings{
				SubscriptionDriver: ZCodeSubscriptionDriver,
			},
		},
	}
}
