package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/reasoning"

	"github.com/gin-gonic/gin"
)

func ClaudeHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {

	info.InitChannelMeta(c)

	claudeReq, ok := info.Request.(*dto.ClaudeRequest)

	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected *dto.ClaudeRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(claudeReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to ClaudeRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	if request.MaxTokens == nil || *request.MaxTokens == 0 {
		defaultMaxTokens := uint(model_setting.GetClaudeSettings().GetDefaultMaxTokens(request.Model))
		request.MaxTokens = &defaultMaxTokens
	}

	if baseModel, effortLevel, ok := reasoning.TrimEffortSuffix(request.Model); ok && effortLevel != "" &&
		(strings.HasPrefix(request.Model, "claude-opus-4-6") ||
			strings.HasPrefix(request.Model, "claude-opus-4-7") ||
			strings.HasPrefix(request.Model, "claude-opus-4-8")) {
		request.Model = baseModel
		request.Thinking = &dto.Thinking{
			Type: "adaptive",
		}
		request.OutputConfig = json.RawMessage(fmt.Sprintf(`{"effort":"%s"}`, effortLevel))
		if strings.HasPrefix(request.Model, "claude-opus-4-7") ||
			strings.HasPrefix(request.Model, "claude-opus-4-8") {
			// Opus 4.7/4.8 reject non-default temperature/top_p/top_k with 400
			// and defaults display to "omitted"; restore the 4.6 visible summary.
			request.Thinking.Display = "summarized"
			request.Temperature = nil
			request.TopP = nil
			request.TopK = nil
		} else {
			request.Temperature = common.GetPointer[float64](1.0)
		}
		info.UpstreamModelName = request.Model
	} else if model_setting.GetClaudeSettings().ThinkingAdapterEnabled &&
		strings.HasSuffix(request.Model, "-thinking") {
		if request.Thinking == nil {
			baseModel := strings.TrimSuffix(request.Model, "-thinking")
			if strings.HasPrefix(baseModel, "claude-opus-4-7") ||
				strings.HasPrefix(baseModel, "claude-opus-4-8") {
				// Opus 4.7/4.8 reject thinking.type="enabled"; use adaptive at high effort.
				request.Thinking = &dto.Thinking{Type: "adaptive", Display: "summarized"}
				request.OutputConfig = json.RawMessage(`{"effort":"high"}`)
				request.Temperature = nil
				request.TopP = nil
				request.TopK = nil
			} else {
				// 因为BudgetTokens 必须大于1024
				if request.MaxTokens == nil || *request.MaxTokens < 1280 {
					request.MaxTokens = common.GetPointer[uint](1280)
				}

				// BudgetTokens 为 max_tokens 的 80%
				request.Thinking = &dto.Thinking{
					Type:         "enabled",
					BudgetTokens: common.GetPointer[int](int(float64(*request.MaxTokens) * model_setting.GetClaudeSettings().ThinkingAdapterBudgetTokensPercentage)),
				}
				// TODO: 临时处理
				// https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking#important-considerations-when-using-extended-thinking
				request.Temperature = common.GetPointer[float64](1.0)
			}
		}
		if !model_setting.ShouldPreserveThinkingSuffix(info.OriginModelName) {
			request.Model = strings.TrimSuffix(request.Model, "-thinking")
		}
		info.UpstreamModelName = request.Model
	}

	if info.ChannelSetting.SystemPrompt != "" {
		if request.System == nil {
			request.SetStringSystem(info.ChannelSetting.SystemPrompt)
		} else if info.ChannelSetting.SystemPromptOverride {
			common.SetContextKey(c, constant.ContextKeySystemPromptOverride, true)
			if request.IsStringSystem() {
				existing := strings.TrimSpace(request.GetStringSystem())
				if existing == "" {
					request.SetStringSystem(info.ChannelSetting.SystemPrompt)
				} else {
					request.SetStringSystem(info.ChannelSetting.SystemPrompt + "\n" + existing)
				}
			} else {
				systemContents := request.ParseSystem()
				newSystem := dto.ClaudeMediaMessage{Type: dto.ContentTypeText}
				newSystem.SetText(info.ChannelSetting.SystemPrompt)
				if len(systemContents) == 0 {
					request.System = []dto.ClaudeMediaMessage{newSystem}
				} else {
					request.System = append([]dto.ClaudeMediaMessage{newSystem}, systemContents...)
				}
			}
		}
	}

	if !model_setting.GetGlobalSettings().PassThroughRequestEnabled &&
		!info.ChannelSetting.PassThroughBodyEnabled &&
		service.ShouldChatCompletionsUseResponsesGlobal(info.ChannelId, info.ChannelType, info.OriginModelName) {
		result, convErr := service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)
		if convErr != nil {
			return types.NewError(convErr, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		openAIRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
		if !ok {
			return types.NewError(fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value), types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		usage, newApiErr := chatCompletionsViaResponses(c, info, adaptor, openAIRequest)
		if newApiErr != nil {
			return newApiErr
		}

		service.PostTextConsumeQuota(c, info, usage, nil)
		return nil
	}

	var requestBody io.Reader
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		requestBody = common.NewReplayableBodyReader(storage)
	} else {
		convertedRequest, err := adaptor.ConvertClaudeRequest(c, info, request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)
		jsonData, err := common.Marshal(convertedRequest)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// remove disabled fields for Claude API
		jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// apply param override
		if len(info.ParamOverride) > 0 {
			jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
			if err != nil {
				return newAPIErrorFromParamOverride(err)
			}
		}

		// Anthropic 对齐：当网关未对请求做实质改写时（重构后的 JSON 与客户端
		// 原始字节语义等价），转发客户端原始字节而非整结构体 round-trip 的结果。
		// 后者会按结构体字段序重排键序（如 max_tokens,messages,model →
		// model,messages,max_tokens），令按请求字节指纹应答的上游（如 e2e mock）
		// 得到与参考实现不同的请求。发生实质改写（模型映射、thinking 注入、
		// param override 等）时二者必然不等价，仍走重构后的 jsonData。
		if originalBody, ok := anthropicForwardOriginalBody(c, jsonData); ok {
			logger.LogDebug(c, "requestBody (original passthrough): %s", originalBody)
			requestBody = originalBody
		} else {
			logger.LogDebug(c, "requestBody: %s", jsonData)
			body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
			if err != nil {
				return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}
			defer closer.Close()
			jsonData = nil
			requestBody = body
		}
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")
	var httpResp *http.Response
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	if resp != nil {
		httpResp = resp.(*http.Response)
		info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
		if httpResp.StatusCode != http.StatusOK {
			newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			// reset status code 重置状态码
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return newAPIError
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), nil)
	return nil
}

// anthropicForwardOriginalBody 在网关未对请求做实质改写时，返回客户端原始
// 请求体（保留键序与未被结构体建模的字段），否则返回 (nil,false)。
//
// 判断方式是语义等价：把「重构后的 JSON」与「客户端原始字节」各自解析再
// 规范化序列化后比较。只要发生过任何实质改动（模型映射、max_tokens 默认
// 注入、thinking/effort 变形、SystemPrompt 注入、param override、字段裁剪，
// 或客户端带了结构体会丢弃的未知字段），二者必然不等价，从而回退到重构
// 后的 jsonData，行为与既有基线一致。
func anthropicForwardOriginalBody(c *gin.Context, transformed []byte) (io.Reader, bool) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, false
	}
	original, err := storage.Bytes()
	if err != nil {
		return nil, false
	}
	if !jsonSemanticallyEqual(original, transformed) {
		return nil, false
	}
	return common.NewReplayableBodyReader(storage), true
}

// jsonSemanticallyEqual 判断两段 JSON 是否语义相同（忽略键序与空白差异）。
// 两侧都经 common.Unmarshal 解析为通用值后再规范化序列化，因此数字格式、
// 键顺序的差异都会被抹平；任一环节失败都保守返回 false。
func jsonSemanticallyEqual(a, b []byte) bool {
	var av, bv any
	if err := common.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := common.Unmarshal(b, &bv); err != nil {
		return false
	}
	canonicalA, err := common.Marshal(av)
	if err != nil {
		return false
	}
	canonicalB, err := common.Marshal(bv)
	if err != nil {
		return false
	}
	return bytes.Equal(canonicalA, canonicalB)
}
