package upstream

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cursor/internal/backend/server"
)

// maxCompatRouteBodyBytes 限制 compat 路由读取的入站 body 大小（F-28）。
// 此前用无界 io.ReadAll(ctx.Request.Body)；恶意/超大请求可耗尽内存。
// 32 MiB 足以覆盖正常 chat completion 请求（含长上下文 + 图片 base64）。
const maxCompatRouteBodyBytes = 32 * 1024 * 1024

type CompatRouteConfig struct {
	Name          string
	StatusCode    int
	JSONBody      map[string]any
	MockProtoType string
	MockBuilder   func(*RequestContext) (map[string]any, error)
	ConsoleLog    bool
	// Credential 作用于 DirectAction：指定回源上游时的凭证策略。
	// CredentialOriginalCursor 恢复真实 Cursor 登录态（仅原始 HTTPS *.cursor.sh 生效），
	// 供 marketplace / 账号 / customize 等官方控制面接口透传登录态。
	Credential CredentialPolicy
	// Stream 标记该路由是流式响应（SSE / Connect 流，响应体可持续数分钟）。
	// 为 true 时，CredentialOriginalCursor 路径改用无端到端超时的流式 NoRedirect 客户端，
	// 避免 http.Client.Timeout 在到点时把正在进行的流截断（run_sse/bidi_append 切 upstream 时必填）。
	// 非流式控制面接口不受影响，仍用带 30s 超时的 NewHTTPClientNoRedirect。
	Stream bool
}

func DirectAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		_ = route
		_, err = ForwardToUpstream(reqCtx, ForwardOptions{Credential: cfg.Credential, Stream: cfg.Stream})
		return err
	}
}

func FixedStatusAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleFixedStatus(reqCtx, route)
	}
}

func MockJSONAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockJSON(reqCtx, route)
	}
}

func MockProtoAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockProto(reqCtx, route)
	}
}

func newCompatRouteObjects(ctx *server.Context, deps Dependencies, cfg CompatRouteConfig) (*RequestContext, *Route, error) {
	if ctx == nil || ctx.Request == nil {
		return nil, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(ctx.Request.Body, maxCompatRouteBodyBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > maxCompatRouteBodyBytes {
		return nil, nil, fmt.Errorf("compat route body exceeds %d bytes: %w", maxCompatRouteBodyBytes, server.ErrCompatRouteBodyTooLarge)
	}
	ctx.Request.Body = io.NopCloser(bytes.NewReader(body))
	targetURL := ctx.UpstreamURL
	if targetURL == nil && ctx.Request.URL != nil {
		copyURL := *ctx.Request.URL
		targetURL = &copyURL
	}
	reqCtx := &RequestContext{
		ResponseWriter: ctx.Writer,
		Request:        ctx.Request,
		StartedAt:      ctx.StartedAt,
		RawURL:         strings.TrimSpace(ctx.Request.Header.Get(server.HeaderServerUpstreamURL)),
		TargetURL:      targetURL,
		Method:         strings.ToUpper(strings.TrimSpace(ctx.Request.Method)),
		Headers:        ctx.Request.Header.Clone(),
		ContentType:    strings.TrimSpace(ctx.Request.Header.Get("content-type")),
		RequestBody:    body,
		Mode:           ctx.Mode,
		Deps:           &deps,
		HTTPRequestID:  resolveHTTPRequestID(ctx.Request),
		Credentials:    ctx.Credentials,
	}
	route := &Route{
		Name:               cfg.Name,
		Pattern:            ctx.Request.URL.Path,
		StatusCode:         cfg.StatusCode,
		JSONBody:           cfg.JSONBody,
		MockProtoType:      cfg.MockProtoType,
		MockPayloadBuilder: cfg.MockBuilder,
		ConsoleLog:         cfg.ConsoleLog,
	}
	return reqCtx, route, nil
}

func ServerTimeMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildServerTimePayload(reqCtx)
}

func ServerConfigMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildServerConfigPayload(reqCtx)
}

func AvailableModelsMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildAvailableModelsPayload(reqCtx)
}

func DefaultModelNudgeMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDefaultModelNudgeDataPayload(reqCtx)
}

func GetDefaultModelMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildGetDefaultModelPayload(reqCtx)
}

// 权益接口 mock（无限制），防止真实账号套餐锁定模型选择器。
func DashboardUsageLimitStatusAndActiveGrantsMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardUsageLimitStatusAndActiveGrantsPayload(reqCtx)
}

func DashboardPlanInfoMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardPlanInfoPayload(reqCtx)
}

func DashboardCurrentPeriodUsageMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardCurrentPeriodUsagePayload(reqCtx)
}

func DashboardGetMeMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardGetMePayload(reqCtx)
}

func DashboardIsOnNewPricingMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardIsOnNewPricingPayload(reqCtx)
}

func BootstrapStatsigMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildBootstrapStatsigPayload(reqCtx)
}

func FirstWindowStatsigDecisionMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildFirstWindowStatsigDecisionPayload(reqCtx)
}

// 说明：Dashboard 账号/套餐/marketplace 相关的 MockBuilder 已全部移除。
// 这些接口现在走官方透传（host.go 的 officialProcedure/officialAnyProcedure），
// 由真实 Cursor 账号响应，不再由 byok 伪造。仅保留模型/兼容层与 Statsig 的本地 mock builder。

func resolveHTTPRequestID(request *http.Request) string {
	requestID := strings.TrimSpace(request.Header.Get("x-request-id"))
	if requestID != "" {
		return requestID
	}
	return strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339Nano), ":", "-")
}
