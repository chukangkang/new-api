package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	codeStr := ""
	if len(code) > 0 {
		codeStr = string(code[0])
	}
	userId := c.GetInt("id")
	_, preparedPluginRoute := c.Get(pluginruntime.ContextKeyRouteRequest)
	if !preparedPluginRoute || !RespondTaskPluginError(c, &dto.TaskError{
		Code:       codeStr,
		Message:    message,
		StatusCode: statusCode,
	}) {
		c.JSON(statusCode, gin.H{
			"error": gin.H{
				"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
				"type":    "new_api_error",
				"code":    codeStr,
			},
		})
	}
	c.Abort()
	logger.LogError(c.Request.Context(), common.LogText("user %d | %s", userId, message))
}

// invalidRequestStatus is the status of a request whose body could not be
// read or parsed: 413 when the body exceeds MAX_REQUEST_BODY_MB, else 400.
func invalidRequestStatus(err error) int {
	if common.IsRequestBodyTooLargeError(err) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func abortWithMidjourneyMessage(c *gin.Context, statusCode int, code int, description string) {
	c.JSON(statusCode, gin.H{
		"description": description,
		"type":        "new_api_error",
		"code":        code,
	})
	c.Abort()
	logger.LogError(c.Request.Context(), description)
}

// abortWithAnthropicNotFoundMessage 以官方 Anthropic 404 报文格式响应
// （error.type = not_found_error，body 顶层 request_id 与响应头 request-id
// 同源同值）。仅用于 /v1/messages 路径的"模型对该分组不可用"场景；
// OpenAI 原生入口仍使用 abortWithOpenAiMessage（code: model_not_found）。
func abortWithAnthropicNotFoundMessage(c *gin.Context, message string) {
	payload := gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "not_found_error",
			"message": message,
		},
	}
	if requestID := c.GetString(common.RequestIdKey); strings.TrimSpace(requestID) != "" {
		officialID := "req_" + requestID
		payload["request_id"] = officialID
		c.Header("request-id", officialID)
	}
	c.JSON(http.StatusNotFound, payload)
	c.Abort()
	logger.LogError(c.Request.Context(), message)
}
