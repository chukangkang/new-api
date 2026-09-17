#!/usr/bin/env bash
#
# Claude Opus 5 · thinking 模式硬约束 & 上游一致性 探针
# ============================================================================
#
# 用途：把本脚本的输出直接发给上游/供应商，要求对方书面回答两件事：
#   Q1  在你们的 claude-opus-5 上，thinking.type 的哪些取值会被 400 拒绝？
#   Q2  同一个请求重复 N 次，结果是否一致？（用于识别"混合池/多后端"）
#
# ---------------------------------------------------------------------------
# 官方依据（Anthropic 官方文档《思考功能故障排查》的模型表原文）
# ---------------------------------------------------------------------------
#   模型            | 思考类型 | 默认值 | 以 400 拒绝
#   Claude Opus 5   | 仅自适应 | 开启   | "enabled"、"disabled" ②
#   Claude Opus 4.8 | 仅自适应 | 关闭   | "enabled"
#   Claude Opus 4.7 | 仅自适应 | 关闭   | "enabled"
#   Claude Sonnet 5 | 仅自适应 | 开启   | "enabled"
#   Claude Opus 4.6 | 自适应、扩展（已弃用）① | 关闭 | 无
#   Claude Opus 4.5 | 仅扩展   | 关闭   | "adaptive"
#
#   ① enabled + budget_tokens 在这些模型仍有效，但已弃用
#   ② Claude Opus 5 在 effort 为 high 或更低时接受 "disabled"；
#      与 effort=xhigh/max 组合会返回 400
#
#   官方原文："Extended thinking（thinking.type: enabled 搭配 budget_tokens）
#   在 Claude 4.6 模型上已被弃用（使用它的请求仍会成功）。Claude 4.7 及更高
#   版本的模型不支持它，并会拒绝使用它的请求，返回 400 错误。"
#
#   官方 400 错误原文（仅自适应模型上传 enabled 时）：
#     "thinking.type.enabled" is not supported for this model.
#     Use "thinking.type.adaptive" and "output_config.effort" to control
#     thinking behavior.
#
# ---------------------------------------------------------------------------
# ★ 本脚本最关键的推论
# ---------------------------------------------------------------------------
#   若 claude-opus-5 是【原生 Opus 5 行为】，则 C5 / C8（enabled）必然是 400，
#   且错误文案应与上面那句官方原文一致。
#
#   若实测为 200 => 说明该跳【改写了 thinking 参数】或【后端并非 Opus 5】，
#   二者都足以要求上游给出解释。请把 C5 的原始响应一并发给上游。
#
# ---------------------------------------------------------------------------
# ★ 对照组（C9-C12）的作用
# ---------------------------------------------------------------------------
#   若某跳确实在校验并透传请求参数，则下列用例都必须 400：
#     C9  非法 thinking.type 枚举值        -> 必 400
#     C11 enabled 缺 budget_tokens         -> 必 400
#     C12 budget_tokens > max_tokens       -> 必 400
#   这些用例若也返回 200，说明该跳根本没有校验 thinking 参数，
#   则上游"格式不对才会 200"这类解释不成立。
#
#   C10（非法 output_config.effort）是这组里的【阳性对照】：它应当 400，
#   用来证明该端点本身具备参数校验能力。若 C10=400 而 C9/C11/C12=200，
#   即证明：output_config 被透传校验、而 thinking 被上游中间层改写或丢弃。
#
# ---------------------------------------------------------------------------
# 用法
# ---------------------------------------------------------------------------
#   KEY=sk-xxx BASE=https://api.starcore.cloud ./test_claude5_mode_probe.sh
#   ./test_claude5_mode_probe.sh <BASE> <KEY> [MODEL] [REPS]
#
#   KEY  必填：密钥一律运行时传入，不写进脚本（避免误提交进仓库）
#   REPS 每个用例重复次数，默认 5（用于检测抖动，建议 >= 5）
#   KEEP=1 保留证据目录（含每个用例的完整请求体与响应体）
#
#   依赖：curl（必需）、jq（必需）
#
# ============================================================================

set -uo pipefail

# ---------------------------------------------------------------- 参数
POS=()
for _a in "$@"; do POS+=("${_a}"); done

BASE="${BASE:-https://new-api.ai}"
# 密钥一律运行时传入（环境变量 KEY 或第二个位置参数），不写死在脚本里
KEY="${KEY:-}"



MODEL="${MODEL:-claude-opus-5}"
REPS="${REPS:-1}"

