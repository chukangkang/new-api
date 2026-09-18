package helper

// 回归测试移植自 sub2api 分支 anthropic-api-style-v0.2.4
// （backend/internal/handler/gateway_anthropic_validation_test.go），
// 全部用例直接搬来做回归，保护官方对齐的校验契约。

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// validAnthropicBody 是一个能通过全部校验的最小合法请求体。
const validAnthropicBody = `{
	"model": "claude-sonnet-4-5",
	"max_tokens": 1024,
	"messages": [{"role": "user", "content": "Hello"}]
}`

func TestValidateAnthropicRequest_ValidMinimal(t *testing.T) {
	require.NoError(t, ValidateAnthropicRequest([]byte(validAnthropicBody), true, ""))
}

func TestValidateAnthropicRequest_ValidFullParams(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": "Hi"},
			{"role": "assistant", "content": [{"type": "text", "text": "Hello!"}]},
			{"role": "user", "content": "More"}
		],
		"system": "You are helpful.",
		"temperature": 0.7,
		"top_p": 0.9,
		"top_k": 40,
		"stop_sequences": ["END"],
		"stream": true,
		"thinking": {"type": "enabled", "budget_tokens": 2048},
		"tools": [{"name": "my_tool", "input_schema": {"type": "object"}}],
		"tool_choice": {"type": "auto"}
	}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_CountTokensWithoutMaxTokens(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": "Hello"}]
	}`
	// count_tokens 不要求 max_tokens
	require.NoError(t, ValidateAnthropicRequest([]byte(body), false, ""))
	// /v1/messages 要求 max_tokens
	require.Error(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

// ── model 校验 ──

func TestValidateAnthropicRequest_ModelMissing(t *testing.T) {
	body := `{"max_tokens": 100, "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"model" is a required property`)
}

func TestValidateAnthropicRequest_ModelNotString(t *testing.T) {
	body := `{"model": 123, "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"model" is a required property`)
}

func TestValidateAnthropicRequest_ModelEmptyString(t *testing.T) {
	body := `{"model": "", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"model" is a required property`)
}

// ── max_tokens 校验 ──

func TestValidateAnthropicRequest_MaxTokensMissing(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"max_tokens" is a required property`)
}

func TestValidateAnthropicRequest_MaxTokensZero(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 0, "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"max_tokens" must be greater than or equal to 1`)
}

func TestValidateAnthropicRequest_MaxTokensNegative(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": -5, "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"max_tokens" must be greater than or equal to 1`)
}

func TestValidateAnthropicRequest_MaxTokensNotNumber(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": "abc", "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"max_tokens" must be an integer`)
}

// ── messages 校验 ──

func TestValidateAnthropicRequest_MessagesMissing(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"messages" is a required property`)
}

func TestValidateAnthropicRequest_MessagesNotArray(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": "hello"}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"messages" must be an array`)
}

func TestValidateAnthropicRequest_MessagesEmptyArray(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": []}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"messages" must be a non-empty array`)
}

func TestValidateAnthropicRequest_MessageRoleMissing(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"messages[0].role" is a required property`)
}

func TestValidateAnthropicRequest_MessageRoleInvalid(t *testing.T) {
	// role=system 真实 API 接受（200），故用一个真正非法的角色作样例。
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "bogus", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"messages[0].role" must be one of: "user", "assistant"`)
}

func TestValidateAnthropicRequest_MessageRoleSystemLeadingRejected(t *testing.T) {
	// 真伪验证实测（midconv_system_leading）：messages[0] 的 role=system 属非法位置 → 400。
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "system", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"messages[0].role" must be one of: "user", "assistant"`)
}

func TestValidateAnthropicRequest_MessageRoleSystemMidConversationAccepted(t *testing.T) {
	// 真伪验证实测（midconv_system）：后续位置的 role=system 接受 → 200。
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [
		{"role": "user", "content": "hi"},
		{"role": "system", "content": "note"},
		{"role": "user", "content": "more"}
	]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_MessageContentMissing(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"messages[0].content" is a required property`)
}

func TestValidateAnthropicRequest_SecondMessageRoleInvalid(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [
		{"role": "user", "content": "hi"},
		{"role": "tool", "content": "result"}
	]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"messages[1].role" must be one of: "user", "assistant"`)
}

// ── temperature 校验 ──

func TestValidateAnthropicRequest_TemperatureOutOfRange_High(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": 1.5}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"temperature" must be between 0.0 and 1.0`)
}

