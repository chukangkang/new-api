package setting

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// SettingKeyThinkingSignatureValidation 是 thinking 块签名结构校验的系统设置键。
// 默认开（缺失/查询出错均按开处理，fail-open 到"校验开"），仅显式 "false" 关闭。
const SettingKeyThinkingSignatureValidation = "ThinkingSignatureValidation"

// ShouldValidateThinkingSignatures 判断是否启用 /v1/messages 的
// thinking 块签名结构校验（非空签名必须为合法 base64 且解码后 >= 32 字节）。
// 与 sub2api 的 thinking_signature_validation 开关语义一致：
// 默认开，仅显式 "false" 关闭（缺失/查询出错均按开处理）。
func ShouldValidateThinkingSignatures() bool {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[SettingKeyThinkingSignatureValidation]
	common.OptionMapRWMutex.RUnlock()
	return strings.TrimSpace(strings.ToLower(value)) != "false"
}
