package types

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 回归测试：对外错误报文不允许出现 "<nil>"。
// 背景：上游（如另一台 new-api）返回的 403 错误对象没有 code 字段，
// 经 WithOpenAIError 包装后 Code 为 nil，ToClaudeError 曾用
// fmt.Sprintf("%v", Code) 输出 "<nil>" 作为 error.type。

func TestToClaudeError_NilCodeDoesNotLeakNil(t *testing.T) {
	// 模拟上游错误：有 message/type，无 code（Code 保持 nil）
	upstream := OpenAIError{
		Message: "用户额度不足, 剩余额度: 💰-0.600712 (request id: abc)",
		Type:    "new_api_error",
	}
	e := WithOpenAIError(upstream, http.StatusForbidden)

	claudeErr := e.ToClaudeError()
	require.NotContains(t, claudeErr.Type, "<nil>", "error.type must not leak <nil>")
	require.NotEmpty(t, claudeErr.Type, "error.type must not be empty")
	require.NotContains(t, claudeErr.Message, "<nil>")
}

func TestToClaudeError_TypeCodeNilPointerDoesNotLeakNil(t *testing.T) {
	// 类型化 nil：接口内包裹一个 nil 指针，Code != nil 但打印为 <nil>
	var nilPtr *string
	e := WithOpenAIError(OpenAIError{
		Message: "boom",
		Type:    "some_error",
		Code:    nilPtr,
	}, http.StatusBadRequest)

	claudeErr := e.ToClaudeError()
	require.NotContains(t, claudeErr.Type, "<nil>")
	require.NotEmpty(t, claudeErr.Type)
}

// 回归测试：error.type 必须落在 Anthropic 官方类型集合内，
// 非官方类型（内部类型名、上游自定义类型）按状态码归一化。
func TestToClaudeError_TypeNormalizedToOfficial(t *testing.T) {
	cases := []struct {
		name       string
		upstream   OpenAIError
		statusCode int
		wantType   string
	}{
		{name: "403 internal type", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusForbidden, wantType: "permission_error"},
		{name: "400 internal type", upstream: OpenAIError{Message: "m", Type: "openai_error"}, statusCode: http.StatusBadRequest, wantType: "invalid_request_error"},
		{name: "401 internal type", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusUnauthorized, wantType: "authentication_error"},
		{name: "404 internal type", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusNotFound, wantType: "not_found_error"},
		{name: "413 internal type", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusRequestEntityTooLarge, wantType: "request_too_large"},
		{name: "422 internal type", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusUnprocessableEntity, wantType: "unprocessable_entity_error"},
		{name: "429 internal type", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusTooManyRequests, wantType: "rate_limit_error"},
		{name: "402 billing", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusPaymentRequired, wantType: "billing_error"},
		{name: "409 conflict", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusConflict, wantType: "conflict_error"},
		{name: "504 timeout", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusGatewayTimeout, wantType: "timeout_error"},
		{name: "529 overloaded", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: 529, wantType: "overloaded_error"},
		{name: "500 internal type", upstream: OpenAIError{Message: "m", Type: "new_api_error"}, statusCode: http.StatusInternalServerError, wantType: "api_error"},
		{name: "official type preserved", upstream: OpenAIError{Message: "m", Type: "rate_limit_error"}, statusCode: http.StatusTooManyRequests, wantType: "rate_limit_error"},
		{name: "official type preserved mismatched status", upstream: OpenAIError{Message: "m", Type: "overloaded_error"}, statusCode: http.StatusInternalServerError, wantType: "overloaded_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := WithOpenAIError(tc.upstream, tc.statusCode)
			claudeErr := e.ToClaudeError()
			require.Equal(t, tc.wantType, claudeErr.Type)
			require.True(t, officialClaudeErrorTypes[claudeErr.Type], "type must be an official Anthropic error type")
		})
	}
}

func TestWithOpenAIError_NilCodeFallsBackToUnknown(t *testing.T) {
	e := WithOpenAIError(OpenAIError{Message: "m", Type: "t"}, http.StatusInternalServerError)
	require.Equal(t, ErrorCode("unknown_error"), e.errorCode, "nil Code should fall back to unknown_error, not <nil>")
}

func TestToOpenAIError_NilCodeSerializesAsNull(t *testing.T) {
	e := WithOpenAIError(OpenAIError{Message: "m", Type: "t"}, http.StatusInternalServerError)
	oai := e.ToOpenAIError()
	require.Nil(t, oai.Code, "absent upstream code must stay nil (marshals as null), not the string <nil>")
	require.NotEqual(t, "<nil>", oai.Type)
}
