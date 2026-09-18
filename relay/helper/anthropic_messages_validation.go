package helper

// 本文件实现 Anthropic /v1/messages 请求体的参数校验，
// 对齐官方 Anthropic Messages API 的校验规则与错误消息格式。
// 校验失败时返回与官方 API 逐字一致的错误消息，由调用方以
// 400 + invalid_request_error 响应给客户端。
//
// 移植自 sub2api 分支 anthropic-api-style-v0.2.4
// （backend/internal/handler/gateway_anthropic_validation.go），
// 规格书见 docs/ANTHROPIC_MESSAGES_VALIDATION_SPEC.md。
//
// 设计原则：
//   - 错误文案必须逐字一致（客户端/上游可能按文案匹配）；
//   - 未知模型一律放行（thinking.type、max_tokens 上限、fast mode 之外），
//     宁可漏拦不可误杀；
//   - 纯结构校验，不做密码学验签（网关没有 Anthropic 私钥）。

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ErrAnthropicValidation 是 /v1/messages 官方校验失败的哨兵错误。
// 用它标记"错误消息已是官方逐字文案"，调用方据此跳过
// MessageWithRequestId 的后缀追加（request id 已通过响应体顶层
// request_id 字段与 request-id 响应头传达，官方文案不带该后缀）。
var ErrAnthropicValidation = errors.New("anthropic messages validation failed")

// WrapAnthropicValidationError 把校验错误包装为带哨兵标记的错误。
func WrapAnthropicValidationError(err error) error {
	return fmt.Errorf("%w: %s", ErrAnthropicValidation, err.Error())
}

// IsAnthropicValidationError 判断错误是否为 /v1/messages 官方校验失败。
func IsAnthropicValidationError(err error) bool {
	return errors.Is(err, ErrAnthropicValidation)
}

// AnthropicValidationErrorMessage 从校验错误中提取官方逐字文案
// （剥离哨兵前缀）。非校验错误返回其原始 Error() 文本。
func AnthropicValidationErrorMessage(err error) string {
	msg := err.Error()
	prefix := ErrAnthropicValidation.Error() + ": "
	if strings.HasPrefix(msg, prefix) {
		return strings.TrimPrefix(msg, prefix)
	}
	return msg
}

// ValidateClaudeMessagesRequest 是 /v1/messages 入口的统一校验钩子
// （原生 Anthropic 组与 OpenAI 桥接组共用同一入口，此处一次挂载两条路径）。
// 对 /v1/messages 入口的所有请求执行官方校验（规范 §4：两条入口无条件挂载）。
// 基础字段校验（model/max_tokens/messages/role）与采样参数校验对所有模型生效；
// 模型感知类规则（thinking.type 矩阵、max_tokens 上限、fast mode）在校验器内部
// 对未知家族 fail-open（规范 §7.4），因此非 Claude 家族模型不会被误杀。
// 返回 nil 表示校验通过。
func ValidateClaudeMessagesRequest(c *gin.Context) error {
	storage, err := common.GetRequestBody(c)
	if err != nil {
		return err
	}
	// GetRequestBody 声明返回 io.Seeker，实际类型为 common.BodyStorage
	// （io.ReadSeeker）。这里做类型断言取得 Reader 视图读取全文；
	// 读完 Seek 回 0，保证后续 UnmarshalBodyReusable 可重新消费请求体。
	reader, ok := storage.(io.Reader)
	if !ok {
		return fmt.Errorf("unexpected body storage type %T", storage)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if _, seekErr := storage.Seek(0, io.SeekStart); seekErr != nil {
		return seekErr
	}
	// count_tokens 端点豁免 max_tokens 必填（规范 §7.5）。
	isCountTokens := strings.HasSuffix(c.Request.URL.Path, "/count_tokens")
	requireMaxTokens := !isCountTokens
	if verr := ValidateAnthropicRequest(body, requireMaxTokens, c.GetHeader("anthropic-beta")); verr != nil {
		return WrapAnthropicValidationError(verr)
	}
	// thinking 块签名结构校验（可配置，默认开）：上游多数链路不验签，
	// 网关自守门，客户端传了格式坏的签名（非 base64/过短）直接 400。
	// 仅 /v1/messages 生效；count_tokens 端点不做此项。
	if !isCountTokens && setting.ShouldValidateThinkingSignatures() {
		if serr := ValidateThinkingSignatures(body); serr != nil {
			return WrapAnthropicValidationError(serr)
		}
	}
	return nil
}

// BetaThinkingDisplayUpdates 是 thinking.display="updates" 专用的 beta token。
// 官方明文：缺此 header 时 display=updates 与未知 display 值一样返回 400。
const BetaThinkingDisplayUpdates = "thinking-display-updates-2026-08-18"

// BetaInterleavedThinking 是 interleaved thinking 的 beta token。
// 携带该 beta 时 thinking.budget_tokens 允许大于等于 max_tokens。
const BetaInterleavedThinking = "interleaved-thinking-2025-05-14"

// thinkingSignatureMinDecodedLen 是 thinking 块签名解码后的最小字节数阈值。
// 真实的 Anthropic thinking 签名是一段较长的不透明 base64（解码后数百~数千字节）；
// 明显偏短的签名通常是截断/损坏。取 32 字节作为下限：既能拦住明显的坏签名，
// 又不会误伤合法的较短签名。
const thinkingSignatureMinDecodedLen = 32

// isClaudeFamilyModel 判断请求的模型是否属于 Claude 家族
// （claude-* / opus-* / sonnet-* / haiku-*）。
// 非 Claude 模型（grok / deepseek / qwen 等）保持既有透传行为，避免误伤。
func isClaudeFamilyModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "claude") ||
		strings.HasPrefix(m, "opus") ||
		strings.HasPrefix(m, "sonnet") ||
		strings.HasPrefix(m, "haiku")
}

