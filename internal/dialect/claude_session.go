package dialect

import (
	"strings"

	"github.com/tidwall/gjson"
)

const claudeCodeLegacySessionMarker = "_session_"

// inspectClaudeCodeSessionID 从 Anthropic metadata.user_id 取出 Claude Code 的会话 ID。
// Claude Code 有两种写法：旧版 "user_<hex>_account_<uuid>_session_<uuid>"，
// 2.1.78 起是 JSON 字符串 {"device_id":…,"account_uuid":…,"session_id":…}。
// 其他客户端的 user_id 多是稳定的终端用户标识，按它绑定会把同一用户的所有对话压到一个账号上，
// 所以只认这两种格式里的 UUID 会话 ID。请求体已由调用方校验为合法 JSON，这里只做按路径查找，不再完整解码。
func inspectClaudeCodeSessionID(body []byte) string {
	field := gjson.GetBytes(body, "metadata.user_id")
	if field.Type != gjson.String {
		return ""
	}
	userID := field.Str
	if strings.HasPrefix(userID, "{") {
		sessionID := gjson.Get(userID, "session_id")
		if !gjson.Valid(userID) || sessionID.Type != gjson.String {
			return ""
		}
		return canonicalSessionUUID(sessionID.Str)
	}
	index := strings.LastIndex(userID, claudeCodeLegacySessionMarker)
	if !strings.HasPrefix(userID, "user_") || index < 0 {
		return ""
	}
	return canonicalSessionUUID(userID[index+len(claudeCodeLegacySessionMarker):])
}

// canonicalSessionUUID 只接受 8-4-4-4-12 十六进制 UUID，统一为小写。
func canonicalSessionUUID(value string) string {
	if len(value) != 36 {
		return ""
	}
	for index := range len(value) {
		character := value[index]
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return ""
			}
			continue
		}
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
			return ""
		}
	}
	return strings.ToLower(value)
}
