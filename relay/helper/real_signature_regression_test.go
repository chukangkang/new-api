package helper

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRealProbeSignatures_SkeletonValidation 用探针脚本里的三个真实签名
// 离线验证方案 B 的骨架校验判定：
//   - SIGNATURE  (正确)      -> 通过
//   - SIGNATURE1 (前导加 C)  -> not valid base64
//   - SIGNATURE2 (首字节 C->B)-> malformed
func TestRealProbeSignatures_SkeletonValidation(t *testing.T) {
	data, err := os.ReadFile("../../test_claude5_mode_probe.sh")
	if err != nil {
		t.Skipf("probe script not present: %v", err)
	}
	text := string(data)

	re := regexp.MustCompile(`(?m)^(SIGNATURE[12]?)="([^"]*)"`)
	matches := re.FindAllStringSubmatch(text, -1)
	sigs := map[string]string{}
	for _, m := range matches {
		sigs[m[1]] = m[2]
	}
	require.Len(t, sigs, 3, "expected SIGNATURE, SIGNATURE1, SIGNATURE2")

	// 正确签名：应通过严格骨架校验
	require.NoError(t, checkThinkingSignatureFormat(sigs["SIGNATURE"], true), "real signature should pass")

	// SIGNATURE1：前导加 C，长度 mod4!=0 -> 非法 base64
	err = checkThinkingSignatureFormat(sigs["SIGNATURE1"], true)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "not valid base64"), "got: %v", err)

	// SIGNATURE2：首字节 0x08->0x04，base64 合法但骨架 malformed
	err = checkThinkingSignatureFormat(sigs["SIGNATURE2"], true)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "malformed"), "got: %v", err)
}
