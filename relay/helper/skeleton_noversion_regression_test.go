package helper

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLiveSonnet5SignatureWithoutOuterVersionField 回归：2026-09-21 线上实测
// 发现 claude-sonnet-5 的真实签名外层**没有 field1 版本标记**（直接从
// field2 内层开始），与 fable-5 样本（带 field1=2）不同。此前骨架校验
// 硬性要求 field1，把所有 sonnet-5/早期 opus-4-8 真签名误判为 malformed。
//
// 夹具 testdata/sonnet5_live_sig.txt 是线上网关实际转发回来的真签名
// （thinking 为空、内嵌模型名 claude-sonnet-5）。
func TestLiveSonnet5SignatureWithoutOuterVersionField(t *testing.T) {
	data, err := os.ReadFile("testdata/sonnet5_live_sig.txt")
	if err != nil {
		t.Skipf("fixture not present: %v", err)
	}
	sig := strings.TrimSpace(string(data))
	require.NotEmpty(t, sig)

	// 真签名必须通过严格校验（骨架 + 深度指纹 + 模型名一致）
	require.NoError(t, checkThinkingSignatureFormat(sig, true, "claude-sonnet-5"),
		"real sonnet-5 signature without outer field1 must pass")

	// 模型名不一致（跨模型重放）仍须拒绝
	err = checkThinkingSignatureFormat(sig, true, "claude-opus-5")
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed")

	// 首字节篡改（field2 tag 破坏）仍须拒绝
	firstByteFlip := flipFirstBase64Char(t, sig)
	err = checkThinkingSignatureFormat(firstByteFlip, true, "claude-sonnet-5")
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed")
}

// flipFirstBase64Char 翻转签名首字符对应的最低有效位（保持 base64 合法、
// 解码后首字节变化），模拟单字符篡改。
func flipFirstBase64Char(t *testing.T, sig string) string {
	t.Helper()
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	c := sig[0]
	idx := strings.IndexByte(alphabet, c)
	require.NotEqual(t, -1, idx, "unexpected base64 char %q", c)
	flipped := alphabet[(idx+1)%64]
	return string(flipped) + sig[1:]
}
