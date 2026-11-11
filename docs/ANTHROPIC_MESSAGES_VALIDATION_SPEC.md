# Anthropic `/v1/messages` 官方对齐校验 —— 移植规格书（已实现版）

> 本文档汇总 `anthropic-api-style-v0.2.4` 分支（基于 main `bdb42e22f`）的全部改动，
> 以及 **new-api 中已完成的移植实现**。截至 2026-09-25，文中全部规则均已落地于
> new-api 分支 `v1.0.0-rc.24-anthropic-validation`（基于 tag `v1.0.0-rc.24`，HEAD
> `bd0d0e541`），文档已与 new-api 源码同步——每条规则给出触发条件、官方逐字错误
> 文案、例外情况及 **new-api 中的实现位置**，可供其他网关（如 sub2api）直接参照移植。
>
> 源实现（sub2api，规则出处）：
> - `backend/internal/handler/gateway_anthropic_validation.go`（核心校验，约 550 行）
> - `backend/internal/handler/gateway_handler.go`（接线点）
> - `backend/internal/service/setting_features.go` + `domain_constants.go`（配置开关）
> - `backend/internal/pkg/claude/constants.go`（beta 常量）
>
> 参考实现（new-api，移植完成，本文基准）：
> - `relay/helper/anthropic_messages_validation.go`（核心校验 + 签名骨架/深度指纹）
> - `relay/helper/anthropic_signature_registry.go`（签名发放注册表 + 配对指纹）
> - `relay/channel/claude/relay-claude.go`（登记侧收割 + 流式 thinking 累积器）
> - `relay/claude_handler.go`（原始字节透传）
> - `controller/relay.go`（接线点 + 官方错误写出器）
> - `middleware/distributor.go`（404 统一 + count_tokens 路径归一）
> - `middleware/utils.go`（404 官方报文写出器）
> - `relaykit/types/error.go`（Claude 错误类型归一化）
> - `setting/anthropic.go`（配置开关）
>
> 回归测试（new-api，全部通过）：
> - `relay/helper/anthropic_messages_validation_test.go`（约 1100 行，R1–R36 全覆盖）
> - `relay/helper/anthropic_signature_registry_test.go`（注册表 + 配对绑定）
> - `relay/channel/claude/signature_harvest_test.go`（登记侧收割）
> - `relay/claude_original_body_test.go`（原始字节透传决策）
> - `relaykit/types/error_nil_test.go`（错误类型归一化）

---

## 1. 提交清单

### 1.1 sub2api 源提交（时间序）

| 提交 | 日期 | 内容 |
|------|------|------|
| `7de3295a6` | 09-14 | 主体校验器 `validateAnthropicRequest`：model/max_tokens/messages/role、temperature/top_p/top_k 分家族规则、thinking.type 分模型矩阵、prefill、forced tool_choice、max_tokens 模型上限（128K/64K）、output_config.effort 枚举+分模型支持、speed=fast；413 错误类型改为 `request_too_large`；错误响应带顶层 `request_id`；count_tokens 共用校验（max_tokens 可选） |
| `1c6a2a8d5` | 09-14 | `request_id` 采用官方 `req_` 前缀格式；响应头同时输出 `request-id`（与 body 字段同源） |
| `98718ec24` | 09-14 | .gitignore 杂项 |
| `e2c5b107c` | 09-16 | 校验器升级为三参 `(body, requireMaxTokens, betaHeader)`；thinking.display 枚举 + beta 门控；`disabled` + effort xhigh/max 组合拒绝；Fable/Mythos 5.x 的 disabled 按实测放宽 |
| `f1c5a50a0` | 09-17 | `budget_tokens` 必须**严格小于** `max_tokens`（官方原文文案）；interleaved-thinking beta 豁免；count_tokens 豁免 |
| `e62c69440` | 09-17 | thinking 签名**结构校验**（可配置默认开）：非空签名必须合法 base64 且解码 ≥32 字节，否则 400 |
| `e62b16903` | 09-17 | **OpenAI 桥接路径**（组平台为 OpenAI-Responses 兼容时走的 `OpenAIGatewayHandler.Messages`）同样挂 `validateAnthropicRequest`，两条 `/v1/messages` 入口校验一致 |
| `9d452bb7a` | 09-17 | max_tokens 上限修正为十进制 **128000/64000**（早期误用 128×1024=131072；实测 128001 官方 400） |
| `38dad16dc` | 09-17 | max_tokens 超限文案改为官方 pydantic 风格逐字一致（见 R36） |
| `3a1038097` | 09-17 | 404 模型不可用的线上错误类型统一为官方 `not_found_error`（见 §2.7） |
| `296f3b3cb` | 09-17 | max_tokens 上限表扩至官方全部有公开规格的模型（见 R36）；新增 13 模型 × 双向断言回归 |
| `4db39d2c5` | 09-18 | R10 位置敏感：`messages[0].role=system` → 400，中间位置 → 200（对齐真实 API）；count_tokens 渠道路径归一（`/v1/messages/count_tokens` 匹配时剥离后缀，复用 Claude Messages 渠道） |
| `b0f2e38e5` | 09-20 | thinking 签名**深度指纹校验**：内层元数据须含 "thinking" 标识 + UUID + 模型名一致（拦跨模型重放）；外层骨架须良构且含版本标记 |
| （2026-09-21） | 09-21 | thinking 签名**发放注册表**（§2.5b）：上游响应签名登记 + 请求侧成员校验，拦单字符篡改/跨用户重放；密码学解剖确认签名是密钥化 MAC，无密钥不可真验签 |
| （2026-09-21） | 09-21 | 注册表升级为**签名↔thinking 文本配对绑定**（§2.5b）：thinking 块按 `sha256(sig‖"\x00"‖thinking)` 记账，拦"真签名配篡改文本"的换绑攻击；redacted_thinking 无文本仍按裸签名 |
| （2026-09-21） | 09-21 | **骨架校验收窄**：外层 field1 版本标记改为**非必需**（§2.5）。线上实测 sonnet-5/早期 opus-4-8 真签名外层无 field1（直接从 field2 起），旧逻辑把它们误判 malformed；现只要求外层良构 + field2 内层良构，首字节篡改仍可拦 |
| （2026-09-21） | 09-21 | 新增 **§2.5a 签名结构解剖**：五份真实样本逆向出的 protobuf 布局、AEAD 密文结论、篡改位置×拦截层对照表 |

