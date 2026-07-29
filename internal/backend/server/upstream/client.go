package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"cursor/gen/aiserverv1"
	"cursor/internal/logger"
	"cursor/internal/netproxy"
	"cursor/internal/relayauth"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

func ForwardToUpstream(reqCtx *RequestContext, options ForwardOptions) (*ForwardMeta, error) {
	requestBody := reqCtx.RequestBody
	if options.BodyOverride != nil {
		requestBody = options.BodyOverride
	}
	if !shouldRequestCarryBody(reqCtx.Method) {
		requestBody = []byte{}
	}

	upstreamRequest, upstreamClient, err := buildUpstreamRequest(reqCtx, requestBody, options)
	if err != nil {
		return nil, err
	}

	upstreamResponse, err := upstreamClient.Do(upstreamRequest)
	if err != nil {
		return nil, err
	}
	defer upstreamResponse.Body.Close()

	copyResponseHeadersToClient(reqCtx.ResponseWriter.Header(), upstreamResponse.Header)
	reqCtx.ResponseWriter.WriteHeader(upstreamResponse.StatusCode)

	written, copyErr := copyResponse(reqCtx.ResponseWriter, upstreamResponse.Body)
	meta := &ForwardMeta{
		StatusCode:   upstreamResponse.StatusCode,
		Status:       upstreamResponse.Status,
		ContentType:  upstreamResponse.Header.Get("content-type"),
		ResponseSize: written,
	}
	if copyErr != nil {
		return meta, copyErr
	}
	return meta, nil
}

func buildUpstreamRequest(reqCtx *RequestContext, body []byte, options ForwardOptions) (*http.Request, HTTPClient, error) {
	upstreamRequest, err := http.NewRequestWithContext(reqCtx.Request.Context(), reqCtx.Method, reqCtx.TargetURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("create upstream request failed: %w", err)
	}

	copyRequestHeadersForUpstream(upstreamRequest.Header, reqCtx.Headers)
	upstreamRequest.Header.Del(HeaderRawServerURL)
	if !shouldRequestCarryBody(reqCtx.Method) {
		upstreamRequest.Header.Del("content-length")
	} else {
		upstreamRequest.Header.Set("content-length", strconv.Itoa(len(body)))
	}
	upstreamRequest.Host = reqCtx.TargetURL.Host

	if options.PatchHeaders != nil {
		options.PatchHeaders(upstreamRequest.Header)
	}

	// 无论上游是谁，先无条件剥离所有敏感/内部头，防止通过 copied headers 或 PatchHeaders 泄漏。
	// backend 中间件已从入站请求删除这些头，此处是纵深防御，也覆盖 PatchHeaders 误注入。
	scrubReservedHeaders(upstreamRequest.Header)

	// 仅当策略为 OriginalCursor 且目标校验通过时，恢复捕获的 Cursor 真实凭证。
	if options.Credential == CredentialOriginalCursor {
		if err := restoreOriginalCursorCredentials(upstreamRequest, reqCtx); err != nil {
			return nil, nil, err
		}
	}

	// 凭证策略优先于依赖注入：恢复真实 Cursor 凭证的官方转发必须用不跟随重定向的客户端，
	// 防止服务端 3xx 把 Authorization/Cookie/x-cursor-checksum 带到未校验的目标。
	// 此前为 `if reqCtx.Deps.HTTPClient == nil` 才走 NoRedirect 分支，但 Host 在 rebuildLocked
	// 中无条件注入了 netproxy.NewHTTPClient（会跟随重定向），导致该分支恒不可达、策略被覆盖（F-09）。
	// 现改为：CredentialOriginalCursor 无条件走 NoRedirect，与注入与否解耦；timeout 对齐 Host 注入值。
	var upstreamClient HTTPClient
	if options.Credential == CredentialOriginalCursor {
		upstreamClient = netproxy.NewHTTPClientNoRedirect(upstreamRedirectClientTimeout)
	} else if reqCtx.Deps.HTTPClient != nil {
		upstreamClient = reqCtx.Deps.HTTPClient
	} else {
		upstreamClient = netproxy.NewHTTPClient(upstreamRedirectClientTimeout)
	}

	return upstreamRequest, upstreamClient, nil
}

