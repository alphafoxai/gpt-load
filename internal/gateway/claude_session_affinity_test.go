package gateway

import (
	"testing"

	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
	"gpt-load/internal/telemetry"
)

// Claude Code 同一项目的不同会话，提示词前缀（系统提示和第一条消息开头的 CLAUDE.md）往往完全相同；
// 会话 ID 必须把它们分开，同时同一会话里的辅助小模型请求不能和主模型共用绑定。
func TestClaudeCodeSessionAffinitySeparatesConversationsSharingAPrefix(t *testing.T) {
	handler, manager, _ := newHandlerForTest(t, &scriptedForwarder{}, "sk-one")
	snapshot := manager.Current()
	refs := map[uint]state.CredentialRef{1: {ID: 1, GroupID: 1, IdentityGeneration: 1}}
	resolve := func(signals affinitySignals) requestAffinity {
		signals.prefix = []byte(`{"v":1,"system_role":"system","system":["shared"],"user":["shared CLAUDE.md"]}`)
		return handler.resolveRequestAffinity(snapshot, 1, protocol.Anthropic, signals, refs, []uint{1})
	}
	const sessionA, sessionB = "0f8fad5b-d9cb-469f-a165-70867728950e", "7c9e6679-7425-40de-944b-e07fc1f90ae7"

	mainA := resolve(affinitySignals{claudeCodeSessionID: sessionA, model: "claude-opus"})
	mainB := resolve(affinitySignals{claudeCodeSessionID: sessionB, model: "claude-opus"})
	helperA := resolve(affinitySignals{claudeCodeSessionID: sessionA, model: "claude-haiku"})
	noSessionFirst := resolve(affinitySignals{model: "claude-opus"})
	noSessionSecond := resolve(affinitySignals{model: "claude-opus"})
	cacheKeyed := resolve(affinitySignals{promptCacheKey: "client-group", claudeCodeSessionID: sessionA, model: "claude-opus"})

	for name, resolved := range map[string]requestAffinity{"main A": mainA, "main B": mainB, "helper A": helperA} {
		if resolved.kind != telemetry.AffinityClaudeCodeSession || !resolved.key.Valid() {
			t.Fatalf("%s affinity = kind %q key %q, want a Claude Code session key", name, resolved.kind, resolved.key)
		}
	}
	if mainA.key == mainB.key || mainA.key == helperA.key || mainA.key == noSessionFirst.key {
		t.Fatal("distinct sessions, models or the prompt-prefix fallback share an affinity key")
	}
	if noSessionFirst.kind != telemetry.AffinityPromptPrefix || noSessionFirst.key != noSessionSecond.key {
		t.Fatal("requests without a session id must fall back to the shared prompt prefix")
	}
	if cacheKeyed.kind != telemetry.AffinityPromptCacheKey {
		t.Fatalf("prompt_cache_key kind = %q, want precedence over the session id", cacheKeyed.kind)
	}
	// provider-private replay 的作用域仍由提示词派生，会话 ID 不改变它。
	if mainA.continuityKey != noSessionFirst.continuityKey {
		t.Fatal("session id changed the replay continuity scope")
	}
}