### 1.2 new-api 移植提交（时间序，分支 `v1.0.0-rc.24-anthropic-validation`，基于 tag `v1.0.0-rc.24`）

| 提交 | 日期 | 内容 |
|------|------|------|
| `94affd303` | 09-17 | 核心校验器 `ValidateAnthropicRequest` 移植（R1–R36 全集，`relay/helper/anthropic_messages_validation.go`） |
| `1c31f3b0f` | 09-17 | 接线：`controller/relay.go::Relay` 在 `GetAndValidateRequest` 之前挂 `ValidateClaudeMessagesRequest`（单一挂载点覆盖 /v1/messages 与 count_tokens 两入口） |
| `5be0352c6` | 09-17 | 加入本规格书 + 探针脚本 `test_claude5_mode_probe.sh` |
| `501dce4db` | 09-18 | auto 分组未映射模型 → 404；网关未实质改写请求时**转发原始字节**（§2.8） |
| `7b87a8627` | 09-18 | 全量校验对打到 /v1/messages 的**所有模型**生效（未知家族在规则内部 fail-open），不再限定 claude 前缀 |
| `f1a51b2de` / `7b5f4df61` | 09-18 | 404 报文对齐官方 Error shapes 标准报文 |
| `2648cf6b6` | 09-18 | 渠道选择失败两种形态统一 404（§2.7） |
| `811926d78` | 09-18 | Fable/Mythos 5.x `thinking.type=disabled` 按官方文档收紧为 400 |
| `c9ab5e5af` | 09-18 | 按真伪验证实测对齐真实 API 行为（R10 位置敏感等） |
| `4db39d2c5` | 09-18 | R10 位置敏感；count_tokens 渠道路径归一 |
| `2aeea95bf` | 09-20 | thinking 签名**严格骨架校验**（外层良构 + field2 内层良构；field1 版本标记非必需）+ **Claude 错误类型归一化**（§2.6） |
| `349ff6059` | 09-20 | 简化非法 base64 签名的错误文案 |
| `78060f736` / `167e58b80` | 09-20 | base64 编码变体容忍度加入后又回滚（最终状态：StdEncoding，失败再试 URLEncoding） |
| `b0f2e38e5` | 09-20 | thinking 签名**深度指纹校验**（§2.5 第 3 层） |
| `bd0d0e541` | 09-21 | **签名发放注册表** + 签名↔thinking 文本配对绑定（§2.5b） |
| （2026-09-26） | 09-26 | **R34 收窄到仅 claude-opus-5**：disabled + effort∈{xhigh,max} 的 400 限制此前误扩到整个 5 系（opus-5/sonnet-5/fable-5*/mythos-5*）。官方《思考功能故障排查》模型表脚注②只挂在 Opus 5 行，且测试台实测 sonnet-5 的 disabled+xhigh 返回 200（用例 thinking.disabled_xhigh_accepted）。现由 `familyRejectsDisabledWithHighEffort` 门控，仅 opus-5 拒绝，其余模型 fail-open；新增 8 模型 × 2 effort 回归测试 |
| （2026-09-26） | 09-26 | **claude-opus-5-5 补入全部校验矩阵**（依据官方 2026-09-25 文档快照）：thinking.type 仅 adaptive（enabled/disabled 均 400，官方明言 Opus 5.5 reject disabled）；max_tokens 上限 128000；拒绝非默认采样参数；拒绝 forced tool_choice（与 fable-5-1/mythos-5-1 并列）；拒绝 assistant prefill。handler 层 `ValidateAnthropicRequest` 五张矩阵补齐；新增 `TestValidateAnthropicRequest_Opus55Matrix` 10 子用例 |
| （2026-09-26） | 09-26 | **image 块校验**（§2.9）：source 三类型结构 + media_type 四值枚举（官方逐字文案）+ base64 ≤10MB + 图片数 ≤600 + 像素 ≤8000×8000（>20 张收紧至 2000px，含 tool_result 内嵌计数）。新文件 `relay/helper/anthropic_image_validation.go`（含 PNG/JPEG/GIF/WebP 头部解析器）；新增 9 组回归测试 |

合计（`git diff v1.0.0-rc.24..HEAD`，截至 `bd0d0e541`）：25 文件，+4206 / -16。（09-26 三项改动尚未计入）

**生产验证（2026-09-17，windf 节点部署 `296f3b3cb` 后实测）**：
`claude-fable-5-1` + `max_tokens=128001` → 400 官方文案；`=131073` → 400；`=128000` → 200 正常出流。全部符合预期。

---

## 2. 校验规则全集（照抄区）

以下每条规则的**错误文案必须逐字一致**（客户端/上游可能按文案匹配）。
统一返回 `400` + `{"type":"error","error":{"type":"invalid_request_error","message":"..."}}`。

### 2.1 基础字段

| # | 规则 | 错误文案（逐字） |
|---|------|------------------|
| R1 | `model` 缺失/非字符串/空串 | `"model" is a required property` |
| R2 | `max_tokens` 缺失（仅 /v1/messages；count_tokens 豁免） | `"max_tokens" is a required property` |
| R3 | `max_tokens` 非整数 | `"max_tokens" must be an integer` |
| R4 | `max_tokens` < 1 | `"max_tokens" must be greater than or equal to 1` |
| R5 | `messages` 缺失 | `"messages" is a required property` |
| R6 | `messages` 非数组 | `"messages" must be an array` |
| R7 | `messages` 空数组 | `"messages" must be a non-empty array` |
| R8 | `messages[i].role` 缺失 | `"messages[i].role" is a required property` |
| R9 | `messages[i].role` 非字符串 | `"messages[i].role" must be a string` |
| R10 | `messages[i].role` ∉ {user, assistant, system}；**位置敏感**：`messages[0].role=system` → 400，`messages[i>0].role=system` → 200（真实 API 行为，2026-09-18 真伪验证实测） | `"messages[i].role" must be one of: "user", "assistant"` |
| R11 | `messages[i].content` 缺失 | `"messages[i].content" is a required property` |