// scrubReservedHeaders 删除永不应通过默认转发链外泄的敏感/内部头。
func scrubReservedHeaders(header http.Header) {
	header.Del("Authorization")
	header.Del("Cookie")
	header.Del("x-cursor-checksum")
	header.Del(HeaderRawServerURL)
	header.Del(relayauth.HeaderRelayProof)
	header.Del("Proxy-Authorization")
}

// restoreOriginalCursorCredentials 在严格校验最终上游目标后，把捕获的 Cursor 真实凭证
// 恢复到出站请求。目标必须为 HTTPS、主机为 cursor.sh/*.cursor.sh、端口缺省或 443，
// 且与凭证绑定目标一致；任一不满足则拒绝恢复（fail-closed），凭证保持剥离状态。
func restoreOriginalCursorCredentials(upstreamRequest *http.Request, reqCtx *RequestContext) error {
	target := reqCtx.TargetURL
	if target == nil {
		return fmt.Errorf("original-cursor credential policy requires a target url")
	}
	if !strings.EqualFold(target.Scheme, "https") {
		return fmt.Errorf("refuse to restore cursor credentials to non-https target")
	}
	if !isCursorHost(target.Hostname()) {
		return fmt.Errorf("refuse to restore cursor credentials to non-cursor host")
	}
	if port := target.Port(); port != "" && port != "443" {
		return fmt.Errorf("refuse to restore cursor credentials to non-standard port")
	}
	bound := strings.ToLower(target.Scheme) + "://" + strings.ToLower(target.Host)
	creds := reqCtx.Credentials
	if creds.BoundTarget == "" || creds.BoundTarget != bound {
		return fmt.Errorf("credential bound target mismatch")
	}
	if creds.AuthorizationPresent {
		upstreamRequest.Header.Set("Authorization", creds.Authorization)
	}
	if len(creds.Cookies) > 0 {
		for _, cookie := range creds.Cookies {
			upstreamRequest.Header.Add("Cookie", cookie)
		}
	}
	// 原样保留 checksum，包括「原本不存在」的情形；绝不为假 token 重算 checksum。
	if creds.ChecksumPresent {
		upstreamRequest.Header.Set("x-cursor-checksum", creds.Checksum)
	}
	return nil
}

// isCursorHost 判断主机是否为 cursor.sh 或其子域。
func isCursorHost(host string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if normalized == "" {
		return false
	}
	return normalized == "cursor.sh" || strings.HasSuffix(normalized, ".cursor.sh")
}

