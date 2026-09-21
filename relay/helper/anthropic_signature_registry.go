package helper

// 本文件实现 thinking 签名的「发放注册表」（issuance registry）。
//
// 背景（密码学解剖结论，见 _tmp_sigdump/cryptcheck 实测）：
// Anthropic 的 thinking 签名是密钥化的 MAC（protobuf 外壳 + 内嵌模型名/
// "thinking" 标识/UUID/时间戳 + 高熵密文主体），网关没有 Anthropic 私钥，
// 无法对其做真正的密码学验签；穷举 SHA-256/384/512 亦未发现部件间的
// 明文哈希承诺。因此静态能做的最强校验就是结构+深度指纹（见
// anthropic_messages_validation.go），它对"密文主体内部单字符篡改"
// 无能为力。
//
// 注册表补齐这一环：网关在转发 /v1/messages 成功后，把上游响应里
// 真实签发的签名登记下来（按 用户+模型 维度，带 TTL）；下一次请求携带
// 非空签名时，除了结构校验，还要求该签名确实出现在本用户近期收到的
// 上游响应中。由此可以拦截：
//   - 单字符篡改的"真签名"（结构完好但从未被上游签发过）；
//   - 跨用户重放（A 用户收割的签名塞进 B 用户的请求）；
//   - 任意凭空捏造的结构合法签名。
//
// 存储：优先 Redis（多实例共享，HSET + TTL）；未启用 Redis 时降级为
// 进程内 map（单实例可用，重启清空）。两种后端都受容量上限保护。
//
// 冷启动宽限：注册表为空（例如刚部署、TTL 过期、Redis 未启用前的存量
// 会话）时对成员检查 fail-open，只保留结构校验，避免误杀合法客户端。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
)

// SignaturePairFingerprint 计算「签名 ↔ thinking 文本」的配对指纹：
// hex(sha256(sig + "\x00" + thinking))。登记侧（上游响应收割）与校验侧
// （请求体重算）使用同一公式：命中即证明这对 (签名, 文本) 确实出自同一
// 次上游响应，堵住"拿真签名 S 配篡改后的文本 T'"的换绑漏洞——官方凭
// 私钥验签能拦、纯签名成员检查拦不住的那一类。
//
// 仅适用于 thinking 块（有文本可绑）；redacted_thinking 无文本，
// 沿用裸签名成员检查。
func SignaturePairFingerprint(sig, thinking string) string {
	h := sha256.Sum256([]byte(sig+"\x00"+thinking))
	return hex.EncodeToString(h[:])
}

// sigRegistryTTL 是已签发签名的存活时长。多轮 agentic 会话（Claude Code
// 等）通常在分钟级内完成，24h 足够覆盖绝大多数会话链，同时限制集合膨胀。
const sigRegistryTTL = 24 * time.Hour

// sigRegistryMaxEntries 是单个 (用户,模型) 桶登记的签名数量上限。
// 超过后停止登记新签名（旧签名自然随 TTL 老化），防止恶意/异常流量
// 无限撑大集合。
const sigRegistryMaxEntries = 512

// sigRegistryMemMaxKeys 是内存降级模式下 (用户,模型) 桶的数量上限。
const sigRegistryMemMaxKeys = 4096

// sigRegistryWarmKey 是全局"注册表已有数据"标记键。冷启动宽限以此为界：
// 标记不存在（网关从未登记过任何签名）时成员检查 fail-open；标记存在后
// 一切未命中都是拒绝——包括其他 (用户,模型) 桶的空桶，堵住跨用户重放。
const sigRegistryWarmKey = "anthropic:sigreg:warm"

// sigRegistryKey 构造注册表的存储键。
func sigRegistryKey(userId int, model string) string {
	return fmt.Sprintf("anthropic:sigreg:%d:%s", userId, model)
}

// RegisterIssuedSignatures 把上游响应中出现的 thinking/redacted_thinking
// 签名登记进注册表。userId<=0 或非 Claude 家族模型时静默跳过。
// 任何存储错误只记日志不影响主流程（验签是增强手段，不能反过来弄挂转发）。
func RegisterIssuedSignatures(ctx context.Context, userId int, model string, sigs []string) {
	if ctx == nil || userId <= 0 || model == "" || len(sigs) == 0 {
		return
	}
	key := sigRegistryKey(userId, model)
	if common.RedisEnabled && common.RDB != nil {
		registerIssuedSignaturesRedis(ctx, key, sigs)
		return
	}
	registerIssuedSignaturesMem(ctx, key, sigs)
}