// ValidateAnthropicRequest 校验 Anthropic Messages API 请求体。
// requireMaxTokens 控制是否要求 max_tokens 字段（/v1/messages 为 true，
// /v1/messages/count_tokens 为 false）。
// betaHeader 是请求头 anthropic-beta 的原始值（可为空），用于
// 校验依赖 beta 的功能（如 thinking.display="updates"）。
// 返回 nil 表示校验通过，否则返回与官方 API 一致的错误消息。
func ValidateAnthropicRequest(body []byte, requireMaxTokens bool, betaHeader string) error {
	// ── model: required string ──
	model := gjson.GetBytes(body, "model")
	if !model.Exists() || model.Type != gjson.String || model.Str == "" {
		return fmt.Errorf("\"model\" is a required property")
	}

	// ── max_tokens: required (when requireMaxTokens), integer >= 1 ──
	if requireMaxTokens {
		mt := gjson.GetBytes(body, "max_tokens")
		if !mt.Exists() {
			return fmt.Errorf("\"max_tokens\" is a required property")
		}
		if mt.Type != gjson.Number {
			return fmt.Errorf("\"max_tokens\" must be an integer")
		}
		if mt.Int() < 1 {
			return fmt.Errorf("\"max_tokens\" must be greater than or equal to 1")
		}
	}

	// ── messages: required, non-empty array ──
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.Exists() {
		return fmt.Errorf("\"messages\" is a required property")
	}
	if !msgs.IsArray() {
		return fmt.Errorf("\"messages\" must be an array")
	}
	arr := msgs.Array()
	if len(arr) == 0 {
		return fmt.Errorf("\"messages\" must be a non-empty array")
	}

	// ── messages[i]: role + content ──
	for i, m := range arr {
		role := m.Get("role")
		if !role.Exists() {
			return fmt.Errorf("\"messages[%d].role\" is a required property", i)
		}
		if role.Type != gjson.String {
			return fmt.Errorf("\"messages[%d].role\" must be a string", i)
		}
		// 真实 API 对 messages 中的 role=system 返回 200（真伪验证实测），
		// 故在 user/assistant 之外额外放行 system；其余角色仍 400。
		if role.Str != "user" && role.Str != "assistant" && role.Str != "system" {
			return fmt.Errorf("\"messages[%d].role\" must be one of: \"user\", \"assistant\"", i)
		}
		if !m.Get("content").Exists() {
			return fmt.Errorf("\"messages[%d].content\" is a required property", i)
		}
	}

	// ── temperature: optional, 0.0 <= t <= 1.0 ──
	// Opus 4.6 之后发布的模型仅接受 1.0（向后兼容），其他值 400
	if t := gjson.GetBytes(body, "temperature"); t.Exists() {
		if t.Type != gjson.Number {
			return fmt.Errorf("\"temperature\" must be a number")
		}
		if f := t.Float(); f < 0.0 || f > 1.0 {
			return fmt.Errorf("\"temperature\" must be between 0.0 and 1.0")
		}
		if familyRejectsSamplingParams(model.Str) && t.Float() != 1.0 {
			return errors.New(`"temperature" must be 1.0 for this model`)
		}
	}

	// ── top_p: optional, 0.0 <= tp <= 1.0 ──
	// Opus 4.6 之后发布的模型仅接受 >= 0.99（向后兼容），其他值 400
	if tp := gjson.GetBytes(body, "top_p"); tp.Exists() {
		if tp.Type != gjson.Number {
			return fmt.Errorf("\"top_p\" must be a number")
		}
		if f := tp.Float(); f < 0.0 || f > 1.0 {
			return fmt.Errorf("\"top_p\" must be between 0.0 and 1.0")
		}
		if familyRejectsSamplingParams(model.Str) && tp.Float() < 0.99 {
			return errors.New(`"top_p" must be >= 0.99 for this model`)
		}
	}

	// ── top_k: optional, integer >= 1 ──
	// Opus 4.6 之后发布的模型不接受任何 top_k 值
	if tk := gjson.GetBytes(body, "top_k"); tk.Exists() {
		if tk.Type != gjson.Number {
			return fmt.Errorf("\"top_k\" must be an integer")
		}
		if tk.Int() < 1 {
			return fmt.Errorf("\"top_k\" must be greater than or equal to 1")
		}
		if familyRejectsSamplingParams(model.Str) {
			return errors.New(`"top_k" is not supported for this model`)
		}
	}

	// ── thinking: optional, model-aware type validation ──
	if th := gjson.GetBytes(body, "thinking"); th.Exists() {
		tt := th.Get("type")
		if !tt.Exists() {
			return fmt.Errorf("\"thinking.type\" is a required property")
		}

		// Determine allowed thinking types based on model family.
		allowed := allowedThinkingTypes(model.Str)
		if !sliceContains(allowed, tt.Str) {
			return thinkingTypeError(normalizeThinkingModelFamily(model.Str), tt.Str)
		}

		// ── thinking.display: 枚举 + beta 门控 + disabled 禁配 ──
		// 官方：display ∈ {summarized, omitted, updates}；
		// "updates" 需 beta header thinking-display-updates-2026-08-18，
		// 缺失时与未知 display 值一样 400；type=disabled 时无东西可展示，
		// 携带 display 即 400。
		if disp := th.Get("display"); disp.Exists() {
			if disp.Type != gjson.String {
				return fmt.Errorf(`"thinking.display" must be a string`)
			}
			if tt.Str == "disabled" {
				return errors.New(`"thinking.display" is not supported when "thinking.type" is "disabled"`)
			}
			switch disp.Str {
			case "summarized", "omitted":
			case "updates":
				if !betaHeaderContains(betaHeader, BetaThinkingDisplayUpdates) {
					return fmt.Errorf(`"thinking.display" value "updates" requires the beta header %s`, BetaThinkingDisplayUpdates)
				}
			default:
				return errors.New(`"thinking.display" must be one of: "summarized", "omitted", "updates"`)
			}
		}

		if tt.Str == "enabled" {
			bt := th.Get("budget_tokens")
			if !bt.Exists() {
				return fmt.Errorf("\"thinking.budget_tokens\" is a required property")
			}
			if bt.Type != gjson.Number {
				return fmt.Errorf("\"thinking.budget_tokens\" must be an integer")
			}
			if bt.Int() < 1024 {
				return fmt.Errorf("\"thinking.budget_tokens\" must be greater than or equal to 1024")
			}
			// 官方：budget_tokens 必须严格小于 max_tokens（thinking tokens 计入
			// max_tokens，须为最终回复留出空间）。实测官方原文：
			//   `max_tokens` must be greater than `thinking.budget_tokens`.
			// 例外：interleaved thinking（anthropic-beta 携带
			// interleaved-thinking-2025-05-14）时 budget 横跨同一 assistant
			// turn 的所有 thinking 块，允许超过 max_tokens。
			// count_tokens 端点无 max_tokens，无从比较，放行。
			if mt := gjson.GetBytes(body, "max_tokens"); mt.Exists() && mt.Type == gjson.Number {
				if bt.Int() >= mt.Int() && !betaHeaderContains(betaHeader, BetaInterleavedThinking) {
					return errors.New("`max_tokens` must be greater than `thinking.budget_tokens`.")
				}
			}
		}
	}

	// ── prefill: 4.6+ / Mythos Preview 不支持 assistant 预填充 ──
	// 仅当最后一条 assistant 消息带文本内容时才视为 prefill；
	// 仅含 tool_use 等结构化块的 assistant 消息不属于 prefill，放行交给上游判定。
	if familySupportsPrefillReject(model.Str) && len(arr) > 0 {
		last := arr[len(arr)-1]
		if last.Get("role").Str == "assistant" && assistantHasTextContent(last) {
			return errors.New(`This model does not support assistant message prefill. The conversation must end with a user message.`)
		}
	}

	// ── tool_choice: Fable 5.1 / Mythos 5.1 不支持强制工具调用 ──
	if tc := gjson.GetBytes(body, "tool_choice"); tc.Exists() {
		tcType := tc.Get("type").Str
		if (tcType == "tool" || tcType == "any") && familyRejectsForcedToolChoice(model.Str) {
			return errors.New(`tool_choice: type "tool" and "any" are not supported for this model.`)
		}
	}

	// ── max_tokens: 不得超过模型的最大输出上限 ──
	// 官方对不同模型有不同的 max_tokens 上限，超过即 400。
	// 仅对已知上限的模型收紧；未知模型放行交给上游判定。
	// 错误文案与官方 API 逐字一致（pydantic 风格）：
	//   'max_tokens': 128001 > 128000 - 'max_tokens' should be smaller than or equal to 128000
	if mt := gjson.GetBytes(body, "max_tokens"); mt.Exists() && mt.Type == gjson.Number {
		if n := mt.Int(); n > 0 {
			if cap, ok := modelMaxOutputTokens(model.Str); ok && n > int64(cap) {
				return fmt.Errorf(`'max_tokens': %d > %d - 'max_tokens' should be smaller than or equal to %d`, n, cap, cap)
			}
		}
	}

	// ── output_config.effort: 取值必须合法且在模型支持的级别内 ──
	if oc := gjson.GetBytes(body, "output_config"); oc.Exists() {
		if eff := oc.Get("effort"); eff.Exists() {
			if eff.Type != gjson.String {
				return fmt.Errorf(`"output_config.effort" must be a string`)
			}
			if !sliceContains(allEffortLevels, eff.Str) {
				return fmt.Errorf(`"output_config.effort" must be one of: %s`, joinQuoted(allEffortLevels))
			}
			if levels := effortLevelsForModel(model.Str); len(levels) > 0 && !sliceContains(levels, eff.Str) {
				return fmt.Errorf(`"output_config.effort" value "%s" is not supported for this model`, eff.Str)
			}
		}
	}

	// ── thinking.type=disabled + effort=xhigh/max 组合：Opus 5 起不可关思考 ──
	// 官方：Claude Opus 5 及之后模型在 xhigh/max effort 下无法关闭 thinking，
	// 两者组合返回 400。低档 effort（≤high）允许 disabled。
	// 注意：仅「Opus 5 及之后」（opus-5/sonnet-5/fable-5*/mythos-5*）受此约束；
	// Opus 4.8 / 4.7 / 4.6 等更早模型 disabled + xhigh 仍可 200（真伪验证实测）。
	if th := gjson.GetBytes(body, "thinking"); th.Exists() && th.Get("type").Str == "disabled" &&
		familyRejectsDisabledWithHighEffort(model.Str) {
		if eff := gjson.GetBytes(body, "output_config.effort"); eff.Exists() && eff.Type == gjson.String {
			if eff.Str == "xhigh" || eff.Str == "max" {
				return errors.New(`"thinking.type" value "disabled" is not supported with "output_config.effort" value "` + eff.Str + `" for this model`)
			}
		}
	}

	// ── speed: fast mode 仅 Opus 5 / Opus 4.8 支持，其余模型传 speed=fast 应 400 ──
	if sp := gjson.GetBytes(body, "speed"); sp.Exists() && sp.Type == gjson.String && strings.EqualFold(sp.Str, "fast") {
		if !modelSupportsFastMode(model.Str) {
			return errors.New(`"speed" value "fast" is not supported for this model`)
		}
	}

	return nil
}

