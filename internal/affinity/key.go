package affinity

import (
	"bytes"
	"encoding/binary"

	"gpt-load/internal/protocol"
)

const keyDomain = "gpt-load/affinity/prompt-prefix-hmac/v2"

// MaxKeyLength bounds a persisted key. Hasher.Hash is a hex-encoded HMAC-SHA256
// digest in production, so every derived key is 64 bytes.
const MaxKeyLength = 64

// Key is an opaque, tenant-scoped affinity cache key.
type Key string

func (key Key) Valid() bool {
	return key != ""
}

// LogIDLength 是请求日志里会话标识的长度：亲和键十六进制摘要的前 16 位（64 bit）。
const LogIDLength = 16

// LogID 返回写进请求日志的会话标识，只用于把同一会话的请求归到一起；键过短时返回空。
func (key Key) LogID() string {
	if len(key) < LogIDLength {
		return ""
	}
	return string(key[:LogIDLength])
}

// ValidLogID 接受 LogID 的格式：16 位小写十六进制。
func ValidLogID(value string) bool {
	if len(value) != LogIDLength {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

// Hasher computes a keyed digest without exposing its key material.
type Hasher interface {
	Hash(string) string
}

// DeriveKey creates a versioned affinity key for one stable prompt prefix.
func DeriveKey(
	hasher Hasher,
	accessKeyID uint,
	clientProtocol protocol.Protocol,
	prefix []byte,
) Key {
	return deriveKey(hasher, accessKeyID, clientProtocol, keyDomain, prefix)
}

// DerivePromptCacheKey 与提示词使用不同命名空间，保持现有租户和协议隔离。
func DerivePromptCacheKey(hasher Hasher, accessKeyID uint, clientProtocol protocol.Protocol, key string) Key {
	return deriveKey(hasher, accessKeyID, clientProtocol, "gpt-load/affinity/prompt-cache-key/v1", []byte(key))
}

// DeriveClaudeCodeSessionKey 按 Claude Code 会话和模型派生亲和键。上游提示缓存按模型隔离，
// 同一会话里的辅助小模型请求（标题、探测）不能改写主模型会话的绑定，所以模型是键的一部分。
func DeriveClaudeCodeSessionKey(hasher Hasher, accessKeyID uint, clientProtocol protocol.Protocol, model string, sessionID string) Key {
	if model == "" || sessionID == "" {
		return ""
	}
	var material bytes.Buffer
	writeKeyField(&material, []byte(model))
	writeKeyField(&material, []byte(sessionID))
	return deriveKey(hasher, accessKeyID, clientProtocol, "gpt-load/affinity/claude-code-session/v1", material.Bytes())
}

func deriveKey(hasher Hasher, accessKeyID uint, clientProtocol protocol.Protocol, domain string, prefix []byte) Key {
	if hasher == nil || accessKeyID == 0 || !clientProtocol.Valid() || len(prefix) == 0 {
		return ""
	}
	var material bytes.Buffer
	writeKeyField(&material, []byte(domain))
	var encodedID [8]byte
	binary.BigEndian.PutUint64(encodedID[:], uint64(accessKeyID))
	material.Write(encodedID[:])
	writeKeyField(&material, []byte(clientProtocol))
	writeKeyField(&material, prefix)
	return Key(hasher.Hash(material.String()))
}

func writeKeyField(target *bytes.Buffer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	target.Write(size[:])
	target.Write(value)
}
