package setting

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// SettingKeyThinkingSignatureValidation 是 thinking 块签名结构校验的系统设置键。
// 默认开（缺失/查询出错均按开处理，fail-open 到"校验开"），仅显式 "false" 关闭。
const SettingKeyThinkingSignatureValidation = "ThinkingSignatureValidation"

// SettingKeyThinkingSignatureRegistry 是 thinking 签名「发放注册表」校验的
// 系统设置键。开启后，除结构校验外还要求非空签名确实出现在该用户近期收到的
// 上游响应中（拦截单字符篡改/跨用户重放/凭空捏造）。默认关：注册表依赖
// 网关自身的签发记录，存量会话在冷启动期会经历 fail-open 宽限，显式开启
// 表明运维已知晓并接受该行为。
const SettingKeyThinkingSignatureRegistry = "ThinkingSignatureRegistry"

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

// ShouldVerifyThinkingSignatureRegistry 判断是否启用 thinking 签名的
// 发放注册表成员校验。与结构校验相反，此项默认关（缺失/查询出错均按关
// 处理），仅显式 "true" 开启——它是增强校验，开启前应确认客户端签名
// 均来自本网关转发的上游响应。
func ShouldVerifyThinkingSignatureRegistry() bool {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[SettingKeyThinkingSignatureRegistry]
	common.OptionMapRWMutex.RUnlock()
	return strings.TrimSpace(strings.ToLower(value)) == "true"
}