// ValidateThinkingSignatures 对请求体中 assistant 消息里的 thinking /
// redacted_thinking 块做签名结构校验。
//
// 背景：上游（尤其经 New-API 等中转的 Opus 5 链路）普遍不校验 thinking 签名
// 的内容，坏签名会被照单全收。为了让本网关自守门，这里在转发前主动把关：
//   - signature 字段缺失或为空串：放行（交由既有的预过滤/整流逻辑处理，
//     那些路径专门负责"缺签名"场景，避免在此重复拦截）。
//   - signature 存在且非空：必须是合法 base64，且解码后不少于
//     thinkingSignatureMinDecodedLen 字节，否则返回 400 风格错误。
//
// 该检查是纯结构性的：它只能识别"格式坏了"的签名（空/截断/乱码/非 base64），
// 无法识别"格式合法但内容被篡改"的签名（网关没有 Anthropic 密钥，做不了密码学
// 验签）。因此对"上游每轮签发新签名"这一正常情形天然免疫——只要新签名格式合法
// 就会放行，不会因为"和上一次不一样"而被误杀。
//
// 作用域：仅 /v1/messages，count_tokens 不做。
func ValidateThinkingSignatures(body []byte) error {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return nil
	}
	for mi, m := range msgs.Array() {
		if m.Get("role").Str != "assistant" {
			continue
		}
		content := m.Get("content")
		if !content.IsArray() {
			continue
		}
		for bi, block := range content.Array() {
			btype := block.Get("type").Str
			if btype != "thinking" && btype != "redacted_thinking" {
				continue
			}
			sig := block.Get("signature")
			if !sig.Exists() || sig.Type != gjson.String || sig.Str == "" {
				continue
			}
			if err := checkThinkingSignatureFormat(sig.Str); err != nil {
				return fmt.Errorf("messages.%d.content.%d: %v", mi, bi, err)
			}
		}
	}
	return nil
}