func TestValidateAnthropicRequest_TemperatureOutOfRange_Low(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": -0.1}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"temperature" must be between 0.0 and 1.0`)
}

func TestValidateAnthropicRequest_TemperatureBoundary(t *testing.T) {
	// 0.0 和 1.0 都是合法边界值
	body0 := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": 0.0}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body0), true, ""))

	body1 := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": 1.0}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body1), true, ""))
}

func TestValidateAnthropicRequest_TemperatureNotNumber(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": "hot"}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"temperature" must be a number`)
}

// ── top_p 校验 ──

func TestValidateAnthropicRequest_TopPOutOfRange(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "top_p": 1.5}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"top_p" must be between 0.0 and 1.0`)
}

func TestValidateAnthropicRequest_TopPValid(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "top_p": 0.5}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

// ── top_k 校验 ──

func TestValidateAnthropicRequest_TopKZero(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "top_k": 0}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"top_k" must be greater than or equal to 1`)
}

func TestValidateAnthropicRequest_TopKValid(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "top_k": 40}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

// ── 采样参数：Opus 4.6 之后发布的模型限制 ──
// 官方规则（platform.claude.com/docs/en/api/messages）：
//   temperature 仅接受 1.0；top_p 仅接受 >= 0.99；top_k 一律拒绝。

var samplingParamModels = []struct {
	name string
	id   string
}{
	{"opus-4-7", "claude-opus-4-7"},
	{"opus-4-8", "claude-opus-4-8"},
	{"opus-5", "claude-opus-5"},
	{"sonnet-5", "claude-sonnet-5"},
	{"fable-5", "claude-fable-5"},
	{"fable-5-1", "claude-fable-5-1"},
	{"mythos-5", "claude-mythos-5"},
	{"mythos-5-1", "claude-mythos-5-1"},
	{"mythos-preview", "claude-mythos-preview"},
}

func TestValidateAnthropicRequest_SamplingTemperatureNonDefaultRejected(t *testing.T) {
	for _, m := range samplingParamModels {
		t.Run(m.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": 0.7}`, m.id)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `"temperature" must be 1.0 for this model`, err.Error())
		})
	}
}

func TestValidateAnthropicRequest_SamplingTemperatureDefaultAccepted(t *testing.T) {
	for _, m := range samplingParamModels {
		t.Run(m.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": 1.0}`, m.id)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
	}
}

func TestValidateAnthropicRequest_SamplingTopPLowRejected(t *testing.T) {
	for _, m := range samplingParamModels {
		t.Run(m.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "top_p": 0.7}`, m.id)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `"top_p" must be >= 0.99 for this model`, err.Error())
		})
	}
}

func TestValidateAnthropicRequest_SamplingTopPNearDefaultAccepted(t *testing.T) {
	for _, m := range samplingParamModels {
		t.Run(m.name+"/0.99", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "top_p": 0.99}`, m.id)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
		t.Run(m.name+"/1.0", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "top_p": 1.0}`, m.id)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
	}
}

func TestValidateAnthropicRequest_SamplingTopKAlwaysRejected(t *testing.T) {
	for _, m := range samplingParamModels {
		t.Run(m.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "top_k": 5}`, m.id)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `"top_k" is not supported for this model`, err.Error())
		})
	}
}

func TestValidateAnthropicRequest_SamplingOldModelsUnaffected(t *testing.T) {
	// Opus 4.6 / Sonnet 4.6 是最后支持采样参数的模型
	for _, id := range []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-sonnet-4-5", "claude-haiku-4-5", "claude-opus-4-5"} {
		t.Run(id, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": 0.7, "top_p": 0.5, "top_k": 40}`, id)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
	}
}

func TestValidateAnthropicRequest_SamplingDateSuffixNormalized(t *testing.T) {
	// 带日期后缀的模型名应归一化后同样受限
	body := `{"model": "claude-opus-4-7-20260416", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "temperature": 0.5}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Equal(t, `"temperature" must be 1.0 for this model`, err.Error())
}

// ── thinking 校验 ──

func TestValidateAnthropicRequest_ThinkingEnabledRequiresBudget(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled"}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"thinking.budget_tokens" is a required property`)
}