### 2.2 采样参数（分家族）

先算 `family = normalizeModelFamily(model)`（见 §3）。
`rejectsSampling = family ∈ {opus-4-7, opus-4-8, opus-5, opus-5-5, sonnet-5, fable-5, fable-5-1, mythos-5, mythos-5-1, mythos-preview}`

| # | 规则 | 错误文案 |
|---|------|----------|
| R12 | `temperature` 非数字 | `"temperature" must be a number` |
| R13 | `temperature` ∉ [0,1] | `"temperature" must be between 0.0 and 1.0` |
| R14 | rejectsSampling 且 temperature ≠ 1.0 | `"temperature" must be 1.0 for this model` |
| R15 | `top_p` 非数字 | `"top_p" must be a number` |
| R16 | `top_p` ∉ [0,1] | `"top_p" must be between 0.0 and 1.0` |
| R17 | rejectsSampling 且 top_p < 0.99 | `"top_p" must be >= 0.99 for this model` |
| R18 | `top_k` 非整数 | `"top_k" must be an integer` |
| R19 | `top_k` < 1 | `"top_k" must be greater than or equal to 1` |
| R20 | rejectsSampling 且传了 top_k | `"top_k" is not supported for this model` |

### 2.3 thinking（核心矩阵）

`thinking.type` 允许的取值按模型家族（**实测校准过的矩阵**）：

| 模型家族 | 允许 thinking.type | 400 拒绝 |
|----------|--------------------|----------|
| claude-fable-5-1 / fable-5 | adaptive | enabled, disabled |
| claude-mythos-5-1 / mythos-5 | adaptive | enabled, disabled |
| claude-opus-5-5 | adaptive | enabled, disabled（官方明言 Opus 5.5 reject disabled，2026-09-25 文档快照） |
| claude-opus-5 | adaptive, disabled | enabled |
| claude-opus-4-8 / opus-4-7 | adaptive, disabled | enabled |
| claude-sonnet-5 | adaptive, disabled | enabled |
| claude-mythos-preview | adaptive, enabled | disabled |
| claude-opus-4-5 / haiku-4-5 / sonnet-4-5 | enabled, disabled | adaptive |
| claude-opus-4-6 / sonnet-4-6 | 不限（enabled 已弃用但仍可用） | 无 |
| 未知模型 | 三种全收（向后兼容） | 无 |

\* 官方文档称 Fable/Mythos 5.x 拒绝 disabled，但实测返回 200，参考实现曾按实测放宽；2026-09-18 起按官方文档收紧为 400（见 platform.claude.com/docs/en/api/errors "Thinking cannot be disabled"）。

拒绝时的**官方逐字文案**（按被拒的值区分）：

| 被拒值 | 文案 |
|--------|------|
| `enabled`（4.7+ 模型） | `"thinking.type.enabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.` |
| `adaptive`（4.5 系） | `adaptive thinking is not supported on this model` |
| `disabled`（mythos-preview） | `"thinking.type.disabled" is not supported for this model. Thinking defaults to adaptive mode when not specified; use "thinking.type.enabled" with "budget_tokens" for extended thinking.` |
| `disabled`（fable/mythos 5.x，若选择按官方收紧） | `"thinking.type.disabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.` |
| 其他任意值 | `"thinking.type" must be one of: "enabled", "disabled", "adaptive"` |
| `thinking` 存在但缺 `type` | `"thinking.type" is a required property` |

#### thinking.display

| # | 规则 | 文案 |
|---|------|------|
| R21 | display 非字符串 | `"thinking.display" must be a string` |
| R22 | type=disabled 且带 display | `"thinking.display" is not supported when "thinking.type" is "disabled"` |
| R23 | display=updates 且 `anthropic-beta` 头不含 `thinking-display-updates-2026-08-18` | `"thinking.display" value "updates" requires the beta header thinking-display-updates-2026-08-18` |
| R24 | display ∉ {summarized, omitted, updates} | `"thinking.display" must be one of: "summarized", "omitted", "updates"` |

#### thinking.type=enabled 专属

| # | 规则 | 文案 |
|---|------|------|
| R25 | 缺 `budget_tokens` | `"thinking.budget_tokens" is a required property` |
| R26 | budget_tokens 非整数 | `"thinking.budget_tokens" must be an integer` |
| R27 | budget_tokens < 1024 | `"thinking.budget_tokens" must be greater than or equal to 1024` |
| R28 | **budget_tokens ≥ max_tokens**（且 beta 头不含 `interleaved-thinking-2025-05-14`；count_tokens 无 max_tokens 自然豁免） | `` `max_tokens` must be greater than `thinking.budget_tokens`. `` |

### 2.4 组合规则

| # | 规则 | 文案 |
|---|------|------|
| R29 | 4.6+ 家族（opus-4-6/sonnet-4-6/opus-4-7/opus-4-8/opus-5/opus-5-5/sonnet-5/fable-5/fable-5-1/mythos-5/mythos-5-1/mythos-preview）且**最后一条消息是带文本内容的 assistant**（prefill） | `This model does not support assistant message prefill. The conversation must end with a user message.` |
| R30 | fable-5-1 / mythos-5-1 / **opus-5-5** 且 `tool_choice.type` ∈ {tool, any} | `tool_choice: type "tool" and "any" are not supported for this model.` |
| R31 | `output_config.effort` 非字符串 | `"output_config.effort" must be a string` |
| R32 | effort ∉ {low, medium, high, xhigh, max} | `"output_config.effort" must be one of: "low", "medium", "high", "xhigh", "max"` |
| R33 | effort 不在该模型支持级别内（分级表见下表） | `"output_config.effort" value "<v>" is not supported for this model` |
| R34 | thinking.type=disabled 且 effort ∈ {xhigh, max}，**仅限 claude-opus-5**（2026-09-26 收窄：官方《思考功能故障排查》模型表脚注②只挂在 Opus 5 行；此前误扩到整个 5 系，但测试台实测 sonnet-5 的 disabled+xhigh 返回 200，用例 thinking.disabled_xhigh_accepted）；fable-5*/mythos-5* 的 disabled 已被 thinking.type 矩阵先行 400；Opus 4.8/4.7/4.6 等更早模型 disabled+xhigh 仍 200（2026-09-18 真伪验证实测） | `"thinking.type" value "disabled" is not supported with "output_config.effort" value "<effort>" for this model` |