// checkThinkingSignatureFormat 校验单个签名字符串的结构合法性。
// 错误文案对齐官方 "Invalid `signature` in `thinking` block" 的风格。
func checkThinkingSignatureFormat(sig string) error {
	const badBase64 = "Invalid `signature` in `thinking` block: signature is not valid base64"
	const tooShort = "Invalid `signature` in `thinking` block: signature is too short"

	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		// 兼容 URL-safe base64（个别客户端/上游可能使用）
		if decoded2, err2 := base64.URLEncoding.DecodeString(sig); err2 == nil {
			decoded = decoded2
		} else {
			return errors.New(badBase64)
		}
	}
	if len(decoded) < thinkingSignatureMinDecodedLen {
		return errors.New(tooShort)
	}
	return nil
}

// allEffortLevels 是官方 output_config.effort 的全部合法取值。
var allEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// effortLevelSets 是各模型家族的 effort 级别集合（源自 sub2api
// pkg/claude/effort_catalog.go 的 EffortLevelsForModel，整表照抄）。
var (
	effortLowMediumHigh         = []string{"low", "medium", "high"}
	effortLowMediumHighMax      = []string{"low", "medium", "high", "max"}
	effortLowMediumHighXHighMax = []string{"low", "medium", "high", "xhigh", "max"}
)