func TestValidateAnthropicRequest_ThinkingBudgetTooSmall(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 512}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"thinking.budget_tokens" must be greater than or equal to 1024`)
}

func TestValidateAnthropicRequest_ThinkingDisabledNoBudgetNeeded(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_ThinkingBudgetMustBeBelowMaxTokens(t *testing.T) {
	// 官方：budget_tokens 必须严格小于 max_tokens（thinking tokens 计入 max_tokens）。
	// 实测官方原文（claude-opus-4-5-20251101，budget 8192 > max 4096）：
	//   `max_tokens` must be greater than `thinking.budget_tokens`.
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 8192}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Equal(t, "`max_tokens` must be greater than `thinking.budget_tokens`.", err.Error())

	// 相等也被拒（官方措辞是 "must be greater than"，不含等于）
	bodyEq := `{"model": "claude-sonnet-4-5", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 4096}}`
	errEq := ValidateAnthropicRequest([]byte(bodyEq), true, "")
	require.Error(t, errEq)
	require.Equal(t, "`max_tokens` must be greater than `thinking.budget_tokens`.", errEq.Error())

	// 合法：budget < max_tokens
	bodyOk := `{"model": "claude-sonnet-4-5", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 2048}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(bodyOk), true, ""))

	// 例外：interleaved thinking（携带 interleaved-thinking-2025-05-14 beta 头）
	// 时 budget 横跨同一 assistant turn 的所有 thinking 块，允许超过 max_tokens
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, "interleaved-thinking-2025-05-14"))
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, "claude-code-20250219, interleaved-thinking-2025-05-14"))

	// count_tokens 不带 max_tokens：无从比较，放行
	bodyCT := `{"model": "claude-sonnet-4-5", "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 8192}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(bodyCT), false, ""))
}

func TestValidateAnthropicRequest_ThinkingInvalidType(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "on"}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"thinking.type" must be one of:`)
}

func TestValidateAnthropicRequest_ThinkingAdaptiveNoBudgetNeeded(t *testing.T) {
	// claude-opus-4-8 接受 adaptive（Sonnet 4.5 反而拒绝 adaptive）
	body := `{"model": "claude-opus-4-8", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive"}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

// ── thinking.display 校验（官方 schema：summarized | omitted | updates）──

func TestValidateAnthropicRequest_ThinkingDisplayValidValues(t *testing.T) {
	for _, display := range []string{"summarized", "omitted"} {
		t.Run(display, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "claude-fable-5-1", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive", "display": "%s"}}`, display)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
	}
	// enabled 模式同样接受 display（官方 ThinkingConfigEnabled 也有 display 字段）
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 2048, "display": "omitted"}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_ThinkingDisplayBogusRejected(t *testing.T) {
	body := `{"model": "claude-fable-5-1", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive", "display": "__bogus__"}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"thinking.display" must be one of: "summarized", "omitted", "updates"`)
}

func TestValidateAnthropicRequest_ThinkingDisplayUpdatesRequiresBetaHeader(t *testing.T) {
	body := `{"model": "claude-fable-5-1", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive", "display": "updates"}}`
	// 无 beta header → 400（官方明文：requires thinking-display-updates-2026-08-18）
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "thinking-display-updates-2026-08-18")
	// 有 beta header → 放行
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, "thinking-display-updates-2026-08-18"))
	// beta header 列表形式也应识别
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, "some-other-beta, thinking-display-updates-2026-08-18"))
}

func TestValidateAnthropicRequest_ThinkingDisplayWithDisabledRejected(t *testing.T) {
	// 官方：display is invalid with type=disabled
	body := `{"model": "claude-opus-4-8", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled", "display": "summarized"}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"thinking.display" is not supported when "thinking.type" is "disabled"`)
}

func TestValidateAnthropicRequest_ThinkingDisplayNotStringRejected(t *testing.T) {
	body := `{"model": "claude-fable-5-1", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive", "display": 42}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"thinking.display" must be a string`)
}

// ── 按模型区分的 thinking.type 校验 ──

