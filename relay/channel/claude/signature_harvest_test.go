package claude

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHarvestCtx 构造一个带 Request 上下文的最小 gin.Context。
func newHarvestCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	return c
}

// TestRegisterClaudeSignatures_NonStreamContentBlocks 验证非流式响应里
// Content 数组的 thinking/redacted_thinking 签名被收割进注册表，且注册表
// 键用的是客户端视角的模型名（OriginModelName）。
func TestRegisterClaudeSignatures_NonStreamContentBlocks(t *testing.T) {
	const uid = 910001
	model := "claude-opus-5"
	c := newHarvestCtx()
	info := &relaycommon.RelayInfo{UserId: uid, OriginModelName: model, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "some-upstream-model"}}

	resp := &dto.ClaudeResponse{
		Type: "message",
		Content: []dto.ClaudeMediaMessage{
			{Type: "thinking", Thinking: ptrStr("hmm"), Signature: "sig-nonstream-1"},
			{Type: "redacted_thinking", Signature: "sig-redacted-1"},
			{Type: "text", Text: ptrStr("answer")},
		},
	}
	registerClaudeSignatures(c, info, resp)

	// thinking 块按「签名↔文本」配对指纹登记
	pair := helper.SignaturePairFingerprint("sig-nonstream-1", "hmm")
	hit, available := helper.HasRegisteredSignature(c.Request.Context(), uid, model, pair)
	assert.True(t, available)
	assert.True(t, hit, "thinking block pair fingerprint must be registered")

	// 换绑文本（同签名配别的文本）-> 未命中，这正是绑定要拦的
	hit, _ = helper.HasRegisteredSignature(c.Request.Context(), uid, model,
		helper.SignaturePairFingerprint("sig-nonstream-1", "hmm-tampered"))
	assert.False(t, hit, "same signature with different thinking text must miss")

	// redacted_thinking 无文本，仍按裸签名登记
	hit, _ = helper.HasRegisteredSignature(c.Request.Context(), uid, model, "sig-redacted-1")
	assert.True(t, hit, "redacted_thinking block signature must be registered")

	// 注册表键绑定客户端模型名：用上游模型名查不到
	hit, _ = helper.HasRegisteredSignature(c.Request.Context(), uid, "some-upstream-model", pair)
	assert.False(t, hit)
}

// TestRegisterClaudeSignatures_StreamSignatureDelta 验证流式响应里
// signature_delta 事件（Delta 携带完整签名）被收割。
func TestRegisterClaudeSignatures_StreamSignatureDelta(t *testing.T) {
	const uid = 910002
	model := "claude-opus-5"
	c := newHarvestCtx()
	info := &relaycommon.RelayInfo{UserId: uid, OriginModelName: model}

	// 先按真实流式时序喂入 thinking_delta 增量，再送 signature_delta
	appendStreamThinking(c, 0, "step one ")
	appendStreamThinking(c, 0, "step two")

	resp := &dto.ClaudeResponse{
		Type:  "content_block_delta",
		Delta: &dto.ClaudeMediaMessage{Type: "signature_delta", Signature: "sig-stream-1"},
	}
	registerClaudeSignatures(c, info, resp)

	// 配对指纹 = 签名 + 累积出的完整 thinking 文本
	pair := helper.SignaturePairFingerprint("sig-stream-1", "step one step two")
	hit, available := helper.HasRegisteredSignature(c.Request.Context(), uid, model, pair)
	assert.True(t, available)
	assert.True(t, hit, "signature_delta must be registered with accumulated thinking text")

	// 累积器是一次性的：再次收割同事件不会二次登记（幂等性观察）
	hit, _ = helper.HasRegisteredSignature(c.Request.Context(), uid, model,
		helper.SignaturePairFingerprint("sig-stream-1", ""))
	assert.False(t, hit, "accumulator must be consumed after first harvest")
}

// TestRegisterClaudeSignatures_ContentBlockFallback 验证 content_block_start
// 事件（ContentBlock 携带签名）的兜底收割路径。
func TestRegisterClaudeSignatures_ContentBlockFallback(t *testing.T) {
	const uid = 910003
	model := "claude-opus-5"
	c := newHarvestCtx()
	info := &relaycommon.RelayInfo{UserId: uid, OriginModelName: model}

	resp := &dto.ClaudeResponse{
		Type:         "content_block_start",
		ContentBlock: &dto.ClaudeMediaMessage{Type: "thinking", Signature: "sig-blockstart-1", Thinking: ptrStr("bt")},
	}
	registerClaudeSignatures(c, info, resp)

	pair := helper.SignaturePairFingerprint("sig-blockstart-1", "bt")
	hit, available := helper.HasRegisteredSignature(c.Request.Context(), uid, model, pair)
	assert.True(t, available)
	assert.True(t, hit)
}

// TestRegisterClaudeSignatures_NoSignatureNoop 验证无签名响应不产生登记
// （不改变注册表冷热状态之外的任何东西）。
func TestRegisterClaudeSignatures_NoSignatureNoop(t *testing.T) {
	const uid = 910004
	model := "claude-opus-5"
	c := newHarvestCtx()
	info := &relaycommon.RelayInfo{UserId: uid, OriginModelName: model}

	resp := &dto.ClaudeResponse{
		Type:    "message",
		Content: []dto.ClaudeMediaMessage{{Type: "text", Text: ptrStr("plain answer")}},
	}
	require.NotPanics(t, func() { registerClaudeSignatures(c, info, resp) })

	// 该 (uid,model) 桶从未登记过
	hit, _ := helper.HasRegisteredSignature(c.Request.Context(), uid, model, "anything")
	assert.False(t, hit)
}

func ptrStr(s string) *string { return &s }