var effortFamilies = []struct {
	family string
	levels []string
}{
	{family: "claude-mythos-preview", levels: effortLowMediumHighMax},
	{family: "claude-mythos-5", levels: effortLowMediumHighXHighMax},
	{family: "claude-fable-5", levels: effortLowMediumHighXHighMax},
	{family: "claude-sonnet-4-6", levels: effortLowMediumHighMax},
	{family: "claude-sonnet-5", levels: effortLowMediumHighXHighMax},
	{family: "claude-opus-4-8", levels: effortLowMediumHighXHighMax},
	{family: "claude-opus-4-7", levels: effortLowMediumHighXHighMax},
	{family: "claude-opus-4-6", levels: effortLowMediumHighMax},
	{family: "claude-opus-4-5", levels: effortLowMediumHigh},
	{family: "claude-opus-5", levels: effortLowMediumHighXHighMax},
}

// effortLevelsForModel 返回模型支持的 output_config.effort 级别。
// 匹配规则：模型 ID 先做归一化（小写、去 models/ 前缀、去 anthropic. 前缀、
// 去 -thinking 后缀、去 8 位日期后缀），然后要求精确等于家族名或以
// <家族名>- 为前缀（所以 claude-fable-5-1 命中 claude-fable-5 条目）。
// 未知模型返回 nil（不限制）。
func effortLevelsForModel(model string) []string {
	id := normalizeEffortModelID(model)
	for _, entry := range effortFamilies {
		if id == entry.family || strings.HasPrefix(id, entry.family+"-") {
			return append([]string(nil), entry.levels...)
		}
	}
	return nil
}