func TestValidateAnthropicRequest_Thinking_FableMythos5Series(t *testing.T) {
	// Fable 5.1 / Mythos 5.1 / Fable 5 / Mythos 5: 仅 adaptive。
	// 官方文档（platform.claude.com/docs/en/api/errors "Thinking cannot be
	// disabled"）：这四个模型 thinking 常开，disabled 返回 400。
	// 早期参考实现按实测放宽（实测 200），2026-09-18 按官方文档收紧。
	models := []string{
		"claude-fable-5-1", "claude-mythos-5-1",
		"claude-fable-5", "claude-mythos-5",
	}
	for _, model := range models {
		t.Run(model+"_adaptive_ok", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive"}}`, model)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
		t.Run(model+"_disabled_rejected", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}}`, model)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `"thinking.type.disabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`, err.Error())
		})
		t.Run(model+"_enabled_rejected", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 2048}}`, model)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `"thinking.type.enabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`, err.Error())
		})
	}
}

func TestValidateAnthropicRequest_Thinking_Opus5Disabled(t *testing.T) {
	// Opus 5: 官方明文接受 disabled（thinking 默认开启，可关闭）
	body := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))

	// enabled 仍被拒绝
	bodyEnabled := `{"model": "claude-opus-5", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 2048}}`
	err := ValidateAnthropicRequest([]byte(bodyEnabled), true, "")
	require.Error(t, err)
	require.Equal(t, `"thinking.type.enabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`, err.Error())
}

func TestValidateAnthropicRequest_DisabledWithHighEffortRejected(t *testing.T) {
	// 官方：Opus 5 起 xhigh/max effort 下无法关闭 thinking
	for _, effort := range []string{"xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "claude-opus-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}, "output_config": {"effort": "%s"}}`, effort)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Contains(t, err.Error(), `"thinking.type" value "disabled" is not supported with "output_config.effort" value "`+effort)
		})
	}
	// 低档 effort 允许 disabled
	for _, effort := range []string{"low", "medium", "high"} {
		t.Run("ok_"+effort, func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "claude-opus-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}, "output_config": {"effort": "%s"}}`, effort)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
	}
	// 不带 effort 的 disabled 放行
	body := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_DisabledWithHighEffort_Opus48Accepted(t *testing.T) {
	// 真伪验证实测：Opus 4.8 的 disabled + xhigh 返回 200（R34 仅限 Opus 5+）。
	for _, effort := range []string{"xhigh", "max"} {
		body := fmt.Sprintf(`{"model": "claude-opus-4-8", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}, "output_config": {"effort": "%s"}}`, effort)
		require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""), "opus-4-8 disabled+%s should be accepted", effort)
	}
}

func TestValidateAnthropicRequest_Thinking_Opus48_Sonnet5(t *testing.T) {
	// Opus 4.8 / 4.7 / Sonnet 5: adaptive + disabled（仅 enabled 被拒绝）
	models := []string{"claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-5"}
	for _, model := range models {
		t.Run(model+"_adaptive_ok", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive"}}`, model)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
		t.Run(model+"_disabled_ok", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}}`, model)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
		t.Run(model+"_enabled_rejected", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 2048}}`, model)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `"thinking.type.enabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`, err.Error())
		})
	}
}

func TestValidateAnthropicRequest_Thinking_MythosPreview(t *testing.T) {
	// Mythos Preview: adaptive + enabled（仅 disabled 被拒绝）
	model := "claude-mythos-preview"
	bodyAdaptive := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive"}}`, model)
	require.NoError(t, ValidateAnthropicRequest([]byte(bodyAdaptive), true, ""))

	bodyEnabled := fmt.Sprintf(`{"model": "%s", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 2048}}`, model)
	require.NoError(t, ValidateAnthropicRequest([]byte(bodyEnabled), true, ""))

	bodyDisabled := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}}`, model)
	err := ValidateAnthropicRequest([]byte(bodyDisabled), true, "")
	require.Error(t, err)
	// Mythos Preview 有专属文案（它是这些模型中唯一接受 extended thinking 的）
	require.Equal(t, `"thinking.type.disabled" is not supported for this model. Thinking defaults to adaptive mode when not specified; use "thinking.type.enabled" with "budget_tokens" for extended thinking.`, err.Error())
}

func TestValidateAnthropicRequest_Thinking_ExtendedOnlyModels(t *testing.T) {
	// Opus 4.5 / Haiku 4.5 / Sonnet 4.5: enabled + disabled（adaptive 被拒绝）
	models := []string{"claude-opus-4-5", "claude-haiku-4-5", "claude-sonnet-4-5"}
	for _, model := range models {
		t.Run(model+"_enabled_ok", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 2048}}`, model)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
		t.Run(model+"_disabled_ok", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "disabled"}}`, model)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
		t.Run(model+"_adaptive_rejected", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive"}}`, model)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `adaptive thinking is not supported on this model`, err.Error())
		})
	}
}

