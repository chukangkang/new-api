package relay

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestJSONSemanticallyEqual 守护 /v1/messages 的"无语义改写时转发原始字节"
// 决策逻辑（见 anthropicForwardOriginalBody）：
//   - 仅键序/空白差异 → 语义相等 → 应转发原始字节
//   - 任何实质改动（模型映射、结构体丢弃的未知字段）→ 不相等 → 回退重构字节
func TestJSONSemanticallyEqual(t *testing.T) {
	// e2e 客户端原始请求
	orig := []byte(`{"max_tokens":16,"messages":[{"content":[{"text":"hi","type":"text"}],"role":"user"}],"model":"model-TZFSPXZ3"}`)
	// 结构体 round-trip 后的字节（键序被重排）
	reordered := []byte(`{"model":"model-TZFSPXZ3","messages":[{"role":"user","content":[{"text":"hi","type":"text"}]}],"max_tokens":16}`)

	assert.True(t, jsonSemanticallyEqual(orig, reordered),
		"仅键序差异应判为语义相等，从而转发原始字节")

	// 模型映射（实质改动）必须不相等
	remapped := []byte(`{"model":"real-model","messages":[{"role":"user","content":[{"text":"hi","type":"text"}]}],"max_tokens":16}`)
	assert.False(t, jsonSemanticallyEqual(orig, remapped),
		"模型被映射时应判为不等，回退到重构字节")

	// 客户端带了结构体会丢弃的未知字段（如 seed）必须不相等
	withUnknown := []byte(`{"max_tokens":16,"seed":42,"messages":[{"content":[{"text":"hi","type":"text"}],"role":"user"}],"model":"model-TZFSPXZ3"}`)
	assert.False(t, jsonSemanticallyEqual(withUnknown, reordered),
		"客户端携带结构体未建模的字段时应判为不等，避免静默丢字段")

	// 非法 JSON 保守返回 false
	assert.False(t, jsonSemanticallyEqual([]byte(`{bad`), reordered))
}
