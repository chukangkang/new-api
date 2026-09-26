package helper

// 本文件实现 Anthropic /v1/messages 请求体中 image 内容块的官方对齐校验
// （规格书 §2.9，docs/ANTHROPIC_MESSAGES_VALIDATION_SPEC.md）。
//
// 官方规则（platform.claude.com/docs/en/build-with-claude/vision，
// 2026-09-26 核对）：
//   - image 块有三种 source 类型：base64（media_type+data）、url（url）、
//     file（file_id）；Bedrock/GCP 只支持 base64（网关侧不区分平台，全收）；
//   - 支持的媒体类型：image/jpeg / image/png / image/gif / image/webp
//     （GIF 动画只取第一帧）；
//   - 单图最大 8000×8000 px；单请求超过 20 张图时收紧到每张 2000px
//     （计数含历史轮次重发与 tool_result 内嵌的图片）；
//   - base64 解码后单图最大 10 MB（Claude API 直连；Bedrock/GCP 为 5 MB，
//     网关按较宽的 10 MB 校验，平台差异交给上游）；
//   - 每请求最多 600 张（200k 上下文模型为 100 张，该细分交给上游，
//     网关统一按 600 fail-open，避免维护易过期的模型→上下文窗口表）。
//
// 错误文案策略：
//   - media_type 枚举文案为官方逐字（pydantic 风格，生产实测确认）；
//   - 其余文案官方未公布逐字版本，按 pydantic 风格推断并在规格书中标注
//     "待实测"，触发后可用真实报文校正。
//
// 像素校验只解析图片头（PNG/JPEG/GIF/WebP），不解码像素数据；
// 头部解析失败一律放行（fail-open），宁漏拦不误杀。

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"github.com/tidwall/gjson"
)

const (
	// imageMaxDecodedBytes 是 base64 解码后单图的大小上限（Claude API 直连 10 MB）。
	imageMaxDecodedBytes = 10 * 1024 * 1024
	// imageMaxDimensionPx 是常规请求的单边像素上限。
	imageMaxDimensionPx = 8000
	// imageManyThreshold 是触发收紧限制的请求内图片数阈值（超过 20 张）。
	imageManyThreshold = 20
	// imageManyMaxDimensionPx 是多图请求的单边像素上限。
	imageManyMaxDimensionPx = 2000
	// imageMaxCount 是每请求图片数上限（200k 上下文模型的 100 张细分交上游）。
	imageMaxCount = 600
)

// imageMediaTypes 是官方支持的图片媒体类型。
var imageMediaTypes = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

// imgLoc 记录一个 image 块的位置路径与其 JSON 节点。
type imgLoc struct {
	path  string
	block gjson.Result
}

// validateAnthropicImages 校验请求体中所有 image 内容块（含 tool_result
// 内嵌的），返回第一个违规的官方风格错误。
func validateAnthropicImages(msgs []gjson.Result) error {
	var imgs []imgLoc
	collectImageBlocks(msgs, &imgs)

	if len(imgs) > imageMaxCount {
		return fmt.Errorf("Too many images in request: maximum %d images allowed, got %d", imageMaxCount, len(imgs))
	}

	many := len(imgs) > imageManyThreshold
	limit := imageMaxDimensionPx
	if many {
		limit = imageManyMaxDimensionPx
	}
	for _, im := range imgs {
		if err := validateImageBlock(im.path, im.block, limit, many); err != nil {
			return err
		}
	}
	return nil
}

// collectImageBlocks 递归收集所有 image 块及其位置路径。
// 位置用官方点分格式（如 messages.0.content.1）；tool_result 块的内嵌
// content 数组也纳入（官方明言其计入多图阈值）。
func collectImageBlocks(msgs []gjson.Result, out *[]imgLoc) {
	for i, m := range msgs {
		content := m.Get("content")
		if !content.IsArray() {
			continue
		}
		for j, blk := range content.Array() {
			pos := fmt.Sprintf("messages.%d.content.%d", i, j)
			switch blk.Get("type").Str {
			case "image":
				*out = append(*out, imgLoc{path: pos, block: blk})
			case "tool_result":
				inner := blk.Get("content")
				if inner.IsArray() {
					for k, iblk := range inner.Array() {
						if iblk.Get("type").Str == "image" {
							*out = append(*out, imgLoc{
								path:  fmt.Sprintf("%s.content.%d", pos, k),
								block: iblk,
							})
						}
					}
				}
			}
		}
	}
}