func TestValidateAnthropicRequest_Thinking_UnrestrictedModels(t *testing.T) {
	// Opus 4.6 / Sonnet 4.6: 已弃用，不限制
	models := []string{"claude-opus-4-6", "claude-sonnet-4-6"}
	for _, model := range models {
		for _, tt := range []string{"enabled", "disabled", "adaptive"} {
			t.Run(fmt.Sprintf("%s_%s_ok", model, tt), func(t *testing.T) {
				bt := ""
				if tt == "enabled" {
					bt = `, "budget_tokens": 2048`
				}
				body := fmt.Sprintf(`{"model": "%s", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "%s"%s}}`, model, tt, bt)
				require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
			})
		}
	}
}

func TestValidateAnthropicRequest_Thinking_DateSuffixNormalization(t *testing.T) {
	// 带日期后缀的模型应能正确归一化
	body := `{"model": "claude-sonnet-4-5-20250929", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "adaptive"}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err) // sonnet-4-5 不接受 adaptive

	body2 := `{"model": "claude-sonnet-4-5-20250929", "max_tokens": 4096, "messages": [{"role": "user", "content": "hi"}], "thinking": {"type": "enabled", "budget_tokens": 2048}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body2), true, ""))
}

func TestNormalizeThinkingModelFamily(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
		{"claude-opus-4-5-20251101", "claude-opus-4-5"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5"},
		{"claude-opus-4-6-thinking", "claude-opus-4-6"},
		{"claude-fable-5-1", "claude-fable-5-1"},
		{"claude-opus-5", "claude-opus-5"},
		{"unknown-model", "unknown-model"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			require.Equal(t, tc.want, normalizeThinkingModelFamily(tc.input))
		})
	}
}

// ── 组合场景 ──

func TestValidateAnthropicRequest_ContentAsStringOrArray(t *testing.T) {
	// content 可以是字符串
	bodyStr := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "plain text"}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(bodyStr), true, ""))

	// content 也可以是数组
	bodyArr := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": [{"type": "text", "text": "block"}]}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(bodyArr), true, ""))
}

func TestValidateAnthropicRequest_SystemAsStringOrArray(t *testing.T) {
	bodyStr := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "system": "You are helpful.", "messages": [{"role": "user", "content": "hi"}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(bodyStr), true, ""))

	bodyArr := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "system": [{"type": "text", "text": "You are helpful."}], "messages": [{"role": "user", "content": "hi"}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(bodyArr), true, ""))
}

// ── prefill 校验（4.6+ / Mythos Preview 必须以 user 消息结尾）──

func TestValidateAnthropicRequest_PrefillRejected(t *testing.T) {
	// 4.6+ 模型与 Mythos Preview 拒绝 assistant 结尾
	rejectModels := []string{
		"claude-opus-4-6", "claude-sonnet-4-6",
		"claude-opus-4-7", "claude-opus-4-8", "claude-opus-5",
		"claude-sonnet-5",
		"claude-fable-5", "claude-fable-5-1",
		"claude-mythos-5", "claude-mythos-5-1",
		"claude-mythos-preview",
	}
	for _, model := range rejectModels {
		t.Run(model+"_assistant_end_rejected", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "hello"}]}`, model)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `This model does not support assistant message prefill. The conversation must end with a user message.`, err.Error())
		})
		t.Run(model+"_user_end_ok", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "assistant", "content": "hello"}, {"role": "user", "content": "hi"}]}`, model)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
	}
}

