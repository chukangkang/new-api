# Anthropic `/v1/messages` 官方对齐校验 —— 移植规格书

> 本文档汇总 `anthropic-api-style-v0.2.4` 分支（基于 main `bdb42e22f`）的全部已提交改动，
> 目的是把这些校验规则**移植到 new-api 源码**中。每条规则都给出了触发条件、
> 官方逐字错误文案、例外情况，可直接照抄实现。
>
> 源实现位置（sub2api）：
> - `backend/internal/handler/gateway_anthropic_validation.go`（核心校验，约 550 行）
> - `backend/internal/handler/gateway_handler.go`（接线点）
> - `backend/internal/service/setting_features.go` + `domain_constants.go`（配置开关）
> - `backend/internal/pkg/claude/constants.go`（beta 常量）
>
> 测试参考：`backend/internal/handler/gateway_anthropic_validation_test.go`（约 870 行，
> 全部用例可直接搬去做回归）。

---

## 1. 提交清单（时间序）

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

合计：20 文件，+1758 / -31。

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
| R10 | `messages[i].role` ∉ {user, assistant} | `"messages[i].role" must be one of: "user", "assistant"` |
| R11 | `messages[i].content` 缺失 | `"messages[i].content" is a required property` |

### 2.2 采样参数（分家族）

先算 `family = normalizeModelFamily(model)`（见 §3）。
`rejectsSampling = family ∈ {opus-4-7, opus-4-8, opus-5, sonnet-5, fable-5, fable-5-1, mythos-5, mythos-5-1, mythos-preview}`

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
| R29 | 4.6+ 家族（opus-4-6/sonnet-4-6/opus-4-7/opus-4-8/opus-5/sonnet-5/fable-5/fable-5-1/mythos-5/mythos-5-1/mythos-preview）且**最后一条消息是带文本内容的 assistant**（prefill） | `This model does not support assistant message prefill. The conversation must end with a user message.` |
| R30 | fable-5-1 / mythos-5-1 且 `tool_choice.type` ∈ {tool, any} | `tool_choice: type "tool" and "any" are not supported for this model.` |
| R31 | `output_config.effort` 非字符串 | `"output_config.effort" must be a string` |
| R32 | effort ∉ {low, medium, high, xhigh, max} | `"output_config.effort" must be one of: "low", "medium", "high", "xhigh", "max"` |
| R33 | effort 不在该模型支持级别内（分级表见下表） | `"output_config.effort" value "<v>" is not supported for this model` |
| R34 | thinking.type=disabled 且 effort ∈ {xhigh, max} | `"thinking.type" value "disabled" is not supported with "output_config.effort" value "<effort>" for this model` |

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

### 2.5 thinking 签名结构校验（e62c69440，可配置）

- 作用域：**仅 /v1/messages**，count_tokens 不做。
- 遍历 `messages[]`，只看 `role == "assistant"` 且 content 为数组的消息；
  其中 `type ∈ {thinking, redacted_thinking}` 的块：
  - `signature` 缺失 / 非字符串 / 空串 → **放行**（交给既有的"缺签名预过滤 + 400 整流"链路，避免双重拦截）
  - `signature` 非空 → 必须满足：
    1. 是合法 base64（StdEncoding，失败再试 URLEncoding）
       否则：`Invalid \`signature\` in \`thinking\` block: signature is not valid base64`
    2. 解码后 ≥ **32 字节**
       否则：`Invalid \`signature\` in \`thinking\` block: signature is too short`
  - 外层包装：`messages.<i>.content.<j>: <上述文案>`
- 设计要点（移植时务必保留）：
  - **纯结构校验**，不做密码学验签（网关没有 Anthropic 私钥）；
  - 对"上游每轮签发新签名"天然免疫（不比历史值，只看格式）；
  - 背景事实：windf.new-api.ai 与 new-api.ai 两条 Opus 5 链路实测**都不校验签名内容**，坏签名照收 200。
- 配置开关：settings 键 `thinking_signature_validation`，**默认开**（缺失/查询出错均按开处理，fail-open 到"校验开"），仅显式 `"false"` 关闭。

### 2.6 错误响应格式对齐（1c6a2a8d5）

所有错误响应（不止校验错误）统一：

```json
{
  "type": "error",
  "error": { "type": "invalid_request_error", "message": "...", "code": "..." },
  "request_id": "req_<内部requestID>"
}
```

- body 顶层 `request_id` = `"req_" + 内部 request id`
- 响应头 `request-id` 与 body 字段**同源同值**
- 413（请求体超限）的 error.type 从 `invalid_request_error` 改为 **`request_too_large`**（官方类型）

### 2.7 404 模型不可用统一为 `not_found_error`（3a1038097）

两种 404 场景的线上错误类型现已完全一致，均为官方 Anthropic 404 类型 `not_found_error`：

| 场景 | 触发点 | 报文 |
|------|--------|------|
| 模型不在组白名单 | `GroupModelAllowlist` 中间件（`/messages` 路径走 `AnthropicErrorWriter`） | 404 `not_found_error`，`The requested resource could not be found.`（官方 Error shapes 标准 404 报文，2026-09-18 按 platform.claude.com/docs/en/api/errors 核对；参考实现的 `Model %q is not available for this group` 已废弃） |
| 池内有账号但都不支持该模型（伪造快照） | `classifyNoAccountError`（`no_account_error.go`，单点分类器，所有入口透传） | 404 `not_found_error`，`Model %q is not supported by any configured account in this group` |