[[ "${#POS[@]}" -gt 0 ]] && BASE="${POS[0]}"
[[ "${#POS[@]}" -gt 1 ]] && KEY="${POS[1]}"
[[ "${#POS[@]}" -gt 2 ]] && MODEL="${POS[2]}"
[[ "${#POS[@]}" -gt 3 ]] && REPS="${POS[3]}"

if [[ -z "${KEY}" ]]; then
  echo "错误：未提供 KEY。请勿把密钥写进脚本，用环境变量或参数传入：" >&2
  echo "  KEY=sk-xxx $0 [BASE] [MODEL] [REPS]" >&2
  echo "  $0 <BASE> <KEY> [MODEL] [REPS]" >&2
  exit 1
fi

MAXTOK="${MAXTOK:-102400}"
TMP=$(mktemp -d)
cleanup() { [[ "${KEEP:-}" == "1" ]] || rm -rf "${TMP}"; }
trap cleanup EXIT

if [[ -t 1 ]]; then
  C_R=$'\033[31m'; C_G=$'\033[32m'; C_Y=$'\033[33m'; C_B=$'\033[36m'; C_D=$'\033[2m'; C_0=$'\033[0m'
else
  C_R=''; C_G=''; C_Y=''; C_B=''; C_D=''; C_0=''
fi

hr()    { printf '%s\n' "${C_D}----------------------------------------------------------------${C_0}"; }
note()  { printf '    %s· %s%s\n' "${C_D}" "$1" "${C_0}"; }

# 问题必须足够难，否则自适应模式会跳过思考，导致判据噪声
PROMPT="Compute (833 × 547) − 18. Think carefully, then reply with only the final integer."
THINKING="I start with a constrained Chinese composition task, not a factual question, so no searching is needed. I enumerate the constraints: the character for not appears exactly three times in the whole answer including punctuation but excluding the table; the possessive particle appears a prime number of times greater than five, so seven, eleven, thirteen, and so on; the total character count of the main body, punctuation included, divided by seven leaves a remainder of three; the city name appears exactly once and the lake name appears twice more than that, meaning three times; reversing the whole body, its first character must be the character for good, so the body's final character must be good; the body must contain a rhetorical question, a hypothetical question with its answer, and a parallelism; the pronouns for you, I, and he must not appear; the year 2026 appears exactly once; the table must list the actual counts and the text must be regenerated if they are not satisfied; and the last line must be exactly the character for good followed by a full stop.\n\nI notice a conflict: if the ending line belongs to the body, the body's last character is the full stop, not good, which breaks the reversal condition. I resolve this by treating the main body as the single paragraph and outputting the mandated final line separately, stating this counting convention in the table.\n\nFor the questions, I plan a rhetorical question of the form \"how could this not...\", a hypothetical question asking what charm such a city hides followed by its answer, and parallel structures with three matching clauses, using the lake's seasons, dawn and dusk, and rain and snow, another triple about where the answer hides, and a third triple of conditional clauses about water, bridges, and poetry.\n\nOn counting, I first reach a possessive-particle total of eight, which is not prime, so I add a tail that brings it to eleven. The character for not lands at exactly three. I then tally characters segment by segment and reach 198, whose remainder modulo seven is two, so I add two characters to reach 199 and a remainder of three, but I later rework the ending to close naturally with the character for good, arriving at 206 characters with remainder three, eleven possessives, exactly three uses of not, one city mention, three lake mentions, four characters for the year, and no forbidden pronouns. I double-check the parallelism, the two question types, and that the final character is good."
SIGNATURE="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"

# ---------------------------------------------------------------- 构造请求体
mkbody() { # mkbody <extra-json-object-or-empty> [max_tokens]
  local extra="$1" mt="${2:-${MAXTOK}}"
  if [[ -z "${extra}" ]]; then
    jq -nc --arg m "${MODEL}" --arg p "${PROMPT}" --argjson mt "${mt}" \
      '{model:$m,max_tokens:$mt,messages:[{role:"user",content:$p}]}'
  else
    jq -nc --arg thinking "${THINKING}" --arg sig "${SIGNATURE}" --arg m "${MODEL}" --arg p "${PROMPT}" --argjson mt "${mt}" --argjson ex "${extra}" \
      '({model:$m,max_tokens:$mt,messages:[{role:"assistant",content:[{type:"thinking",thinking:$thinking,signature:$sig}]},{role:"user",content:$p}]} + $ex)'
  fi
}