func TestValidateAnthropicRequest_PrefillAllowedOnOlderModels(t *testing.T) {
	// 4.5 系列不受 prefill 限制
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "hello"}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_Prefill_ToolUseEndingNotPrefill(t *testing.T) {
	// 工具调用流程：assistant 消息仅含 tool_use 块（无文本），不应被视为 prefill
	body := `{"model": "claude-opus-4-6", "max_tokens": 100, "messages": [
		{"role": "user", "content": "weather?"},
		{"role": "assistant", "content": [{"type": "tool_use", "id": "tu_1", "name": "get_weather", "input": {}}]}
	]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))

	// assistant 消息同时含文本 + tool_use → 仍是 prefill，应拒绝
	bodyMixed := `{"model": "claude-opus-4-6", "max_tokens": 100, "messages": [
		{"role": "user", "content": "weather?"},
		{"role": "assistant", "content": [{"type": "text", "text": "Let me check"}, {"type": "tool_use", "id": "tu_1", "name": "get_weather", "input": {}}]}
	]}`
	err := ValidateAnthropicRequest([]byte(bodyMixed), true, "")
	require.Error(t, err)
	require.Equal(t, `This model does not support assistant message prefill. The conversation must end with a user message.`, err.Error())
}

// ── tool_choice 强制工具调用校验（Fable 5.1 / Mythos 5.1）──

func TestValidateAnthropicRequest_ForcedToolChoiceRejected(t *testing.T) {
	rejectModels := []string{"claude-fable-5-1", "claude-mythos-5-1"}
	for _, model := range rejectModels {
		t.Run(model+"_tool_rejected", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "tool_choice": {"type": "tool", "name": "get_weather"}}`, model)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `tool_choice: type "tool" and "any" are not supported for this model.`, err.Error())
		})
		t.Run(model+"_any_rejected", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "tool_choice": {"type": "any"}}`, model)
			err := ValidateAnthropicRequest([]byte(body), true, "")
			require.Error(t, err)
			require.Equal(t, `tool_choice: type "tool" and "any" are not supported for this model.`, err.Error())
		})
		t.Run(model+"_auto_ok", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "tool_choice": {"type": "auto"}}`, model)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
		t.Run(model+"_none_ok", func(t *testing.T) {
			body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "tool_choice": {"type": "none"}}`, model)
			require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
		})
	}
}

func TestValidateAnthropicRequest_ForcedToolChoiceAllowedElsewhere(t *testing.T) {
	// 其他模型允许 tool/any
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "tool_choice": {"type": "any"}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

// ── max_tokens 上限校验 ──

func TestValidateAnthropicRequest_MaxTokensAboveCapRejected(t *testing.T) {
	// 128K 上限模型：官方 "128K" 是十进制 128000（实测 128001 官方 400），cap+1 应 400。
	// 文案与官方 API 逐字一致（pydantic 风格）。
	body := `{"model": "claude-opus-5", "max_tokens": 128001, "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Equal(t, `'max_tokens': 128001 > 128000 - 'max_tokens' should be smaller than or equal to 128000`, err.Error())
}

func TestValidateAnthropicRequest_MaxTokensAtCapAccepted(t *testing.T) {
	// 恰好等于上限应放行
	body := `{"model": "claude-opus-5", "max_tokens": 128000, "messages": [{"role": "user", "content": "hi"}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_MaxTokensHaikuCap(t *testing.T) {
	// Haiku 4.5 上限 64K（十进制 64000），文案与官方逐字一致
	body := `{"model": "claude-haiku-4-5", "max_tokens": 64001, "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Equal(t, `'max_tokens': 64001 > 64000 - 'max_tokens' should be smaller than or equal to 64000`, err.Error())
}

func TestValidateAnthropicRequest_MaxTokensUnknownModelUntouched(t *testing.T) {
	// 未知模型不设上限，超大值也放行（交给上游）
	body := `{"model": "some-unknown-model", "max_tokens": 999999, "messages": [{"role": "user", "content": "hi"}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_MaxTokensCapPerModel(t *testing.T) {
	// 官方各模型页核实的完整上限表（2026-09-17）：
	// 128K：fable-5-1 / fable-5 / mythos-5-1 / mythos-5 / opus-5 / sonnet-5 /
	//       opus-4-8 / opus-4-7 / opus-4-6 / sonnet-4-6
	// 64K ：haiku-4-5 / opus-4-5 / sonnet-4-5
	// mythos-preview 无公开规格页，保持 fail-open。
	cases := []struct {
		model string
		cap   int
	}{
		{"claude-fable-5-1", 128000},
		{"claude-fable-5", 128000},
		{"claude-mythos-5-1", 128000},
		{"claude-mythos-5", 128000},
		{"claude-opus-5", 128000},
		{"claude-sonnet-5", 128000},
		{"claude-opus-4-8", 128000},
		{"claude-opus-4-7", 128000},
		{"claude-opus-4-6", 128000},
		{"claude-sonnet-4-6", 128000},
		{"claude-haiku-4-5", 64000},
		{"claude-opus-4-5", 64000},
		{"claude-sonnet-4-5", 64000},
	}
	for _, tc := range cases {
		t.Run(tc.model+"_"+fmt.Sprint(tc.cap), func(t *testing.T) {
			above := fmt.Sprintf(`{"model": %q, "max_tokens": %d, "messages": [{"role": "user", "content": "hi"}]}`, tc.model, tc.cap+1)
			err := ValidateAnthropicRequest([]byte(above), true, "")
			require.Error(t, err)
			require.Equal(t,
				fmt.Sprintf(`'max_tokens': %d > %d - 'max_tokens' should be smaller than or equal to %d`, tc.cap+1, tc.cap, tc.cap),
				err.Error())

			atCap := fmt.Sprintf(`{"model": %q, "max_tokens": %d, "messages": [{"role": "user", "content": "hi"}]}`, tc.model, tc.cap)
			require.NoError(t, ValidateAnthropicRequest([]byte(atCap), true, ""), "cap 本身应放行")
		})
	}

	// 带日期后缀的快照 ID 也应命中同一上限
	dated := `{"model": "claude-sonnet-4-5-20250929", "max_tokens": 64001, "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(dated), true, "")
	require.Error(t, err)
	require.Equal(t, `'max_tokens': 64001 > 64000 - 'max_tokens' should be smaller than or equal to 64000`, err.Error())

	// mythos-preview：无公开规格，fail-open
	unspecified := `{"model": "claude-mythos-preview", "max_tokens": 999999, "messages": [{"role": "user", "content": "hi"}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(unspecified), true, ""))
}

// ── output_config.effort 校验 ──

func TestValidateAnthropicRequest_EffortInvalidValueRejected(t *testing.T) {
	body := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "output_config": {"effort": "ultra"}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"output_config.effort" must be one of`)
}

func TestValidateAnthropicRequest_EffortValidValuesAccepted(t *testing.T) {
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		body := fmt.Sprintf(`{"model": "claude-opus-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "output_config": {"effort": "%s"}}`, e)
		require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""), "effort %s should be accepted on opus-5", e)
	}
}

