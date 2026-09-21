package helper

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetSigRegistryMem 清空内存降级后端（含热标记），让每个用例从确定的
// 冷启动状态出发，互不干扰。
func resetSigRegistryMem(t *testing.T) {
	t.Helper()
	sigRegistryMem.mu.Lock()
	sigRegistryMem.buckets = make(map[string]map[string]struct{})
	sigRegistryMem.expiry = make(map[string]time.Time)
	sigRegistryMem.warm = time.Time{}
	sigRegistryMem.mu.Unlock()
}

// setSigRegistryFlag 临时设置 ThinkingSignatureRegistry 开关并返回还原函数。
func setSigRegistryFlag(t *testing.T, on bool) func() {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	old := common.OptionMap[setting.SettingKeyThinkingSignatureRegistry]
	if on {
		common.OptionMap[setting.SettingKeyThinkingSignatureRegistry] = "true"
	} else {
		delete(common.OptionMap, setting.SettingKeyThinkingSignatureRegistry)
	}
	common.OptionMapRWMutex.Unlock()
	return func() {
		common.OptionMapRWMutex.Lock()
		if old == "" {
			delete(common.OptionMap, setting.SettingKeyThinkingSignatureRegistry)
		} else {
			common.OptionMap[setting.SettingKeyThinkingSignatureRegistry] = old
		}
		common.OptionMapRWMutex.Unlock()
	}
}

// TestSignatureRegistry_MemBackendRegisterAndLookup 验证内存降级后端的
// 登记/命中/未命中/空桶四态语义（测试环境无 Redis，RDB==nil 走内存）。
func TestSignatureRegistry_MemBackendRegisterAndLookup(t *testing.T) {
	resetSigRegistryMem(t)
	ctx := context.Background()
	const uid = 900001
	model := "claude-opus-5"

	// 冷启动（从未登记）：available=false（宽限的前提）
	hit, available := HasRegisteredSignature(ctx, uid, model, "whatever")
	assert.False(t, hit)
	assert.False(t, available, "cold registry must report unavailable (fail-open)")

	RegisterIssuedSignatures(ctx, uid, model, []string{"sig-A", "sig-B"})

	hit, available = HasRegisteredSignature(ctx, uid, model, "sig-A")
	assert.True(t, available)
	assert.True(t, hit, "registered signature must hit")

	hit, available = HasRegisteredSignature(ctx, uid, model, "sig-C")
	assert.True(t, available)
	assert.False(t, hit, "unseen signature must miss")

	// 其他 (用户,模型) 桶：注册表已热但该桶为空 -> 未命中（跨用户隔离的基础）
	hit, available = HasRegisteredSignature(ctx, uid+1, model, "sig-A")
	assert.True(t, available, "registry is warm after any registration")
	assert.False(t, hit)

	// userId<=0 / 空模型 / 空签名：不参与注册表
	RegisterIssuedSignatures(ctx, 0, model, []string{"sig-X"})
	hit, available = HasRegisteredSignature(ctx, 0, model, "sig-X")
	assert.False(t, available)
	assert.False(t, hit)
}