# ---------------------------------------------------------------- 用例表
# 期望值按【官方 Opus 5 表】填写
LABELS=(); EXPECTS=(); BODIES=()
add_case() { LABELS+=("$1"); EXPECTS+=("$2"); BODIES+=("$3"); }

add_case "C1 无 thinking（基线）"              "200" "$(mkbody '')"
add_case "C2 adaptive"                         "200" "$(mkbody '{"thinking":{"type":"adaptive"}}')"
add_case "C3 adaptive+display=summarized"      "200" "$(mkbody '{"thinking":{"type":"adaptive","display":"summarized"}}')"
add_case "C4 adaptive+effort=high"             "200" "$(mkbody '{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}')"
add_case "C5 enabled+budget_tokens=1024"       "400" "$(mkbody '{"thinking":{"type":"enabled","budget_tokens":1024}}')"
add_case "C6 disabled（effort 未传）"           "200" "$(mkbody '{"thinking":{"type":"disabled"}}')"
add_case "C7 disabled+effort=max"              "400" "$(mkbody '{"thinking":{"type":"disabled"},"output_config":{"effort":"max"}}')"
add_case "C8 enabled+budget=1024+effort=high"  "400" "$(mkbody '{"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"high"}}')"

# ---- 对照组：任何做过枚举校验的实现在这些用例上都必须 400 -------------------
add_case "C9 ★非法 thinking.type=__bogus__"     "400" "$(mkbody '{"thinking":{"type":"__bogus_mode_xyz__"}}')"
add_case "C10 ★非法 effort=__bogus__"           "400" "$(mkbody '{"thinking":{"type":"adaptive"},"output_config":{"effort":"__bogus_effort_xyz__"}}')"
add_case "C11 enabled 缺 budget_tokens"         "400" "$(mkbody '{"thinking":{"type":"enabled"}}')"
add_case "C12 budget_tokens>max_tokens(8192>4096)" "400" "$(mkbody '{"thinking":{"type":"enabled","budget_tokens":8192}}')"

# ---------------------------------------------------------------- 单次请求
HTTP_CODE=""; TEXT=""
call() { # call <name> <json>
  local name="$1" json="$2"
  printf '%s' "${json}" > "${TMP}/${name}.req.json"
  HTTP_CODE=$(curl -sS -m 240 -X POST "${BASE}/v1/messages" \
    -H "x-api-key: ${KEY}" \
    -H "anthropic-version: 2023-06-01" \
    -H "content-type: application/json" \
    -D "${TMP}/${name}.headers" \
    -d "${json}" \
    -o "${TMP}/${name}.json" \
    -w '%{http_code}' 2>"${TMP}/${name}.err") || HTTP_CODE="000"
  TEXT=$(cat "${TMP}/${name}.json" 2>/dev/null)
}

errmsg()  { printf '%s' "$1" | jq -r '.error.message // .msg // .message // .detail // empty' 2>/dev/null; }
vtypes()  { printf '%s' "$1" | jq -r '[.content[]?|.type] | join(">")' 2>/dev/null; }
vin()     { printf '%s' "$1" | jq -r '.usage.input_tokens // 0' 2>/dev/null; }
vout()    { printf '%s' "$1" | jq -r '.usage.output_tokens // 0' 2>/dev/null; }
vthklen() { printf '%s' "$1" | jq -r '[.content[]?|select(.type=="thinking")|((.thinking // "")|length)] | add // 0' 2>/dev/null; }

# ============================================================================
printf '\n%s' "${C_B}"
cat <<'BANNER'
+==================================================================+
|  Claude Opus 5 · thinking 模式硬约束 & 上游一致性 探针           |
+==================================================================+
BANNER
printf '%s' "${C_0}"
echo "  目标 BASE : ${BASE}"
echo "  模型名     : ${MODEL}"
echo "  每例重复   : ${REPS} 次"
echo "  max_tokens : ${MAXTOK}"
echo "  时间       : $(date '+%Y-%m-%d %H:%M:%S')"
echo "  官方期望   : enabled 必 400（Opus 5 为"仅自适应"）；adaptive 必 200"
hr

# ============================================================================
RESULTS=()
ALERT=0