**R33 的 effort 分级表**（源自 `backend/internal/pkg/claude/effort_catalog.go` 的 `EffortLevelsForModel`，移植时整表照抄）：

| 模型家族 | 支持的 effort 级别 |
|----------|--------------------|
| claude-mythos-preview | low, medium, high, max |
| claude-mythos-5（含 mythos-5-1） | low, medium, high, xhigh, max |
| claude-fable-5（含 fable-5-1） | low, medium, high, xhigh, max |
| claude-sonnet-4-6 | low, medium, high, max |
| claude-sonnet-5 | low, medium, high, xhigh, max |
| claude-opus-4-8 | low, medium, high, xhigh, max |
| claude-opus-4-7 | low, medium, high, xhigh, max |
| claude-opus-4-6 | low, medium, high, max |
| claude-opus-4-5 | low, medium, high |
| claude-opus-5 | low, medium, high, xhigh, max |
| 未知模型 | 全部放行（返回 nil = 不限制） |

匹配规则：模型 ID 先做归一化（小写、去 `models/` 前缀、去 `anthropic.` 前缀、去 `-thinking` 后缀、去 8 位日期后缀），然后要求**精确等于家族名或以 `<家族名>-` 为前缀**（所以 `claude-fable-5-1` 命中 `claude-fable-5` 条目）。注意 `claude-haiku-4-5` 不在表中 → 视为未知 → 放行。
| R35 | `speed=fast` 且模型不支持 fast（仅 opus-5 / opus-4.8 支持） | `"speed" value "fast" is not supported for this model` |
| R36 | max_tokens 超模型输出上限（分级表见下表） | `'max_tokens': <n> > <cap> - 'max_tokens' should be smaller than or equal to <cap>` |

**R36 的 max_tokens 上限表**（源自 `gateway_anthropic_validation.go` 的 `modelMaxOutputTokens`，移植时整表照抄）：

| 模型家族 | max_tokens 上限 |
|----------|----------------|
| claude-fable-5-1 / fable-5 | 128000 |
| claude-mythos-5-1 / mythos-5 | 128000（官方明言 Mythos 5 与 Fable 5 共享规格） |
| claude-opus-5 | 128000 |
| claude-opus-5-5 | 128000（2026-09-25 文档快照） |
| claude-sonnet-5 | 128000 |
| claude-opus-4-8 | 128000 |
| claude-opus-4-7 | 128000 |
| claude-opus-4-6 | 128000 |
| claude-sonnet-4-6 | 128000 |
| claude-haiku-4-5 | 64000 |
| claude-opus-4-5 | 64000 |
| claude-sonnet-4-5 | 64000 |
| claude-mythos-preview | 放行（无公开规格页，404） |
| 未知模型 | 放行（fail-open） |

要点：
- 上限取自官方各模型页（2026-09-17 逐一核实）。
- 官方 "128K" 是十进制 **128000**，**不是** 128×1024=131072（实测 128001 官方 400）。
- 匹配走 `normalizeThinkingModelFamily`（同 §3 归一化），表外一律 fail-open。
- 300k batch beta（`output-300k-2026-03-24`）只作用于 Message Batches API 的 Opus 5/Sonnet 5/Opus 4.8/4.7/4.6/Sonnet 4.6，不适用于同步 `/v1/messages`，无需在此处理。

### 2.5 thinking 签名校验（三层，可配置）

- 作用域：**仅 /v1/messages**，count_tokens 不做。
- 遍历 `messages[]`，只看 `role == "assistant"` 且 content 为数组的消息；
  其中 `type ∈ {thinking, redacted_thinking}` 的块：
  - `signature` 缺失 / 非字符串 / 空串 → **放行**（交给既有的"缺签名预过滤 + 400 整流"链路，避免双重拦截）
  - `signature` 非空 → 依次通过三层校验（第 2/3 层**仅作用于 thinking 块**；
    redacted_thinking 的签名结构尚未取样确认，只做第 1 层，避免误杀）：

| 层 | 适用 | 检查 | 失败文案（逐字） |
|----|------|------|------------------|
| 1. base64+长度 | thinking + redacted_thinking | 合法 base64（StdEncoding，失败再试 URLEncoding）且解码后 ≥ **32 字节** | 非法 base64：`Invalid \`signature\` in \`thinking\` block`；过短：`Invalid \`signature\` in \`thinking\` block: signature is too short` |
| 2. 骨架（2aeea95bf） | **仅 thinking** | 外层是良构 protobuf（可完整消费，无 field0/越界/坏 tag），且含 field2（wire 2）且其内容本身良构；**外层 field1 版本标记非必需**（部分真签名没有，见 §2.5a 结论 5）；首字节篡改仍可拦（tag 损坏使外层解析失败或 field2 丢失） | `Invalid \`signature\` in \`thinking\` block: signature is malformed` |
| 3. 深度指纹（b0f2e38e5） | **仅 thinking** | 内层 field1（元数据容器）≥ **128 字节**且含 ASCII `"thinking"` 且含标准 UUID；内层 field5（密文主体）≥ **256 字节**；请求模型非空时元数据中的模型名必须与请求模型一致（大小写不敏感，容忍数字/`-`/`_` 边界粘连，如 "claude-opus-58"）——拦跨模型重放 | 同上 `...: signature is malformed` |

- 外层包装：`messages.<i>.content.<j>: <上述文案>`
- 设计要点（移植时务必保留）：
  - **纯结构校验**，不做密码学验签（网关没有 Anthropic 私钥）；
  - 对"上游每轮签发新签名"天然免疫（不比历史值，只看格式）；
  - 背景事实：windf.new-api.ai 与 new-api.ai 两条 Opus 5 链路实测**都不校验签名内容**，坏签名照收 200。
- 配置开关：settings 键 `ThinkingSignatureValidation`（new-api `setting/anthropic.go`；对应 sub2api 的 `thinking_signature_validation`），**默认开**（缺失/查询出错均按开处理，fail-open 到"校验开"），仅显式 `"false"` 关闭。

### 2.5a thinking 签名结构解剖（逆向事实，2026-09-21 五份真实样本实测）

