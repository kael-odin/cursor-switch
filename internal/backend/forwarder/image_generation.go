package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// generateImage 调用 OpenAI 兼容 images API（{baseURL}/v1/images/generations）生成图片，
// 返回 base64 编码（无 data: 前缀），供 GenerateImage 工具回传给 Cursor 客户端。
//
// 背景：GenerateImage 工具此前是空壳 stub——直接读 carrier.ImageData（结果字段，正常流程为空）
// 当成功返回，agent 调用必失败。这里改为真实调上游 image 模型（如 gpt-image-2）的 images API。
//
// model 由调用方决定（adapter.ImageModelID，空则回退 Model）；baseURL/apiKey 复用当前命中的
// chat adapter，与 chat 共用同一凭据（OpenAI 同一 key 可调 chat 与 images 两类端点）。
//
// 仅文生图。图生图（reference_image_paths 非空）走 /v1/images/edits，留 TODO。
func generateImage(ctx context.Context, baseURL, apiKey, model, prompt string) (string, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/v1/images/generations"
	started := time.Now()
	log.Printf("forwarder image generate start endpoint=%s model=%s prompt_bytes=%d", endpoint, model, len(prompt))
	body := map[string]any{
		"model":           model,
		"prompt":          prompt,
		"n":               1,
		"response_format": "b64_json",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode image request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build image request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Content-Type", "application/json")
	// 生图慢：部分上游正常就要 120s+。整体超时交给 ctx（由 runImageGeneration 给 15min 看门狗），
	// 这里只控建连/首字节下限，绝不设 http.Client.Timeout——那会砍掉正常的慢请求。
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			// ResponseHeaderTimeout 等上游开始返回响应头；生图是非流式一次性响应，
			// 上游在生成期间不写任何字节，故此处也不能太短（设 10min，与 ctx 看门狗同量级）。
			ResponseHeaderTimeout: 10 * time.Minute,
			// IdleConnTimeout 复用连接的空闲回收，与单次请求耗时无关。
			IdleConnTimeout: 90 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("forwarder image generate failed endpoint=%s model=%s elapsed_ms=%d err=%v", endpoint, model, time.Since(started).Milliseconds(), err)
		return "", fmt.Errorf("image request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("forwarder image generate bad_status endpoint=%s model=%s status=%d elapsed_ms=%d body=%s", endpoint, model, resp.StatusCode, time.Since(started).Milliseconds(), parseImageAPIError(raw))
		return "", fmt.Errorf("image API returned %d: %s", resp.StatusCode, parseImageAPIError(raw))
	}
	// OpenAI images 响应：{data:[{b64_json:"...", revised_prompt:"..."}]}
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode image response: %w", err)
	}
	if len(out.Data) == 0 || strings.TrimSpace(out.Data[0].B64JSON) == "" {
		log.Printf("forwarder image generate no_data endpoint=%s model=%s elapsed_ms=%d", endpoint, model, time.Since(started).Milliseconds())
		return "", fmt.Errorf("image API returned no image data")
	}
	log.Printf("forwarder image generate ok endpoint=%s model=%s elapsed_ms=%d image_bytes=%d", endpoint, model, time.Since(started).Milliseconds(), len(out.Data[0].B64JSON))
	return out.Data[0].B64JSON, nil
}