// validateImageBlock 校验单个 image 块的 source 结构与限额。
func validateImageBlock(pos string, blk gjson.Result, dimLimit int, many bool) error {
	src := blk.Get("source")
	if !src.Exists() || !src.IsObject() {
		return fmt.Errorf("%s.source: Field required", pos)
	}
	st := src.Get("type")
	if !st.Exists() || st.Type != gjson.String {
		return fmt.Errorf("%s.source.type: Field required", pos)
	}
	switch st.Str {
	case "base64":
		mt := src.Get("media_type")
		if !mt.Exists() || mt.Type != gjson.String {
			return fmt.Errorf("%s.source.base64.media_type: Field required", pos)
		}
		if !sliceContains(imageMediaTypes, mt.Str) {
			// 官方逐字文案（pydantic Literal 风格，生产实测确认）
			return fmt.Errorf("%s.source.base64.media_type: Input should be 'image/jpeg', 'image/png', 'image/gif' or 'image/webp'", pos)
		}
		data := src.Get("data")
		if !data.Exists() || data.Type != gjson.String {
			return fmt.Errorf("%s.source.base64.data: Field required", pos)
		}
		if n := base64DecodedLen(data.Str); n > imageMaxDecodedBytes {
			// 待实测：官方未公布逐字文案
			return fmt.Errorf("%s.source.base64.data: Image exceeds the maximum size of 10MB", pos)
		}
		if w, h, ok := decodeImageDimensions(data.Str); ok && (w > dimLimit || h > dimLimit) {
			if many {
				// 官方描述：message references "many-image requests" and
				// states the current limit in pixels（逐字待实测）
				return fmt.Errorf("%s: Image dimensions (%dx%d) exceed the %dpx limit for many-image requests", pos, w, h, dimLimit)
			}
			// 待实测：官方未公布逐字文案
			return fmt.Errorf("%s: Image dimensions (%dx%d) exceed the maximum of %dpx", pos, w, h, dimLimit)
		}
	case "url":
		u := src.Get("url")
		if !u.Exists() || u.Type != gjson.String || u.Str == "" {
			return fmt.Errorf("%s.source.url.url: Field required", pos)
		}
	case "file":
		fid := src.Get("file_id")
		if !fid.Exists() || fid.Type != gjson.String || fid.Str == "" {
			return fmt.Errorf("%s.source.file.file_id: Field required", pos)
		}
	default:
		return fmt.Errorf("%s.source.type: Input should be 'base64', 'url' or 'file'", pos)
	}
	return nil
}

// base64DecodedLen 估算 base64 字符串解码后的字节数（不实际解码）。
// 非法 base64 按长度比例估算，不影响限额判断的量级。
func base64DecodedLen(s string) int {
	core := len(s)
	// 去掉可能的换行与结尾 padding
	for i := 0; i < core; i++ {
		if s[i] == '\n' || s[i] == '\r' || s[i] == ' ' {
			core--
		}
	}
	if core <= 0 {
		return 0
	}
	if s[core-1] == '=' {
		core--
		if core > 0 && s[core-1] == '=' {
			core--
		}
	}
	return core * 3 / 4
}

// decodeImageDimensions 从 base64 图片数据解析宽高（只读文件头）。
// 支持 PNG / JPEG / GIF / WebP；任何解析失败返回 ok=false（放行）。
func decodeImageDimensions(b64 string) (w, h int, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(b64)
		if err != nil {
			return 0, 0, false
		}
	}
	switch {
	case len(raw) >= 24 && raw[0] == 0x89 && raw[1] == 'P' && raw[2] == 'N' && raw[3] == 'G':
		// PNG: IHDR 紧随 8 字节魔数，width/height 为大端 uint32 @16/@20
		if string(raw[8:12]) != "IHDR" {
			return 0, 0, false
		}
		w = int(binary.BigEndian.Uint32(raw[16:20]))
		h = int(binary.BigEndian.Uint32(raw[20:24]))
	case len(raw) >= 10 && raw[0] == 'G' && raw[1] == 'I' && raw[2] == 'F' && raw[3] == '8':
		// GIF87a/89a: logical screen width/height 小端 uint16 @6/@8
		w = int(raw[6]) | int(raw[7])<<8
		h = int(raw[8]) | int(raw[9])<<8
	case len(raw) >= 21 && raw[0] == 'R' && raw[1] == 'I' && raw[2] == 'F' && raw[3] == 'F' &&
		raw[8] == 'W' && raw[9] == 'E' && raw[10] == 'B' && raw[11] == 'P':
		switch string(raw[12:16]) {
		case "VP8 ": // lossy: frame tag @16，start code @18-20，width/height 小端 14bit @21/@23
			if len(raw) < 25 {
				return 0, 0, false
			}
			w = (int(uint16(raw[21])|uint16(raw[22])<<8) & 0x3fff)
			h = (int(uint16(raw[23])|uint16(raw[24])<<8) & 0x3fff)
		case "VP8L": // lossless: fourcc @12，signature byte @16，width-1/height-1 小端 14bit @17/@19
			w = (int(uint16(raw[17])|uint16(raw[18])<<8) & 0x3fff) + 1
			h = (int(uint16(raw[19])|uint16(raw[20])<<8) & 0x3fff) + 1
		case "VP8X": // extended: width-1/height-1 小端 24bit @24/@27
			if len(raw) < 30 {
				return 0, 0, false
			}
			w = int(raw[24]) | int(raw[25])<<8 | int(raw[26])<<16
			h = int(raw[27]) | int(raw[28])<<8 | int(raw[29])<<16
			w, h = w+1, h+1
		default:
			return 0, 0, false
		}
	case len(raw) >= 4 && raw[0] == 0xFF && raw[1] == 0xD8:
		// JPEG: 扫描 SOF 标记（C0-CF，除 C4/D0-D3 外的连续段）
		i := 2
		for i+9 < len(raw) {
			if raw[i] != 0xFF {
				i++
				continue
			}
			marker := raw[i+1]
			if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
				i += 2
				continue
			}
			if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
				// SOF: precision @i+4，height/width 大端 uint16 @i+5/@i+7
				h = int(uint16(raw[i+5])<<8 | uint16(raw[i+6]))
				w = int(uint16(raw[i+7])<<8 | uint16(raw[i+8]))
				return w, h, w > 0 && h > 0
			}
			if i+3 >= len(raw) {
				break
			}
			segLen := int(uint16(raw[i+2]) | uint16(raw[i+3])<<8)
			i += 2 + segLen
		}
		return 0, 0, false
	default:
		return 0, 0, false
	}
	return w, h, w > 0 && h > 0
}