签名 base64 解码后是嵌套 protobuf。以 claude-opus-5 真签名（解码 4546B）为例：

```
outer
├─ field1  varint = 2            版本标记（★ 非恒定：sonnet-5 / 早期 opus-4-8
│                                 样本外层直接从 field2 起，无此字段）
├─ field2  bytes = inner         内层容器（占绝大部分）
│   ├─ field1  bytes(~168)       元数据容器（明文区）：
│   │                             模型名 ASCII（如 "claude-opus-5"，常与相邻
│   │                             字节的 0x38 粘连显示为 "...58"）
│   │                             + "thinking" 常量
│   │                             + 每次签发变化的标准 UUID（Z$<uuid>r 包裹）
│   ├─ field2  bytes(12)         低熵短块：LE64 低 32 位 ≈ unix 秒（签发时间戳①）
│   ├─ field3  bytes(12)         低熵短块：签发时间戳②
│   ├─ field4  bytes(48)         中熵块（疑似摘要/MAC 分量；恰为 SHA-384 长度
│   │                             但非任何明文字段的裸哈希 → 掺了密钥）
│   └─ field5  bytes(1.1k~4.3k)  密文主体（C2 篡改靶区）：高熵、长度 ∝
│                                 thinking 文本长度（空 thinking 也有 ~1.1k
│                                 基线 → 含 nonce），形态符合 AEAD（密文+标签一体）
└─ field3  varint = 1            尾随标记
```

实测结论（`_tmp_sigdump/cryptcheck` + `analyze_live_sig.py`）：

1. **无明文哈希承诺**：穷举 SHA-256/384/512（含截断、多种字段拼接顺序）对
   f2/f3/f4 全部 MISS → 各部件间不存在可离线重算的哈希关系。
2. **f2/f3 是签发时间戳**（unix 秒级），签名内嵌签发时刻。
3. **空 thinking 也有独立签名** → 签名 ≠ H(thinking)，含 nonce/时间戳/密钥。
4. **field5 是 AEAD 密文**：无 Anthropic 服务端密钥不可解密/重算，暴力空间
   2¹²⁸ 级别，物理不可行；密钥候选（模型名/常量/UUID/时间戳的各种派生）
   均已排除。
5. **版本标记非恒定**：不同模型/时期外层有无 field1 不一（曾因此误杀
   sonnet-5 真签名，2026-09-21 修复，见变更日志）。

篡改分类与拦截层对应关系：

| 篡改位置 | 用例 | 结构+深度指纹 | 发放注册表 |
|----------|------|--------------|-----------|
| 头部/tag 区（首字节等） | C3 | ✅ `malformed` | 不需要 |
| 密文主体 field5 内部 | C2 | ❌ 看不见 | ✅ `not recognized` |
| 换绑（真签名配篡改文本） | — | ❌ | ✅ 配对指纹 `not recognized` |
| 跨模型重放 | — | ✅ 深度指纹模型名比对 | — |

### 2.5b thinking 签名发放注册表（2026-09-21，可配置，默认关）

§2.5 的结构+深度指纹校验对"密文主体内部单字符篡改"无效（C2 用例长期 200 的原因）。
密码学解剖（`_tmp_sigdump/cryptcheck`，2026-09-21 三份真实样本实测）确认：签名是
**密钥化 MAC**——穷举 SHA-256/384/512（含截断、多种字段拼接）均无明文哈希承诺；
内层 field2/field3 是签发时间戳（LE64≈unix 秒）；空 thinking 也有独立签名
（含 nonce）。**无 Anthropic 私钥不可能做真正的密码学验签。**

注册表用"只认自己见过的签名"补齐这一环：

- **登记侧**（`relay/channel/claude/relay-claude.go::registerClaudeSignatures`，由
  `HandleClaudeResponseData`（非流式）与 `HandleStreamResponseData`（流式）两处调用）：
  上游 200 响应中出现 thinking/redacted_thinking 签名即登记。收割来源：
  非流式 `Content[]` 块；流式 `signature_delta` 事件（`Delta` 携带完整签名）；
  兜底 `content_block_start` 的 `ContentBlock`。键 = `(UserId, 模型名)`，
  模型名取 `info.OriginModelName`（客户端视角，与请求侧校验口径一致），
  为空时回退上游模型名。
- **配对绑定**：Anthropic 的签名覆盖 thinking 内容本身，因此注册表对
  thinking 块不按裸签名而是按**配对指纹**记账：
  `SignaturePairFingerprint(sig, thinking) = hex(sha256(sig ‖ "\x00" ‖ thinking))`
  （`relay/helper/anthropic_signature_registry.go`）。流式场景 thinking 文本散在
  多个 `thinking_delta` 事件里，按块索引用 gin context 累积器
  （`appendStreamThinking`/`accumulateStreamThinking`）拼到 `signature_delta`
  到达时再算指纹。`redacted_thinking` 无可见文本，仍按裸签名记账。
- **校验侧**（`ValidateThinkingSignaturesFull`）：结构校验通过后，若开关开启，
  要求签名命中注册表，否则 400 `Invalid \`signature\` in \`thinking\` block:
  signature not recognized`。thinking 块同样按配对指纹查询——拿到真签名
  但换了 thinking 文本（换绑攻击）也会因指纹不匹配而被拒。
- **冷启动宽限**：全局"热标记"（`anthropic:sigreg:warm`，24h TTL，每次登记刷新）。
  标记不存在或已过期（从未登记、或 24h 内无任何登记）→ 冷态 → 成员检查 fail-open，
  只做结构校验；进入热态后，一切未命中（含其他用户/模型的空桶）都是拒绝——
  堵住跨用户重放。
- **存储**：优先 Redis（`anthropic:sigreg:<uid>:<model>` hash + 24h TTL +
  `anthropic:sigreg:warm` 热标记）；未启用 Redis 降级进程内 map（桶上限
  4096、每桶签名上限 512）。存储异常只记日志，不影响转发。
- 配置开关：settings 键 `ThinkingSignatureRegistry`，**默认关**（仅显式
  `"true"` 开启）——它是增强校验，开启前需确认客户端签名均来自本网关转发的
  上游响应。