// TestValidateThinkingSignaturesFull_RegistryRejectsTampered 核心回归：
// 结构完全合法但从未被上游签发过的签名（模拟单字符篡改后的真签名），
// 在注册表开启时必须 400；注册表关闭时保持既有行为（放行）。
func TestValidateThinkingSignaturesFull_RegistryRejectsTampered(t *testing.T) {
	resetSigRegistryMem(t)
	restore := setSigRegistryFlag(t, true)
	defer restore()

	ctx := context.Background()
	const uid = 900002
	model := "claude-opus-5"

	issued := buildValidSigSkeletonForModel(model)
	// 单字符篡改：翻密密文主体（f5）内部一字节。结构/长度/元数据全部不变，
	// 骨架+深度指纹校验照常通过，但签名串与签发值不同——正是注册表要拦的场景。
	raw, derr := base64.StdEncoding.DecodeString(issued)
	require.NoError(t, derr)
	raw[len(raw)-10] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(raw)
	require.NotEqual(t, issued, tampered)

	body := func(sig string) []byte {
		return []byte(fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [
			{"role": "assistant", "content": [{"type": "thinking", "thinking": "hmm", "signature": %q}]},
			{"role": "user", "content": "hi"}
		]}`, model, sig))
	}

	// 冷启动宽限：注册表开启但尚无任何登记 -> fail-open 放行
	require.NoError(t, ValidateThinkingSignaturesFull(body(issued), uid),
		"cold start (empty registry) must fail-open")

	// 登记上游签发过的 (签名, 文本) 配对后：命中的放行
	// （thinking 块按配对指纹记账，与收割侧同公式）
	RegisterIssuedSignatures(ctx, uid, model,
		[]string{SignaturePairFingerprint(issued, "hmm")})
	require.NoError(t, ValidateThinkingSignaturesFull(body(issued), uid),
		"issued signature must be accepted")

	// 未签发过的（篡改/捏造）-> 拒绝
	err := ValidateThinkingSignaturesFull(body(tampered), uid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "signature not recognized")

	// 其他用户即使见过该签名也不放行（跨用户隔离）
	err = ValidateThinkingSignaturesFull(body(issued), uid+1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "signature not recognized")
}

// TestValidateThinkingSignaturesFull_PairBindingRejectsReboundText 核心回归：
// 真签名 S 原本配 thinking 文本 T（登记时按配对指纹记账）。攻击者拿 S 配
// 篡改后的文本 T' 回传——签名成员检查会放行（S 在账上），但配对指纹
// H(S,T') 不在账上 -> 必须 400。这是与官方"签名覆盖 thinking 内容"
// 语义对齐的关键一环。
func TestValidateThinkingSignaturesFull_PairBindingRejectsReboundText(t *testing.T) {
	resetSigRegistryMem(t)
	restore := setSigRegistryFlag(t, true)
	defer restore()

	ctx := context.Background()
	const uid = 900010
	model := "claude-opus-5"

	sig := buildValidSigSkeletonForModel(model)
	const origText = "I reasoned carefully about the problem."
	const reboundText = "I reasoned careFULLY about the problem."

	body := func(thinking string) []byte {
		return []byte(fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [
			{"role": "assistant", "content": [{"type": "thinking", "thinking": %q, "signature": %q}]},
			{"role": "user", "content": "hi"}
		]}`, model, thinking, sig))
	}

	// 登记上游真实签发的 (签名, 文本) 配对
	RegisterIssuedSignatures(ctx, uid, model,
		[]string{SignaturePairFingerprint(sig, origText)})

	// 原样回传：配对命中 -> 放行
	require.NoError(t, ValidateThinkingSignaturesFull(body(origText), uid),
		"originally paired (sig, text) must be accepted")

	// 换绑文本：签名在账上但配对指纹不在 -> 拒绝
	err := ValidateThinkingSignaturesFull(body(reboundText), uid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "signature not recognized")
}

// TestValidateThinkingSignaturesFull_RegistryOffKeepsStructureOnly 验证
// 开关关闭时行为与改造前完全一致（纯结构校验，不看注册表）。
func TestValidateThinkingSignaturesFull_RegistryOffKeepsStructureOnly(t *testing.T) {
	resetSigRegistryMem(t)
	restore := setSigRegistryFlag(t, false)
	defer restore()

	model := "claude-opus-5"
	unissued := buildValidSigSkeletonForModel(model)
	body := []byte(fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "hmm", "signature": %q}]},
		{"role": "user", "content": "hi"}
	]}`, model, unissued))
	require.NoError(t, ValidateThinkingSignaturesFull(body, 900003),
		"registry off: structure-valid signature passes regardless of issuance")
}

// TestValidateThinkingSignatures_BackwardCompat 确认旧的单参入口仍然只做
// 结构校验（userId=0 时注册表不参与），保护既有调用方与测试。
func TestValidateThinkingSignatures_BackwardCompat(t *testing.T) {
	resetSigRegistryMem(t)
	restore := setSigRegistryFlag(t, true)
	defer restore()

	model := "claude-opus-5"
	unissued := buildValidSigSkeletonForModel(model)
	body := []byte(fmt.Sprintf(`{"model": %q, "max_tokens": 100, "messages": [
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "hmm", "signature": %q}]},
		{"role": "user", "content": "hi"}
	]}`, model, unissued))
	require.NoError(t, ValidateThinkingSignatures(body))
}