// normalizeEffortModelID 归一化模型 ID 用于 effort 分级表匹配。
func normalizeEffortModelID(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	id = strings.TrimPrefix(id, "models/")
	if slash := strings.IndexByte(id, '/'); slash >= 0 {
		id = strings.TrimPrefix(strings.TrimSpace(id[slash+1:]), "models/")
	}
	id = strings.TrimPrefix(id, "anthropic.")
	id = strings.TrimSuffix(id, "-thinking")
	if len(id) >= 9 {
		suffix := id[len(id)-9:]
		if suffix[0] == '-' {
			digits := true
			for _, r := range suffix[1:] {
				if r < '0' || r > '9' {
					digits = false
					break
				}
			}
			if digits {
				id = id[:len(id)-9]
			}
		}
	}
	return id
}

// modelMaxOutputTokens 返回模型的同步 Messages API 最大输出 token 上限。
// 依据官方各模型页（2026-09-17 逐一核实）：
//   - 128K：Fable 5.1 / Fable 5 / Mythos 5 / Mythos 5.1 / Opus 5 / Sonnet 5 /
//     Opus 4.7 / 4.8 / Opus 4.6 / Sonnet 4.6
//   - 64K：Haiku 4.5 / Opus 4.5 / Sonnet 4.5
//
// Mythos 5 官方明言与 Fable 5 共享规格（128K）；mythos-preview 无公开规格页，
// 保持 fail-open。未知模型返回 ok=false（放行）。
//
// 注意：官方文档的 "128K" 是十进制 128000，不是 128*1024=131072。
// 实测（2026-09-17）：claude-opus-5 传 max_tokens=128001 官方返回 400，
// 128000 放行。早期实现误用 128*1024 导致 128001~131072 区间被误放行。
func modelMaxOutputTokens(model string) (int, bool) {
	switch normalizeThinkingModelFamily(model) {
	case "claude-fable-5-1", "claude-fable-5",
		"claude-mythos-5-1", "claude-mythos-5",
		"claude-opus-5", "claude-sonnet-5",
		"claude-opus-4-8", "claude-opus-4-7",
		"claude-opus-4-6", "claude-sonnet-4-6":
		return 128000, true
	case "claude-haiku-4-5", "claude-opus-4-5", "claude-sonnet-4-5":
		return 64000, true
	}
	return 0, false
}

// familyRejectsDisabledWithHighEffort 判断模型是否属于「Opus 5 及之后」，
// 即在 thinking.type=disabled 搭配 effort=xhigh/max 时应返回 400。
// 仅 opus-5 / sonnet-5 / fable-5* / mythos-5* 受此约束；更早的
// opus-4-8 / opus-4-7 / opus-4-6 等 disabled + xhigh 仍可 200（真伪验证实测）。
func familyRejectsDisabledWithHighEffort(model string) bool {
	switch normalizeThinkingModelFamily(model) {
	case "claude-opus-5", "claude-sonnet-5",
		"claude-fable-5", "claude-fable-5-1",
		"claude-mythos-5", "claude-mythos-5-1":
		return true
	}
	return false
}