func TestValidateAnthropicRequest_EffortXHighRejectedOnOpus46(t *testing.T) {
	// opus-4-6 支持 low/medium/high/max，但不支持 xhigh
	body := `{"model": "claude-opus-4-6", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "output_config": {"effort": "xhigh"}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `value "xhigh" is not supported for this model`)
}

func TestValidateAnthropicRequest_EffortMaxAcceptedOnOpus46(t *testing.T) {
	// opus-4-6 支持 max
	body := `{"model": "claude-opus-4-6", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "output_config": {"effort": "max"}}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

func TestValidateAnthropicRequest_EffortNotStringRejected(t *testing.T) {
	body := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "output_config": {"effort": 3}}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"output_config.effort" must be a string`)
}

// ── speed=fast 校验 ──

func TestValidateAnthropicRequest_SpeedFastSupportedModelsAccepted(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "claude-opus-4-8", "claude-opus-4.8"} {
		body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "speed": "fast"}`, model)
		require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""), "model %s should accept speed=fast", model)
	}
}

func TestValidateAnthropicRequest_SpeedFastUnsupportedModelsRejected(t *testing.T) {
	for _, model := range []string{
		"claude-opus-4-7", // fast mode 已移除
		"claude-opus-4-6",
		"claude-opus-4-5", // 不能被 "opus-5" 规则误判
		"claude-sonnet-5",
		"claude-haiku-4-5",
	} {
		body := fmt.Sprintf(`{"model": "%s", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "speed": "fast"}`, model)
		err := ValidateAnthropicRequest([]byte(body), true, "")
		require.Error(t, err, "model %s should reject speed=fast", model)
		require.Equal(t, `"speed" value "fast" is not supported for this model`, err.Error())
	}
}

func TestValidateAnthropicRequest_SpeedStandardAlwaysAccepted(t *testing.T) {
	body := `{"model": "claude-sonnet-4-5", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}], "speed": "standard"}`
	require.NoError(t, ValidateAnthropicRequest([]byte(body), true, ""))
}

// ── thinking 块签名结构校验 ──

// longValidSig 是一个合法 base64、解码后远超最小长度的签名（模拟真实签名）。
const longValidSig = "CAISuyMKpgEIERgCKkCozyv1jDNFSU1VkqYoVveGjyGIeEuG7iAuUN2RtIb6MXhssIMBOlwsr+v0knDmsgp7nWdfdTC7"