- 拦截能力对比：结构校验拦"格式坏/元数据不符/跨模型重放"；注册表在其上
  追加拦"单字符篡改、跨用户重放、凭空捏造"；配对绑定再追加拦"真签名配篡改
  文本"。三者叠加即为网关侧完整验签闭环。残余理论缺口：能实时观察到用户
  上游响应的攻击者可回放真实的 (签名, 文本) 对——等价于合法持有该会话。

### 2.6 错误响应格式对齐

`/v1/messages` 的所有错误响应（不止校验错误）统一经 `controller/relay.go::writeClaudeRelayError` 写出：

```json
{
  "type": "error",
  "error": { "type": "invalid_request_error", "message": "..." },
  "request_id": "req_<内部requestID>"
}
```

- body 顶层 `request_id` = `"req_" + 内部 request id`；响应头 `request-id` 与之**同源同值**（404 写出器 `abortWithAnthropicNotFoundMessage` 同样如此）
- **无 `code` 字段**（new-api 写出器只输出 type/message；sub2api 参考实现带 code，官方 Error shapes 并不要求）
- 413（请求体超限）的 error.type = **`request_too_large`**（官方类型）
- 官方校验错误（哨兵 `ErrAnthropicValidation` 标记）：直接用 §2 逐字文案，**跳过敏感信息掩码与 "(request id: xxx)" 后缀追加**，保证逐字一致
- **错误类型归一化**（`relaykit/types/error.go::ToClaudeError`，2aeea95bf）：Claude 路径出口的任何错误，其 type 都被强制落入官方集合——已是官方类型则原样保留（真上游 Anthropic 错误透传），否则按状态码推导，防止内部类型名（`new_api_error`、`openai_error`）或 `"<nil>"` 泄漏给客户端：

| 状态码 | error.type |
|--------|------------|
| 400 | invalid_request_error |
| 401 | authentication_error |
| 403 | permission_error |
| 402 | billing_error |
| 404 | not_found_error |
| 409 | conflict_error |
| 413 | request_too_large |
| 422 | unprocessable_entity_error |
| 429 | rate_limit_error |
| 529 | overloaded_error |
| 504 | timeout_error |
| 其他 | api_error |

### 2.7 404 模型不可用统一为 `not_found_error`

**实现口径（2026-09-18 定稿）**：`/v1/messages` 路径（含 `/v1/messages/count_tokens`，由 `middleware/distributor.go::isAnthropicMessagesPath` 判定）上，渠道选择失败的**两种形态**一律返回官方 404 通用报文，不再区分 503：

| 形态 | 触发点（`middleware/distributor.go`） | 报文 |
|------|--------|------|
| 渠道查找出错 | `CacheGetRandomSatisfiedChannel` 返回 err（含 auto 分组展开全部候选分组后仍无该模型） | 404 `not_found_error`，`The requested resource could not be found.` |
| 找到分组但无可用渠道（nil） | 涵盖"无能力行"与"有能力行但渠道不可用"（伪造快照） | 同上 |

- 写出器：`middleware/utils.go::abortWithAnthropicNotFoundMessage`（404 + `not_found_error` + `req_` request_id + `request-id` 响应头）
- 报文依据：platform.claude.com/docs/en/api/errors "Error shapes" 标准 404 报文（2026-09-18 核对）；sub2api 参考实现的 `Model %q is not available for this group` / `Model %q is not supported by any configured account in this group` 已废弃
- 例外保留：OpenAI 原生入口（GET `/v1/models` 单模型查询、白名单中间件的 OpenAI 路径）仍是 `code: "model_not_found"`（OpenAI 惯例，不属于 Anthropic 对齐范围）
- 上游识别逻辑（解析第三方上游返回的 `model_not_found`）不动——那是识别别人的报文

### 2.8 原始字节透传（501dce4db）

问题：new-api 非透传模式会把请求经 `dto.ClaudeRequest` 结构体 round-trip，键序被重排（`max_tokens,messages,model` → `model,messages,max_tokens`；message 内 `content,role` → `role,content`），令按请求字节指纹应答的上游（e2e mock、部分观测系统）收到与 sub2api（原生 anthropic 转发原始字节、仅定向重写 model）不同的字节。

修法（`relay/claude_handler.go`）：
- `anthropicForwardOriginalBody(c, transformed)`：当重构后的 JSON 与客户端原始字节**语义等价**时，转发原始字节（保留键序与结构体未建模的字段，如 `seed`）；
- `jsonSemanticallyEqual(a, b)`：两侧各自解析后规范化序列化比较（忽略键序/空白/数字格式）；
- 一旦发生实质改写（模型映射、max_tokens 默认注入、thinking/effort 变形、SystemPrompt 注入、param override、字段裁剪、存在未知字段），二者必然不等价 → 回退重构字节，行为与基线一致。
- 回归：`relay/claude_original_body_test.go`。

### 2.9 image 块校验（2026-09-26，`relay/helper/anthropic_image_validation.go`）

依据官方 Vision 指南（platform.claude.com/docs/en/build-with-claude/vision，2026-09-26 核对）。
作用域：`/v1/messages` 与 `count_tokens` 两入口（随 `ValidateAnthropicRequest` 无条件生效，
与其余结构类规则一致）；对全部模型生效（图片限额与模型无关，无需 fail-open 门控）。

**收集范围**：遍历 `messages[].content[]` 中 `type=="image"` 的块，另递归收集
`tool_result.content[]` 内嵌的 image 块（官方明言其计入多图阈值）。位置路径用官方点分格式
（如 `messages.0.content.1`、`messages.0.content.2.content.0`）。

**规则与文案**：