// HasRegisteredSignature 判断签名是否曾被上游对该 (用户,模型) 签发过。
// 返回 (是否命中, 注册表是否处于"热"状态)。热 = 网关近期登记过任意签名
// （全局标记），此时未命中即拒绝；冷（从未登记）或未启用后端时
// available=false，调用方应 fail-open（只做结构校验）。
func HasRegisteredSignature(ctx context.Context, userId int, model string, sig string) (bool, bool) {
	if ctx == nil || userId <= 0 || model == "" || sig == "" {
		return false, false
	}
	key := sigRegistryKey(userId, model)
	if common.RedisEnabled && common.RDB != nil {
		warm, err := common.RDB.Exists(ctx, sigRegistryWarmKey).Result()
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("signature registry: warm check failed: %v", err))
			return false, false
		}
		if warm == 0 {
			return false, false
		}
		hit, err := common.RDB.HExists(ctx, key, sig).Result()
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("signature registry: hexists failed: %v", err))
			return false, false
		}
		return hit, true
	}
	return hasRegisteredSignatureMem(ctx, key, sig)
}

func registerIssuedSignaturesRedis(ctx context.Context, key string, sigs []string) {
	pipe := common.RDB.Pipeline()
	for _, s := range sigs {
		pipe.HSet(ctx, key, s, "1")
	}
	pipe.Expire(ctx, key, sigRegistryTTL)
	// 刷新全局热标记：只要还在持续登记，宽限期就不结束
	pipe.SetNX(ctx, sigRegistryWarmKey, "1", sigRegistryTTL)
	pipe.Expire(ctx, sigRegistryWarmKey, sigRegistryTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("signature registry: pipeline exec failed: %v", err))
	}
}

// ── 内存降级后端（未启用 Redis 时使用）──────────────────────────────

var sigRegistryMem struct {
	mu      sync.Mutex
	buckets map[string]map[string]struct{}
	expiry  map[string]time.Time
	warm    time.Time // 零值 = 从未登记（冷）
}

func init() {
	sigRegistryMem.buckets = make(map[string]map[string]struct{})
	sigRegistryMem.expiry = make(map[string]time.Time)
}

func pruneExpiredMem(now time.Time) {
	for k, exp := range sigRegistryMem.expiry {
		if now.After(exp) {
			delete(sigRegistryMem.buckets, k)
			delete(sigRegistryMem.expiry, k)
		}
	}
	if !sigRegistryMem.warm.IsZero() && now.After(sigRegistryMem.warm.Add(sigRegistryTTL)) {
		sigRegistryMem.warm = time.Time{}
	}
}

func registerIssuedSignaturesMem(ctx context.Context, key string, sigs []string) {
	sigRegistryMem.mu.Lock()
	defer sigRegistryMem.mu.Unlock()
	now := time.Now()
	pruneExpiredMem(now)
	set, ok := sigRegistryMem.buckets[key]
	if !ok {
		if len(sigRegistryMem.buckets) >= sigRegistryMemMaxKeys {
			// 桶已满且目标桶不存在：放弃登记（fail-open 语义，不误杀）
			return
		}
		set = make(map[string]struct{}, len(sigs))
		sigRegistryMem.buckets[key] = set
		sigRegistryMem.expiry[key] = now.Add(sigRegistryTTL)
	}
	for _, s := range sigs {
		if len(set) >= sigRegistryMaxEntries {
			break
		}
		set[s] = struct{}{}
	}
	sigRegistryMem.warm = now // 每次登记都续热标记
}

func hasRegisteredSignatureMem(ctx context.Context, key string, sig string) (bool, bool) {
	sigRegistryMem.mu.Lock()
	defer sigRegistryMem.mu.Unlock()
	now := time.Now()
	pruneExpiredMem(now)
	if sigRegistryMem.warm.IsZero() {
		return false, false
	}
	set, ok := sigRegistryMem.buckets[key]
	if !ok {
		return false, true
	}
	_, hit := set[sig]
	return hit, true
}