// modelSupportsFastMode 判断模型是否支持 fast mode（speed=fast）。
// 目前仅 Claude Opus 5 / Opus 4.8 支持；其余模型传 speed=fast 应 400。
func modelSupportsFastMode(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if !strings.Contains(m, "opus") {
		return false
	}
	// "opus-5" 必须先判：不能用裸 "5" 匹配，否则 claude-opus-4-5 会被误判。
	if strings.Contains(m, "opus-5") || strings.Contains(m, "opus5") {
		return true
	}
	return strings.Contains(m, "4.8") || strings.Contains(m, "4-8")
}

// thinkingTypeError 返回与官方 API 完全一致的 thinking.type 拒绝消息。
// 参见 https://platform.claude.com/docs/en/api/errors#common-validation-errors
func thinkingTypeError(family, requested string) error {
	switch requested {
	case "enabled":
		// Claude 4.7+ 移除了 extended thinking
		return errors.New(`"thinking.type.enabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`)
	case "adaptive":
		// 仅支持 extended thinking 的模型（Claude 4.5 及更早）
		return errors.New(`adaptive thinking is not supported on this model`)
	case "disabled":
		// Fable 5.1 / Mythos 5.1 / Fable 5 / Mythos 5
		if family == "claude-mythos-preview" {
			return errors.New(`"thinking.type.disabled" is not supported for this model. Thinking defaults to adaptive mode when not specified; use "thinking.type.enabled" with "budget_tokens" for extended thinking.`)
		}
		return errors.New(`"thinking.type.disabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`)
	default:
		return fmt.Errorf("\"thinking.type\" must be one of: %s", joinQuoted(allThinkingTypes))
	}
}

// familySupportsPrefillReject 判断模型是否属于「不支持 assistant prefill」的范围：
// Claude 4.6 及之后的所有模型，以及 Claude Mythos Preview。
func familySupportsPrefillReject(model string) bool {
	family := normalizeThinkingModelFamily(model)
	if family == "claude-mythos-preview" {
		return true
	}
	// 已知会拒绝 prefill 的家族白名单（4.6+）
	switch family {
	case "claude-opus-4-6", "claude-sonnet-4-6",
		"claude-opus-4-7", "claude-opus-4-8", "claude-opus-5",
		"claude-sonnet-5",
		"claude-fable-5", "claude-fable-5-1",
		"claude-mythos-5", "claude-mythos-5-1":
		return true
	}
	return false
}

// familyRejectsForcedToolChoice 判断模型是否拒绝 tool_choice 的 "tool"/"any"：
// Claude Fable 5.1 与 Claude Mythos 5.1。
func familyRejectsForcedToolChoice(model string) bool {
	switch normalizeThinkingModelFamily(model) {
	case "claude-fable-5-1", "claude-mythos-5-1":
		return true
	}
	return false
}

// familyRejectsSamplingParams 判断模型是否拒绝非默认采样参数：
// Claude Opus 4.6 之后发布的模型（temperature 仅接受 1.0、top_p 仅接受 >= 0.99、
// top_k 一律拒绝）。Opus 4.6 / Sonnet 4.6 是最后支持采样参数的模型。
func familyRejectsSamplingParams(model string) bool {
	family := normalizeThinkingModelFamily(model)
	if family == "claude-mythos-preview" {
		return true
	}
	switch family {
	case "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5",
		"claude-sonnet-5",
		"claude-fable-5", "claude-fable-5-1",
		"claude-mythos-5", "claude-mythos-5-1":
		return true
	}
	return false
}