for i in "${!LABELS[@]}"; do
  name="c$((i + 1))"
  label="${LABELS[$i]}"
  expect="${EXPECTS[$i]}"
  body="${BODIES[$i]}"

  dist=""; err=""; types=""; usage=""
  for r in $(seq 1 "${REPS}"); do
    call "${name}_${r}" "${body}"
    dist="${dist} ${HTTP_CODE}"
    if [[ "${HTTP_CODE}" != "200" ]]; then
      m=$(errmsg "${TEXT}")
      [[ -n "${m}" ]] && err="${m}"
    else
      t=$(vtypes "${TEXT}")
      [[ -n "${t}" ]] && types="${t}"
      usage="in=$(vin "${TEXT}") out=$(vout "${TEXT}") thinking_chars=$(vthklen "${TEXT}")"
    fi
  done

  dist_trim=$(printf '%s' "${dist}" | sed 's/^ //')
  n_total="${REPS}"
  n_expect=$(printf '%s' "${dist_trim}" | tr ' ' '\n' | grep -c "^${expect}$" || true)
  n_uniq=$(printf '%s' "${dist_trim}" | tr ' ' '\n' | sort -u | wc -l | tr -d ' ')

  if [[ "${n_uniq}" != "1" ]]; then
    verdict_str="⚠ 抖动（同一请求结果不一致）"
    ALERT=1
  elif [[ "${n_expect}" == "${n_total}" ]]; then
    verdict_str="✓ 符合官方"
  else
    verdict_str="✗ 与官方不符"
    ALERT=1
  fi

  printf '\n%s[%s]%s %s\n' "${C_B}" "${name}" "${C_0}" "${label}"
  printf '    期望 %s | 实测: %s | %s\n' "${expect}" "${dist_trim}" "${verdict_str}"
  [[ -n "${types}" ]] && note "content 块序列: ${types}   ${usage}"
  if [[ -n "${err}" ]]; then
    note "错误原文: ${err}"
  fi

  RESULTS+=("${name}|${label}|${expect}|${dist_trim}|${verdict_str}|${err}|${types}|${usage}")
done

# ============================================================================
hr
echo
echo "汇总表（可直接复制给上游）"
hr
printf '%-6s %-34s %-6s %-22s %s\n' "用例" "thinking 参数" "期望" "实测分布" "结论"
hr
for row in "${RESULTS[@]}"; do
  IFS='|' read -r n l e d v _rest <<<"${row}"
  printf '%-6s %-34s %-6s %-22s %s\n' "${n}" "${l}" "${e}" "${d}" "${v}"
done
hr

echo
echo "边界与顺序校验（官方要求：thinking 块必须位于 text 块之前）"
hr
ORDER_BAD=0
for row in "${RESULTS[@]}"; do
  IFS='|' read -r n l e d v _err types _usage <<<"${row}"
  if [[ -n "${types}" && "${types}" == *thinking* && "${types}" == *text* ]]; then
    first_thk=$(printf '%s' "${types}" | grep -bo 'thinking' | head -1 | cut -d: -f1)
    first_txt=$(printf '%s' "${types}" | grep -bo 'text' | head -1 | cut -d: -f1)
    if [[ "${first_thk}" -lt "${first_txt}" ]]; then
      printf '  %s✓%s %s  thinking 在 text 之前（%s）\n' "${C_G}" "${C_0}" "${n}" "${types}"
    else
      printf '  %s✗%s %s  thinking 不在 text 之前（%s）\n' "${C_R}" "${C_0}" "${n}" "${types}"
      ORDER_BAD=1
    fi
  fi
done
[[ "${ORDER_BAD}" == "0" ]] && note "凡返回了 thinking 的用例，顺序均符合官方要求"

echo
echo "结论"
hr
if [[ "${ALERT}" == "1" ]]; then
  echo "  存在与官方不符或结果抖动的用例 => 该跳不是稳定的原生 Opus 5 行为。"
  echo "  请上游就下列问题给出书面答复："
else
  echo "  全部用例与官方 Opus 5 行为一致，且重复 ${REPS} 次结果稳定。"
fi
echo "    1) C5/C8（thinking.type=enabled）在官方 Opus 5 上必然 400，请解释实测结果；"
echo "    2) 若上游声称已把 enabled 改写为 adaptive，请给出改写发生的组件与依据；"
echo "    3) 若同一请求出现 200/400 抖动，请说明后端池构成（是否混用非 Opus 5 后端）。"
echo
echo "  发给上游时，建议附上本目录（KEEP=1 可保留）："
echo "    KEEP=1 REPS=10 $0 ${BASE} <KEY> ${MODEL}"
echo

if [[ "${KEEP:-}" == "1" ]]; then
  echo "证据目录已保留：${TMP}"
  echo "  每个用例的完整请求体：<case>_<n>.req.json"
  echo "  每个用例的完整响应体：<case>_<n>.json"
  echo
fi