| # | 规则 | 错误文案（400 invalid_request_error） | 文案来源 |
|---|------|----------------------------------------|----------|
| I1 | `source` 必须是对象 | `<pos>.source: Field required` | 推断（pydantic 风格） |
| I2 | `source.type` ∈ {base64, url, file} | `<pos>.source.type: Input should be 'base64', 'url' or 'file'` | 推断 |
| I3 | base64 型必须有 `media_type`（字符串） | `<pos>.source.base64.media_type: Field required` | 推断 |
| I4 | `media_type` ∈ {image/jpeg, image/png, image/gif, image/webp} | `<pos>.source.base64.media_type: Input should be 'image/jpeg', 'image/png', 'image/gif' or 'image/webp'` | **官方逐字**（生产实测确认，pydantic Literal 风格，路径含 discriminated union 变体名） |
| I5 | base64 型必须有 `data`（字符串） | `<pos>.source.base64.data: Field required` | 推断 |
| I6 | url 型必须有非空 `url` | `<pos>.source.url.url: Field required` | 推断 |
| I7 | file 型必须有非空 `file_id` | `<pos>.source.file.file_id: Field required` | 推断 |
| I8 | base64 解码后 ≤ 10 MB（10485760 B） | `<pos>.source.base64.data: Image exceeds the maximum size of 10MB` | 待实测（官方只说 10 MB base64-encoded，未公布文案） |
| I9 | 每请求图片总数 ≤ 600 | `Too many images in request: maximum 600 images allowed, got <n>` | 待实测 |
| I10 | 单边像素 ≤ 8000 | `<pos>: Image dimensions (<w>x<h>) exceed the maximum of 8000px` | 待实测 |
| I11 | 请求内图片 > 20 张时，每张单边 ≤ 2000 | `<pos>: Image dimensions (<w>x<h>) exceed the 2000px limit for many-image requests` | 半官方（官方描述：message references "many-image requests" and states the current limit in pixels；措辞按其描述构造） |

**实现要点**：

- 像素校验只解析图片头（PNG IHDR / JPEG SOF / GIF logical screen / WebP VP8·VP8L·VP8X），
  不解码像素；头部解析失败（含非法 base64、未知格式）一律放行（fail-open）。
- 10 MB 上限按 base64 字符串长度估算解码字节数（`len*3/4` 减 padding），不实际解码；
  像素检查时才做真正的 `base64.StdEncoding`（失败再试 `URLEncoding`）解码。
- 200k 上下文模型的 100 张细分**不做**（fail-open 到 600）：需要维护易过期的
  模型→上下文窗口表，收益低；Bedrock/GCP 的 5 MB 细分同理不做（按较宽的 10 MB 校验）。
- 校验顺序：先收集全部 image 块 → 数量检查 → 逐块结构/媒体类型/大小/像素检查，
  返回第一个违规。
- 挂载点：`ValidateAnthropicRequest` 的 messages 循环之后、temperature 之前。

**回归测试**（`anthropic_messages_validation_test.go`）：`TestValidateAnthropicRequest_Image*
` 9 组（三种 source 合法、media_type 逐字文案、8 种结构缺失、10MB 边界、600/601 张边界、
8000/8001 像素边界、20/21 张 many 模式切换、tool_result 内嵌计数、坏头 fail-open）+
`TestDecodeImageDimensions`（四格式头部解析器表驱动）。

---

## 3. 模型家族归一化

基础归一化（new-api `normalizeThinkingModelFamily`，用于 thinking.type 矩阵 / 采样参数 /
max_tokens 上限 / prefill / tool_choice / fast mode 各表）：

```
1. lowercase(model)
2. 若末尾是 "-<8位数字>"（日期后缀，如 -20251101），剥掉
3. 若末尾是 "-thinking"，剥掉
```

effort 分级表用更强的归一化（`normalizeEffortModelID`）：在上述基础上再去
`models/` 前缀与 `anthropic.` 前缀。

得到 family key 后查各张表。**注意陷阱**：`opus-5` 匹配必须先于裸 `5`，
否则 `claude-opus-4-5` 会被误判成 Opus 5。

---

## 4. 接线点（new-api 已实现）

路由层（`router/relay-router.go`）：`POST /v1/messages` 与 `POST /v1/messages/count_tokens`
都汇入 `controller.Relay(c, types.RelayFormatClaude)`——**单一挂载点覆盖两条入口**
（等效于 sub2api 的双入口挂载要求 e62b16903）。

`controller/relay.go::Relay` 内的挂载顺序（读完 body 之后、channel 选择之前）：

```
Relay(c, RelayFormatClaude)
  → helper.ValidateClaudeMessagesRequest(c)                      // 统一校验钩子
      → 读 body（common.GetRequestBody，读完 Seek 回 0，下游可再消费）
      → isCountTokens := strings.HasSuffix(path, "/count_tokens")
      → ValidateAnthropicRequest(body, !isCountTokens, c.GetHeader("anthropic-beta"))
      → if !isCountTokens && setting.ShouldValidateThinkingSignatures():
            ValidateThinkingSignaturesFull(body, userId)          // 结构+深度指纹（+注册表若开）
      → 失败包 WrapAnthropicValidationError（哨兵标记）
  → 错误映射：body 超限 → 413（request_too_large）；其余校验失败 → 400（invalid_request_error）
  → GetAndValidateRequest / GenRelayInfo / Distribute（渠道选择，§2.7 404）
  → 转发（原始字节透传判定，§2.8）
  → defer: 出错时 writeClaudeRelayError（§2.6）
```

关键实现细节：
- 哨兵错误 `ErrAnthropicValidation` 标记"文案已是官方逐字"：defer 分支跳过 `MessageWithRequestId` 的后缀追加，`writeClaudeRelayError` 跳过敏感信息掩码；
- 登记侧（注册表用）：`relay/channel/claude/relay-claude.go` 的 `HandleClaudeResponseData`（非流式）与 `HandleStreamResponseData`（流式）均调用 `registerClaudeSignatures`；
- count_tokens 上游转发：`relay/channel/claude/adaptor.go::GetRequestURL` 检测 `/count_tokens` 后缀并追加到上游路径。

---

## 5. 常量（new-api 实际值）