// editImage 调用 OpenAI 兼容 images edits API（{baseURL}/v1/images/edits）做图生图。
// referenceImages 是已读取的参考图字节切片；filenames 用于 multipart 文件名（决定扩展名→Content-Type）。
//
// OpenAI /v1/images/edits 走 multipart/form-data：image（第一张参考图）+ prompt + model + n +
// response_format=b64_json。多张参考图按 OpenAI 多 image part 语义追加（部分兼容端点只取首张）。
// 返回与 generateImage 同构：base64（无 data: 前缀）。
func editImage(ctx context.Context, baseURL, apiKey, model, prompt string, referenceImages []imageReference) (string, error) {
	if len(referenceImages) == 0 {
		return "", fmt.Errorf("edit image requires at least one reference image")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/v1/images/edits"
	started := time.Now()
	log.Printf("forwarder image edit start endpoint=%s model=%s prompt_bytes=%d reference_count=%d", endpoint, model, len(prompt), len(referenceImages))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", model); err != nil {
		return "", fmt.Errorf("encode edit model field: %w", err)
	}
	if err := writer.WriteField("prompt", prompt); err != nil {
		return "", fmt.Errorf("encode edit prompt field: %w", err)
	}
	if err := writer.WriteField("n", "1"); err != nil {
		return "", fmt.Errorf("encode edit n field: %w", err)
	}
	if err := writer.WriteField("response_format", "b64_json"); err != nil {
		return "", fmt.Errorf("encode edit response_format field: %w", err)
	}
	for _, ref := range referenceImages {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition",
			fmt.Sprintf(`form-data; name="image"; filename=%q`, ref.filename))
		h.Set("Content-Type", imagePartContentType(ref.mimeType, ref.data))
		part, err := writer.CreatePart(h)
		if err != nil {
			return "", fmt.Errorf("create image part: %w", err)
		}
		if _, err := part.Write(ref.data); err != nil {
			return "", fmt.Errorf("write reference image bytes: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("finalize edit multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("build edit image request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	// 图生图含上传，比文生图更慢；整体超时交给 ctx（runImageGeneration 给 15min 看门狗），
	// 这里只控建连/首字节，绝不设 http.Client.Timeout 砍掉正常的慢请求。
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			// multipart 上传后上游在生成期间不写字节，ResponseHeaderTimeout 设与 ctx 同量级。
			ResponseHeaderTimeout: 10 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("forwarder image edit failed endpoint=%s model=%s elapsed_ms=%d err=%v", endpoint, model, time.Since(started).Milliseconds(), err)
		return "", fmt.Errorf("edit image request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("forwarder image edit bad_status endpoint=%s model=%s status=%d elapsed_ms=%d body=%s", endpoint, model, resp.StatusCode, time.Since(started).Milliseconds(), parseImageAPIError(raw))
		return "", fmt.Errorf("image edit API returned %d: %s", resp.StatusCode, parseImageAPIError(raw))
	}
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode edit image response: %w", err)
	}
	if len(out.Data) == 0 || strings.TrimSpace(out.Data[0].B64JSON) == "" {
		log.Printf("forwarder image edit no_data endpoint=%s model=%s elapsed_ms=%d", endpoint, model, time.Since(started).Milliseconds())
		return "", fmt.Errorf("image edit API returned no image data")
	}
	log.Printf("forwarder image edit ok endpoint=%s model=%s elapsed_ms=%d image_bytes=%d", endpoint, model, time.Since(started).Milliseconds(), len(out.Data[0].B64JSON))
	return out.Data[0].B64JSON, nil
}

// imagePartContentType 决定 multipart image part 的 Content-Type。
// 优先用显式声明的 mimeType，否则用字节嗅探（http.DetectContentType）。
// 绝不返回非 image/* 类型：OpenAI /v1/images/edits 校验 data URL 的 image MIME，
// application/octet-stream 会被 400 拒绝（实测：CreateFormFile 写死 octet-stream 即触发）。
// 嗅探失败/非图片时回退 image/png（OpenAI images 默认接受 png）。
func imagePartContentType(mimeType string, payload []byte) string {
	if m := strings.TrimSpace(strings.ToLower(mimeType)); strings.HasPrefix(m, "image/") {
		return m
	}
	if detected := strings.TrimSpace(strings.ToLower(http.DetectContentType(payload))); strings.HasPrefix(detected, "image/") {
		return detected
	}
	return "image/png"
}

// imageReference 是一张已读到内存的参考图。
// filename 用于 multipart 文件名（扩展名仅作展示）；mimeType 决定 part 的 Content-Type，
// 必须是真实 image/* 类型——OpenAI /v1/images/edits 会校验，application/octet-stream 会被 400 拒绝。
type imageReference struct {
	filename string
	mimeType string
	data     []byte
}

// parseImageAPIError 从 OpenAI 兼容错误响应体提取可读 message。
// OpenAI 格式：{"error":{"message":"...","type":"...","code":"..."}}。
// 解析失败则回退原文片段（截断到 200 字符），避免把冗长 JSON 直接抛给用户。
func parseImageAPIError(raw []byte) string {
	var wrapper struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &wrapper) == nil {
		if msg := strings.TrimSpace(wrapper.Error.Message); msg != "" {
			return msg
		}
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