// assistantHasTextContent 判断 assistant 消息是否带有文本内容（即构成 prefill）。
// content 为字符串时直接视为文本；为数组时任一 text 块非空即为 true。
// 解析不确定时保守返回 false（放行，交给上游判定），避免误伤合法流量。
func assistantHasTextContent(msg gjson.Result) bool {
	content := msg.Get("content")
	if content.IsArray() {
		for _, block := range content.Array() {
			if block.Get("type").Str == "text" && strings.TrimSpace(block.Get("text").Str) != "" {
				return true
			}
		}
		return false
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.Str) != ""
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Model-aware thinking.type validation
//
// Based on the official Anthropic documentation:
//
//	| Model Family          | Allowed thinking.type       | Rejected with 400       |
//	|-----------------------|-----------------------------|-------------------------|
//	| Fable 5.1 / 5        | adaptive, disabled*         | enabled                 |
//	| Mythos 5.1 / 5       | adaptive, disabled*         | enabled                 |
//	| Opus 5               | adaptive, disabled          | enabled                 |
//	| Opus 4.8 / 4.7       | adaptive                    | enabled                 |
//	| Sonnet 5             | adaptive                    | enabled                 |
//	| Mythos Preview       | adaptive, enabled           | disabled                |
//	| Opus 4.6 / Sonnet 4.6| adaptive, enabled (deprec.) | (none)                |
//	| Opus 4.5             | enabled, disabled           | adaptive                |
//	| Haiku 4.5            | enabled, disabled           | adaptive                |
//	| Sonnet 4.5           | enabled, disabled           | adaptive                |
//
//	* 官方文档称 Fable/Mythos 5.x 拒绝 disabled，但实测返回 200，按实测放宽。
//
// Models not in the table accept all three types (backward compat).
// ─────────────────────────────────────────────────────────────────────────────

// thinkingTypeRules maps normalized model family → allowed thinking.type values.
// Normalization strips date suffixes (-20251101) and -thinking suffix.
var thinkingTypeRules = map[string][]string{
	// 自适应为主，thinking 常开 (仅 adaptive; enabled/disabled 均 400 拒绝)。
	// 官方文档（platform.claude.com/docs/en/api/errors "Thinking cannot be
	// disabled"）：Fable/Mythos 5.x 发送 thinking.type=disabled 返回 400。
	// 早期参考实现按实测放宽（实测 200），2026-09-18 按官方文档收紧。
	"claude-fable-5-1":  {"adaptive"},
	"claude-mythos-5-1": {"adaptive"},
	"claude-fable-5":    {"adaptive"},
	"claude-mythos-5":   {"adaptive"},

	// 自适应为主 (adaptive + disabled; 仅 enabled 被 400 拒绝)。
	// Opus 5 官方明文接受 disabled（effort ≤ high 时）。
	"claude-opus-5": {"adaptive", "disabled"},

	// 自适应为主，默认关闭 (adaptive + disabled; 仅 enabled 被 400 拒绝)
	"claude-opus-4-8": {"adaptive", "disabled"},
	"claude-opus-4-7": {"adaptive", "disabled"},
	"claude-sonnet-5": {"adaptive", "disabled"},

	// 自适应 + 扩展 (adaptive + enabled; 仅 disabled 被 400 拒绝)
	"claude-mythos-preview": {"adaptive", "enabled"},

	// 仅扩展 (enabled + disabled; adaptive 被 400 拒绝)
	"claude-opus-4-5":   {"enabled", "disabled"},
	"claude-haiku-4-5":  {"enabled", "disabled"},
	"claude-sonnet-4-5": {"enabled", "disabled"},

	// Opus 4.6 / Sonnet 4.6: 自适应/扩展均已弃用，不限制（无需条目，fallthrough 接受全部）
}

// allThinkingTypes is the fallback for unknown models.
var allThinkingTypes = []string{"enabled", "disabled", "adaptive"}

// allowedThinkingTypes returns the allowed thinking.type values for a model.
func allowedThinkingTypes(model string) []string {
	family := normalizeThinkingModelFamily(model)
	if allowed, ok := thinkingTypeRules[family]; ok {
		return allowed
	}
	return allThinkingTypes
}

// normalizeThinkingModelFamily strips date suffixes (-YYYYMMDD) and -thinking
// suffix from a model ID to get the base family key.
func normalizeThinkingModelFamily(model string) string {
	m := strings.ToLower(model)
	// Strip trailing date suffix like -20251101 (8 digits after a dash)
	if len(m) >= 10 {
		dashIdx := len(m) - 9
		if m[dashIdx] == '-' {
			allDigits := true
			for i := dashIdx + 1; i < len(m); i++ {
				if m[i] < '0' || m[i] > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				m = m[:dashIdx]
			}
		}
	}
	// Strip -thinking suffix
	m = strings.TrimSuffix(m, "-thinking")
	return m
}

func sliceContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// betaHeaderContains 判断 anthropic-beta 请求头（逗号分隔列表）是否包含指定 beta。
func betaHeaderContains(header, beta string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.TrimSpace(part) == beta {
			return true
		}
	}
	return false
}

func joinQuoted(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	result := quoted[0]
	for _, q := range quoted[1:] {
		result += ", " + q
	}
	return result
}