```go
// relay/helper/anthropic_messages_validation.go
BetaThinkingDisplayUpdates     = "thinking-display-updates-2026-08-18"
BetaInterleavedThinking        = "interleaved-thinking-2025-05-14"
thinkingSignatureMinDecodedLen = 32   // §2.5 第 1 层：解码最小字节数
thinkingSigInnerMetaMinLen     = 128  // §2.5 第 3 层：内层 field1 元数据最小字节数
thinkingSigInnerBlobMinLen     = 256  // §2.5 第 3 层：内层 field5 密文主体最小字节数

// relay/helper/anthropic_image_validation.go（§2.9）
imageMaxDecodedBytes    = 10 * 1024 * 1024 // base64 解码后单图上限（Claude API 直连）
imageMaxDimensionPx     = 8000             // 常规请求单边像素上限
imageManyThreshold      = 20               // 触发收紧限制的请求内图片数阈值
imageManyMaxDimensionPx = 2000             // 多图请求单边像素上限
imageMaxCount           = 600              // 每请求图片数上限（200k 模型 100 张细分交上游）

// setting/anthropic.go
SettingKeyThinkingSignatureValidation = "ThinkingSignatureValidation" // 默认开（显式 "false" 关）
SettingKeyThinkingSignatureRegistry   = "ThinkingSignatureRegistry"   // 默认关（显式 "true" 开）
```

---

## 6. 回归验证方法

探针脚本 `test_claude5_mode_probe.sh`（已提交至仓库根目录，330 行）：12 个用例打
`${BASE}/v1/messages`，期望值按官方 Opus 5 矩阵填写：

| 用例 | 参数 | 期望 |
|------|------|------|
| C1 | 无 thinking | 200 |
| C2 | adaptive | 200 |
| C3 | adaptive+display=summarized | 200 |
| C4 | adaptive+effort=high | 200 |
| C5 | enabled+budget=1024 | 400 |
| C6 | disabled（无 effort） | 200 |
| C7 | disabled+effort=max | 400 |
| C8 | enabled+budget=1024+effort=high | 400 |
| C9 | thinking.type=\_\_bogus\_\_ | 400 |
| C10 | effort=\_\_bogus\_\_（阳性对照） | 400 |
| C11 | enabled 缺 budget_tokens | 400 |
| C12 | budget 8192 > max 4096 | 400 |

用法：`bash test_claude5_mode_probe.sh <BASE> <KEY> [MODEL] [REPS]`，依赖 curl + jq。
对 new-api 改完后，把 BASE 指向 new-api 实例跑一遍，12/12 全绿即对齐完成。
另加一组签名用例：把请求体里 assistant thinking 块的 signature 改成 `!!!not-base64!!!`
应得 400 `Invalid \`signature\` in \`thinking\` block`。

Go 回归套件（new-api，`go test ./relay/helper/... ./relay/... ./relaykit/types/...`）：
`anthropic_messages_validation_test.go`（R1–R36 + 采样家族矩阵 + §2.9 image 块 9 组）、
`anthropic_signature_registry_test.go`（篡改/跨用户/换绑拒绝）、
`signature_harvest_test.go`（登记侧收割）、`claude_original_body_test.go`（透传决策）、
`error_nil_test.go`（错误类型归一化）。

**max_output 三枪**（R36 专用，2026-09-17 已在生产节点实测全绿）：

| 请求 `max_tokens`（claude-fable-5-1） | 期望 |
|------|------|
| 128001 | 400 `'max_tokens': 128001 > 128000 - 'max_tokens' should be smaller than or equal to 128000` |
| 131073 | 400 同上（旧版二进制在这里才报 131072，可用来分辨部署版本新旧） |
| 128000 | 200 正常出流 |

排障经验：若 128001 被放行，先确认 ① 探测目标确实是跑了本分支二进制的实例（注意：new-api 移植版错误响应也已带 `req_` 前缀 request_id 且 error.type 已归一化为官方类型，不能再靠这两个特征区分 sub2api / new-api；改用 `GET /` 的版本号或 131073 探测分辨）；② 请求里的 model ID 是否在 R36 表内（表外 fail-open）；③ 部署的是否为新二进制（用 131073 探测：报 131072 = 旧版）。

---

## 7. 移植注意事项（踩过的坑）

1. **文案逐字**：客户端（Claude Code 等）和部分上游按错误文案分支处理，标点/反引号都不能差。
2. **Fable/Mythos 5.x 的 disabled**：官方文档与实测矛盾（文档 400，实测 200）。参考实现曾按实测放宽；new-api 移植版 2026-09-18 起按官方文档收紧为 400，代码注释已写明依据。
3. **prefill 判定**：只有"最后一条 assistant 消息含**文本**"才算 prefill；只含 tool_use 等结构化块的不算，放行给上游。
4. **未知模型一律放行**（thinking.type、max_tokens 上限、fast mode 之外的未知项），宁可漏拦不可误杀。
5. **count_tokens 豁免**：max_tokens 必填、budget<max 比较、签名校验三项都不适用。
6. **count_tokens 渠道路径归一**（4db39d2c5）：type-58 高级自定义渠道按入站路径精确相等匹配（`matchAdvancedCustomIncomingPath`），`/v1/messages/count_tokens` 无法匹配配置为 `/v1/messages` 的渠道 → 无满足渠道 → 404。修法：`middleware/distributor.go` 在渠道路径匹配前将 `/count_tokens` 后缀剥离（`channelMatchPath`），使 count_tokens 复用同一 Claude Messages 渠道；上游转发仍走完整路径（`relay/channel/claude/adaptor.go` 的 `GetRequestURL` 检测后缀追加）。
7. **beta 头解析**：`anthropic-beta` 是逗号分隔列表，逐项 trim 后精确匹配。
8. **签名校验与既有整流链路不冲突**：空/缺签名不归它管（预过滤 + 400 后整流专管），它只管"非空但格式坏"。
9. 探针脚本里目前硬编码了一个 sk- key，对外分享前先挪回环境变量。
10. **哨兵错误机制**：校验失败要包 `WrapAnthropicValidationError`（哨兵 `ErrAnthropicValidation`）；错误写出路径识别哨兵后跳过 "(request id: xxx)" 后缀追加与敏感信息掩码，否则逐字文案会被破坏。
11. **注册表键的模型名两侧必须一致**：登记侧用 `OriginModelName`（空则回退上游模型名），请求侧校验用请求体顶层 `model`；不一致会导致合法签名未命中被误杀。
12. **redacted_thinking 只做第 1 层**：其签名结构尚未取样确认，骨架/深度指纹层跳过以免误杀。
13. **base64 容忍边界**：只接受 StdEncoding + URLEncoding（带 padding）；RawStd/RawURL（无 padding）曾尝试并已回滚（`78060f736`/`167e58b80`），勿再引入。