移植要点：
- 分类器内部保留 `ModelNotFound` 布尔标志（ops 归因：routing/platform/local-model-config 阶段判定、不被 429 限流改判），**只改线上 ErrType 字符串**。
- 例外保留：OpenAI 原生入口（GET `/v1/models` 单模型查询、白名单中间件的 OpenAI 路径）仍是 `code: "model_not_found"`（OpenAI 惯例，不属于 Anthropic 对齐范围）。
- 上游识别逻辑（解析第三方上游返回的 `model_not_found`）不动——那是识别别人的报文。

---

## 3. 模型家族归一化（normalizeModelFamily）

```
1. lowercase(trim(model))
2. 若末尾是 "-<8位数字>"（日期后缀，如 -20251101），剥掉
3. 若末尾是 "-thinking"，剥掉
```

得到 family key 后查各张表。**注意陷阱**：`opus-5` 匹配必须先于裸 `5`，
否则 `claude-opus-4-5` 会被误判成 Opus 5。

---

## 4. 接线点（在 new-api 中对应的挂载位置）

sub2api 中的挂载顺序（`Messages` handler，读完 body 之后、路由分发之前）：

```
readBody
  → validateAnthropicRequest(body, requireMaxTokens=true, betaHeader)   // 400 invalid_request_error
  → if 签名校验开关开: validateThinkingSignatures(body)                  // 400 invalid_request_error
  → 后续鉴权/路由/转发
```

**两条 `/v1/messages` 入口都要挂**（e62b16903）：
- 原生 Anthropic 组：`GatewayHandler.Messages`（`gateway_handler.go` L217）
- OpenAI-Responses 兼容组（桥接路径）：`OpenAIGatewayHandler.Messages`（`openai_gateway_handler.go`）

CountTokens handler 同样挂 `validateAnthropicRequest(body, false, betaHeader)`，**不挂**签名校验。

new-api 对应位置建议：`controller/relay`（或 `relay/controller`）中处理
`POST /v1/messages` 的入口函数，在读完并解析 body 之后、channel 选择之前插入；
`/v1/messages/count_tokens` 同理。错误返回沿用 new-api 现有的
`abortWithMessage` / `NewOpenAIError` 通道，但 **message 文案必须用 §2 的逐字文案**，
error type 用 `invalid_request_error`。

---

## 5. 需要新增的常量

```go
BetaThinkingDisplayUpdates = "thinking-display-updates-2026-08-18" // 已有 BetaInterleavedThinking = "interleaved-thinking-2025-05-14" 可复用
SettingKeyThinkingSignatureValidation = "thinking_signature_validation"
thinkingSignatureMinDecodedLen = 32
```

---

## 6. 回归验证方法

探针脚本 `test_claude5_mode_probe.sh`（仓库根目录，被 `.gitignore` 的 `/test*` 规则忽略，需随文档一起单独拷贝）：12 个用例打
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
应得 400 `Invalid \`signature\` in \`thinking\` block: signature is not valid base64`。

**max_output 三枪**（R36 专用，2026-09-17 已在生产节点实测全绿）：

| 请求 `max_tokens`（claude-fable-5-1） | 期望 |
|------|------|
| 128001 | 400 `'max_tokens': 128001 > 128000 - 'max_tokens' should be smaller than or equal to 128000` |
| 131073 | 400 同上（旧版二进制在这里才报 131072，可用来分辨部署版本新旧） |
| 128000 | 200 正常出流 |

排障经验：若 128001 被放行，先确认 ① 探测目标确实是跑了本分支二进制的实例（坏 body 指纹：`Failed to parse request body` + 顶层 `req_` 前缀 request_id = sub2api；`new_api_error` = New-API 中继）；② 请求里的 model ID 是否在 R36 表内（表外 fail-open）；③ 部署的是否为新二进制（用 131073 探测：报 131072 = 旧版）。

---

## 7. 移植注意事项（踩过的坑）

1. **文案逐字**：客户端（Claude Code 等）和部分上游按错误文案分支处理，标点/反引号都不能差。
2. **Fable/Mythos 5.x 的 disabled**：官方文档与实测矛盾（文档 400，实测 200）。参考实现曾按实测放宽；new-api 移植版 2026-09-18 起按官方文档收紧为 400，代码注释已写明依据。
3. **prefill 判定**：只有"最后一条 assistant 消息含**文本**"才算 prefill；只含 tool_use 等结构化块的不算，放行给上游。
4. **未知模型一律放行**（thinking.type、max_tokens 上限、fast mode 之外的未知项），宁可漏拦不可误杀。
5. **count_tokens 豁免**：max_tokens 必填、budget<max 比较、签名校验三项都不适用。
6. **beta 头解析**：`anthropic-beta` 是逗号分隔列表，逐项 trim 后精确匹配。
7. **签名校验与既有整流链路不冲突**：空/缺签名不归它管（预过滤 + 400 后整流专管），它只管"非空但格式坏"。
8. 探针脚本里目前硬编码了一个 sk- key，对外分享前先挪回环境变量。