func copyResponse(writer io.Writer, reader io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64

	for {
		readCount, readErr := reader.Read(buffer)
		if readCount > 0 {
			chunk := buffer[:readCount]
			written, writeErr := writer.Write(chunk)
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written < len(chunk) {
				return total, io.ErrShortWrite
			}
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

func copyRequestHeadersForUpstream(target http.Header, source http.Header) {
	for key, values := range source {
		lowerKey := strings.ToLower(key)
		if _, exists := hopByHopHeaders[lowerKey]; exists {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

func copyResponseHeadersToClient(target http.Header, source http.Header) {
	for key, values := range source {
		lowerKey := strings.ToLower(key)
		if _, exists := hopByHopHeaders[lowerKey]; exists {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

// 说明：shouldRewriteHost / BuildCursorChecksum / formatBearerAuthorization 已移除。
// 它们曾用于 local 模式覆盖 Authorization 为假 token 并重算 checksum；
// 现在真实凭证由 CredentialOriginalCursor 策略原样透传，不再需要伪造/重算。

func shouldRequestCarryBody(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		return false
	default:
		return true
	}
}

func marshalJSONBody(payload map[string]any) ([]byte, error) {
	if payload == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(payload)
}

func handleMockJSON(reqCtx *RequestContext, route *Route) error {
	responseBody, err := marshalJSONBody(route.JSONBody)
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/json")
	reqCtx.ResponseWriter.WriteHeader(route.StatusCode)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

func handleMockProto(reqCtx *RequestContext, route *Route) error {
	payload := map[string]any{}
	if route.MockPayloadBuilder != nil {
		built, err := route.MockPayloadBuilder(reqCtx)
		if err != nil {
			return err
		}
		payload = built
	}
	responseBody, err := encodeMockProto(route.MockProtoType, payload)
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/proto")
	reqCtx.ResponseWriter.Header().Del("content-encoding")
	reqCtx.ResponseWriter.Header().Set("content-length", strconv.Itoa(len(responseBody)))
	reqCtx.ResponseWriter.WriteHeader(route.StatusCode)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

// 说明：假 OAuth / Stripe / auth-poll / GetEmail mock handler 已全部移除。
// 这些接口现在走官方透传（真实 Cursor 账号），byok 不再伪造登录态与订阅信息。

func handleFixedStatus(reqCtx *RequestContext, route *Route) error {
	if route != nil && route.ConsoleLog {
		logger.Infof("backend server fixed-status route hit name=%s method=%s path=%s raw_url=%s status=%d", route.Name, reqCtx.Method, reqCtx.TargetURL.Path, reqCtx.RawURL, route.StatusCode)
	}
	writeFixedStatus(reqCtx, route.StatusCode)
	return nil
}

func writeFixedStatus(reqCtx *RequestContext, statusCode int) {
	if reqCtx == nil || reqCtx.ResponseWriter == nil {
		return
	}
	reqCtx.ResponseWriter.WriteHeader(statusCode)
}

func handleDirect(reqCtx *RequestContext, route *Route) error {
	_ = route
	_, err := ForwardToUpstream(reqCtx, ForwardOptions{})
	return err
}

func encodeMockProto(typeName string, payload map[string]any) ([]byte, error) {
	message, err := newProtoMessage(typeName)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, message); err != nil {
		return nil, fmt.Errorf("mock proto json decode failed: %w", err)
	}
	return proto.Marshal(message)
}

func newProtoMessage(typeName string) (proto.Message, error) {
	switch strings.TrimSpace(typeName) {
	case "aiserver.v1.ServerTimeResponse":
		return &aiserverv1.ServerTimeResponse{}, nil
	case "aiserver.v1.GetServerConfigResponse":
		return &aiserverv1.GetServerConfigResponse{}, nil
	case "aiserver.v1.AvailableModelsResponse":
		return &aiserverv1.AvailableModelsResponse{}, nil
	case "aiserver.v1.GetDefaultModelResponse":
		return &aiserverv1.GetDefaultModelResponse{}, nil
	case "aiserver.v1.GetDefaultModelNudgeDataResponse":
		return &aiserverv1.GetDefaultModelNudgeDataResponse{}, nil
	case "aiserver.v1.BootstrapStatsigResponse":
		return &aiserverv1.BootstrapStatsigResponse{}, nil
	case "aiserver.v1.GetFirstWindowStatsigDecisionResponse":
		return &aiserverv1.GetFirstWindowStatsigDecisionResponse{}, nil
	case "aiserver.v1.GetCurrentPeriodUsageResponse":
		return &aiserverv1.GetCurrentPeriodUsageResponse{}, nil
	case "aiserver.v1.GetTeamsResponse":
		return &aiserverv1.GetTeamsResponse{}, nil
	case "aiserver.v1.GetMeResponse":
		return &aiserverv1.GetMeResponse{}, nil
	case "aiserver.v1.GetUserPrivacyModeResponse":
		return &aiserverv1.GetUserPrivacyModeResponse{}, nil
	case "aiserver.v1.GetPlanInfoResponse":
		return &aiserverv1.GetPlanInfoResponse{}, nil
	case "aiserver.v1.GetUsageLimitStatusAndActiveGrantsResponse":
		return &aiserverv1.GetUsageLimitStatusAndActiveGrantsResponse{}, nil
	case "aiserver.v1.IsOnNewPricingResponse":
		return &aiserverv1.IsOnNewPricingResponse{}, nil
	case "aiserver.v1.GetManagedSkillsResponse":
		return &aiserverv1.GetManagedSkillsResponse{}, nil
	case "aiserver.v1.GetEffectiveUserPluginsResponse":
		return &aiserverv1.GetEffectiveUserPluginsResponse{}, nil
	case "aiserver.v1.ListMarketplacesResponse":
		return &aiserverv1.ListMarketplacesResponse{}, nil
	case "aiserver.v1.ListMarketplacePluginsResponse":
		return &aiserverv1.ListMarketplacePluginsResponse{}, nil
	case "aiserver.v1.RegisterMarketplaceAndPluginsResponse":
		return &aiserverv1.RegisterMarketplaceAndPluginsResponse{}, nil
	default:
		return nil, fmt.Errorf("unsupported proto message type %q", typeName)
	}
}