func TestValidateThinkingSignatures_ValidSignatureAccepted(t *testing.T) {
	body := fmt.Sprintf(`{"model": "claude-opus-5", "max_tokens": 100, "messages": [
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "hmm", "signature": "%s"}]},
		{"role": "user", "content": "hi"}
	]}`, longValidSig)
	require.NoError(t, ValidateThinkingSignatures([]byte(body)))
}

func TestValidateThinkingSignatures_NonBase64Rejected(t *testing.T) {
	body := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "hmm", "signature": "!!!not-base64$$$"}]},
		{"role": "user", "content": "hi"}
	]}`
	err := ValidateThinkingSignatures([]byte(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "messages.0.content.0")
	require.Contains(t, err.Error(), "not valid base64")
}

func TestValidateThinkingSignatures_TooShortRejected(t *testing.T) {
	// "AAAA" 是合法 base64，但解码后仅 3 字节 < 32
	body := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "hmm", "signature": "AAAA"}]},
		{"role": "user", "content": "hi"}
	]}`
	err := ValidateThinkingSignatures([]byte(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "signature is too short")
}

func TestValidateThinkingSignatures_EmptyOrMissingSignatureSkipped(t *testing.T) {
	// 空签名 / 缺签名不归签名校验管（交给既有的预过滤 + 400 整流链路）
	bodyEmpty := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "hmm", "signature": ""}]},
		{"role": "user", "content": "hi"}
	]}`
	require.NoError(t, ValidateThinkingSignatures([]byte(bodyEmpty)))

	bodyMissing := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "hmm"}]},
		{"role": "user", "content": "hi"}
	]}`
	require.NoError(t, ValidateThinkingSignatures([]byte(bodyMissing)))
}

func TestValidateThinkingSignatures_RedactedThinkingCovered(t *testing.T) {
	body := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [
		{"role": "assistant", "content": [{"type": "redacted_thinking", "data": "xxx", "signature": "!!!bad!!!"}]},
		{"role": "user", "content": "hi"}
	]}`
	err := ValidateThinkingSignatures([]byte(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not valid base64")
}

func TestValidateThinkingSignatures_UserMessagesIgnored(t *testing.T) {
	body := `{"model": "claude-opus-5", "max_tokens": 100, "messages": [
		{"role": "user", "content": [{"type": "thinking", "thinking": "hmm", "signature": "!!!bad!!!"}]},
		{"role": "user", "content": "hi"}
	]}`
	require.NoError(t, ValidateThinkingSignatures([]byte(body)))
}

func TestValidateThinkingSignatures_NoMessagesField(t *testing.T) {
	require.NoError(t, ValidateThinkingSignatures([]byte(`{"model": "claude-opus-5"}`)))
}

// ── Claude 家族门控 ──

func TestIsClaudeFamilyModel(t *testing.T) {
	for _, m := range []string{
		"claude-sonnet-4-5", "claude-opus-5", "claude-haiku-4-5",
		"opus-5", "sonnet-5", "haiku-4-5",
		"Claude-Opus-5", "  claude-sonnet-4-5  ",
	} {
		require.True(t, isClaudeFamilyModel(m), "model %s should be claude family", m)
	}
	for _, m := range []string{"gpt-4o", "deepseek-chat", "qwen-max", "grok-3", ""} {
		require.False(t, isClaudeFamilyModel(m), "model %s should not be claude family", m)
	}
}

// ── 非 Claude 家族模型也必须经过基础校验（规范 §4 无条件挂载）──

func TestValidateAnthropicRequest_NonClaudeFamilyStillValidated(t *testing.T) {
	// 合成模型名（e2e 场景）：缺少 max_tokens 必须报 R2 必填错误，
	// 而不是因为非 claude 前缀被整体跳过。
	body := `{"model": "model-TZFSPXZ3", "messages": [{"role": "user", "content": "hi"}]}`
	err := ValidateAnthropicRequest([]byte(body), true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"max_tokens" is a required property`)

	// 补齐 max_tokens 后应通过（模型感知类规则对未知家族 fail-open）。
	okBody := `{"model": "model-TZFSPXZ3", "max_tokens": 16, "messages": [{"role": "user", "content": "hi"}]}`
	require.NoError(t, ValidateAnthropicRequest([]byte(okBody), true, ""))
}
