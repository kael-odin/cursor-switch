package forwarder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/gen/agentv1"
)

type streamPathContext struct {
	workspacePaths      []string
	terminalsFolder     string
	requestFileContents map[string]string
}

func updateStreamRequestContextData(stream *ActiveStream, requestContext *agentv1.RequestContext) {
	if stream == nil {
		return
	}

	var workspacePaths []string
	var terminalsFolder string
	var fileContents map[string]string
	if requestContext != nil {
		env := requestContext.GetEnv()
		workspacePaths = compactWorkspacePaths(env.GetWorkspacePaths(), env.GetProjectFolder())
		terminalsFolder = strings.TrimSpace(env.GetTerminalsFolder())
		fileContents = cloneStringMap(requestContext.GetFileContents())
	}

	stream.mu.Lock()
	stream.WorkspacePaths = workspacePaths
	stream.TerminalsFolder = terminalsFolder
	stream.RequestFileContents = fileContents
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func snapshotStreamPathContext(stream *ActiveStream) streamPathContext {
	if stream == nil {
		return streamPathContext{}
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()

	return streamPathContext{
		workspacePaths:      append([]string(nil), stream.WorkspacePaths...),
		terminalsFolder:     strings.TrimSpace(stream.TerminalsFolder),
		requestFileContents: cloneStringMap(stream.RequestFileContents),
	}
}

func compactWorkspacePaths(workspacePaths []string, projectFolder string) []string {
	items := append([]string(nil), workspacePaths...)
	if trimmedProjectFolder := strings.TrimSpace(projectFolder); trimmedProjectFolder != "" {
		items = append(items, trimmedProjectFolder)
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		result = append(result, cleaned)
	}
	return result
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		cloned[trimmedKey] = value
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func resolveWorkspacePath(path string, workspaceRoots []string, requireExisting bool) (string, bool) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "", false
	}
	cleanedPath := filepath.Clean(trimmedPath)
	if pathCandidateUsable(cleanedPath, requireExisting) {
		return cleanedPath, true
	}

	if len(workspaceRoots) == 0 {
		return "", false
	}

	if !filepath.IsAbs(cleanedPath) {
		for _, workspaceRoot := range workspaceRoots {
			candidate := filepath.Join(workspaceRoot, cleanedPath)
			if pathCandidateUsable(candidate, requireExisting) {
				return filepath.Clean(candidate), true
			}
		}
		return "", false
	}

	pathParts := splitPathParts(cleanedPath)
	if len(pathParts) == 0 {
		return "", false
	}

	for _, workspaceRoot := range workspaceRoots {
		rootBase := strings.TrimSpace(filepath.Base(workspaceRoot))
		if rootBase == "" || rootBase == "." || rootBase == string(filepath.Separator) {
			continue
		}
		if index := lastIndexFold(pathParts, rootBase); index >= 0 {
			candidate := workspaceRoot
			if index+1 < len(pathParts) {
				candidate = filepath.Join(append([]string{workspaceRoot}, pathParts[index+1:]...)...)
			}
			if pathCandidateUsable(candidate, requireExisting) {
				return filepath.Clean(candidate), true
			}
		}
	}

	for suffixLen := len(pathParts); suffixLen >= 1; suffixLen-- {
		suffixParts := append([]string(nil), pathParts[len(pathParts)-suffixLen:]...)
		for _, workspaceRoot := range workspaceRoots {
			candidate := joinWorkspaceRootWithSuffix(workspaceRoot, suffixParts)
			if pathCandidateUsable(candidate, requireExisting) {
				return filepath.Clean(candidate), true
			}
		}
	}

	return "", false
}

func isAbsoluteToolPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "/") {
		return true
	}
	if strings.HasPrefix(trimmed, `\\`) {
		return true
	}
	return len(trimmed) >= 3 && isASCIIAlpha(trimmed[0]) && trimmed[1] == ':' && isPathSeparator(trimmed[2])
}

// pathWithinWorkspace 判断 resolved 是否落在 root 之下（含 root 自身）。
// 跨盘绝对路径（如 root=C:\ws, resolved=D:\x）会被 filepath.Rel 判为相对路径而不被接受。
//
// 注意：本函数只做词法 Clean/Rel 比较，不解析 symlink——workspace 内指向区外的
// symlink 会绕过本检查。对写入/删除/下载这类副作用入口，应使用 pathWithinWorkspaceReal
// 做 realpath 校验。本词法版仍用于"路径尚不存在"等无法 EvalSymlinks 的场景。
func pathWithinWorkspace(resolved string, root string) bool {
	cleanedResolved := filepath.Clean(strings.TrimSpace(resolved))
	cleanedRoot := filepath.Clean(strings.TrimSpace(root))
	if cleanedResolved == "" || cleanedRoot == "" {
		return false
	}
	rel, err := filepath.Rel(cleanedRoot, cleanedResolved)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// rel 形如 "sub/file" 在工作区内；".." 或 "../..." 表示逃逸出 root。
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

// pathWithinWorkspaceReal 是 F-32 的 realpath-aware 围栏：对候选路径与 root 都
// 解析 symlink 后再做 pathWithinWorkspace 比较。
//
// 修复要点：此前的词法 Clean/Rel 比较，对"workspace 内 symlink → 区外真实路径"
// 无能为力——模型可在工作区内放一个指向 ~/.ssh 的软链，词法围栏判其位于工作区内
// 而放行，实际写入/删除落到区外。本函数对两端 EvalSymlinks 后再比较，堵住该旁路。
//
// 降级策略：root 不存在（os.ErrNotExist）时退回词法 pathWithinWorkspace——root 不在
// 磁盘上就不可能有 symlink 逃逸，词法校验已足够，且这让"root 尚未创建"的合法场景
// 不被 fail-closed 误伤。root 存在但解析失败（权限/IO 错误）仍 fail-closed。
// 候选路径可能尚不存在（写入新文件、删除已删文件）——EvalSymlinks 对缺失路径会
// 沿父目录解析到最近存在的祖先再拼接，返回该祖先的 realpath + 剩余词法段。
func pathWithinWorkspaceReal(candidate string, root string) bool {
	cleanedCandidate := filepath.Clean(strings.TrimSpace(candidate))
	cleanedRoot := filepath.Clean(strings.TrimSpace(root))
	if cleanedCandidate == "" || cleanedRoot == "" {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(cleanedRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// root 不存在——无 symlink 逃逸面，退回词法校验。
			return pathWithinWorkspace(cleanedCandidate, cleanedRoot)
		}
		// root 存在但不可解析（权限/IO）——fail-closed。
		return false
	}
	realRoot = filepath.Clean(realRoot)
	realCandidate, err := evalSymlinksAncestorAware(cleanedCandidate)
	if err != nil {
		// 候选路径完全不可解析（连祖先都不可达）——fail-closed。
		return false
	}
	return pathWithinWorkspace(realCandidate, realRoot)
}

// evalSymlinksAncestorAware 对可能不存在的路径做 EvalSymlinks：从最深层开始尝试，
// 失败则回退到最近存在的祖先目录的 realpath + 剩余词法段。
// 这让"写入一个尚不存在的文件"也能得到 realpath 校验（其父目录的 symlink 被解析）。
func evalSymlinksAncestorAware(path string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return "", fmt.Errorf("empty path")
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return filepath.Clean(resolved), nil
	}
	// 回退：逐层上找最近可解析的祖先，拼回剩余段。
	dir := filepath.Dir(cleaned)
	tail := filepath.Base(cleaned)
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Clean(filepath.Join(filepath.Clean(resolved), tail)), nil
		}
		nextDir := filepath.Dir(dir)
		if nextDir == dir {
			// 到根仍未可解析——fail-closed。
			return "", fmt.Errorf("no resolvable ancestor for %s", path)
		}
		tail = filepath.Join(filepath.Base(dir), tail)
		dir = nextDir
	}
}

// resolveAndFenceWritePath 把模型给定的写入路径解析并围栏在工作区内。
// 规则：
//   - 路径必须 realpath 解析后落在某 workspaceRoot 之下，或落在 terminalsFolder 之下；否则拒绝。
//   - 相对路径按各 workspaceRoot 解析；绝对路径需本身就在某 workspace 内。
//   - 不做"base 名后缀匹配"这类可能逃逸工作区的启发式——写入必须显式落在工作区。
//   - F-32：使用 pathWithinWorkspaceReal 解析 symlink，堵住"workspace 内 symlink → 区外"旁路。
//
// 返回围栏后的绝对路径；ok=false 时调用方必须拒绝该写入。
func resolveAndFenceWritePath(path string, ctx streamPathContext) (string, bool) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "", false
	}
	cleanedPath := filepath.Clean(trimmedPath)

	// terminalsFolder 是显式允许的写入区（终端快照文件），单独放行。
	if tf := strings.TrimSpace(ctx.terminalsFolder); tf != "" {
		if pathWithinWorkspaceReal(cleanedPath, tf) || filepath.Clean(cleanedPath) == filepath.Clean(tf) {
			return cleanedPath, true
		}
	}

	if len(ctx.workspacePaths) == 0 {
		return "", false
	}

	// 绝对路径：必须本身已 realpath 落在某 workspace 内。
	if filepath.IsAbs(cleanedPath) {
		for _, root := range ctx.workspacePaths {
			if pathWithinWorkspaceReal(cleanedPath, root) {
				return cleanedPath, true
			}
		}
		return "", false
	}

	// 相对路径：按各 workspace 解析后 realpath 落定。
	for _, root := range ctx.workspacePaths {
		candidate := filepath.Clean(filepath.Join(root, cleanedPath))
		if pathWithinWorkspaceReal(candidate, root) {
			return candidate, true
		}
	}
	return "", false
}

// ensureWritePathWithinWorkspace 是面向 tool 调用方的包装：取 stream 上下文并围栏。
// 返回围栏后的绝对路径，或 error（调用方应作为可恢复的工具调用错误返回）。
//
// F-32：当 stream 既无 workspace 也无 terminals 上下文时（异常 resume / 老对话），
// fail-closed 拒绝写入——此前回退"仅要求绝对路径"放行任意绝对路径，是审计点名的旁路。
// 真正的防护发生在"有工作区上下文但路径在区外"与"workspace 内 symlink → 区外"两条。
func ensureWritePathWithinWorkspace(stream *ActiveStream, path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("write path is required")
	}
	ctx := snapshotStreamPathContext(stream)
	if len(ctx.workspacePaths) == 0 && strings.TrimSpace(ctx.terminalsFolder) == "" {
		return "", fmt.Errorf("refused write: workspace context missing (F-32 fail-closed): %s", trimmed)
	}
	resolved, ok := resolveAndFenceWritePath(trimmed, ctx)
	if !ok {
		return "", fmt.Errorf("refused write outside workspace: %s", trimmed)
	}
	return resolved, nil
}

// ensureDeletePathWithinWorkspace 围栏 Delete 的路径（F-32）。
// Delete 此前在 events.go/service.go 原样下发到 exec bridge，未做围栏——模型可删除
// 工作区外文件（绝对路径或 workspace 内 symlink → 区外）。本函数复用 write 围栏语义：
// 必须落在 workspace 或 terminalsFolder 之下，否则 fail-closed 拒绝。
func ensureDeletePathWithinWorkspace(stream *ActiveStream, path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("delete path is required")
	}
	ctx := snapshotStreamPathContext(stream)
	if len(ctx.workspacePaths) == 0 && strings.TrimSpace(ctx.terminalsFolder) == "" {
		return "", fmt.Errorf("refused delete: workspace context missing (F-32 fail-closed): %s", trimmed)
	}
	resolved, ok := resolveAndFenceWritePath(trimmed, ctx)
	if !ok {
		return "", fmt.Errorf("refused delete outside workspace: %s", trimmed)
	}
	return resolved, nil
}

// ensureDownloadPathWithinWorkspace 围栏 FetchMcpResource.downloadPath（F-32）。
// downloadPath 此前在 exec/bridge.go:openReadMcpResource 原样下发，未拒绝对路径或 `..`——
// 模型可把 MCP resource 下载到工作区外（如覆盖 ~/.ssh/authorized_keys）。
// 本函数要求 downloadPath 非空时必须落在 workspace 之下；空 downloadPath（流式返回）放行。
func ensureDownloadPathWithinWorkspace(stream *ActiveStream, path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		// 空 downloadPath 表示流式返回内容、不落盘——合法。
		return "", nil
	}
	ctx := snapshotStreamPathContext(stream)
	if len(ctx.workspacePaths) == 0 && strings.TrimSpace(ctx.terminalsFolder) == "" {
		return "", fmt.Errorf("refused download path: workspace context missing (F-32 fail-closed): %s", trimmed)
	}
	resolved, ok := resolveAndFenceWritePath(trimmed, ctx)
	if !ok {
		return "", fmt.Errorf("refused download path outside workspace: %s", trimmed)
	}
	return resolved, nil
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z')
}

// extractDeletePath 从 Delete 工具调用的 ArgsJSON 中取 path 字段（F-32）。
// 解析失败返回空串（围栏会因空 path 拒绝），不阻断流——围栏层负责 fail-closed。
func extractDeletePath(argsJSON []byte) string {
	var input struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(argsJSON, &input)
	return strings.TrimSpace(input.Path)
}

// rewriteDeletePath 把围栏后的 path 写回 ArgsJSON（F-32）。
// 用 map 通用解码以保留未知字段，只覆盖 path。
func rewriteDeletePath(argsJSON []byte, fencedPath string) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(argsJSON, &raw); err != nil {
		return nil, fmt.Errorf("decode Delete args for rewrite failed: %w", err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	raw["path"] = fencedPath
	return json.Marshal(raw)
}

// extractDownloadPath 从 FetchMcpResource 工具调用的 ArgsJSON 中取 downloadPath（F-32）。
func extractDownloadPath(argsJSON []byte) string {
	var input struct {
		DownloadPath string `json:"downloadPath,omitempty"`
	}
	_ = json.Unmarshal(argsJSON, &input)
	return strings.TrimSpace(input.DownloadPath)
}

// rewriteDownloadPath 把围栏后的 downloadPath 写回 ArgsJSON（F-32）。
// 空 fencedPath（流式返回、不落盘）时移除 downloadPath 键，避免下发空串。
func rewriteDownloadPath(argsJSON []byte, fencedPath string) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(argsJSON, &raw); err != nil {
		return nil, fmt.Errorf("decode FetchMcpResource args for rewrite failed: %w", err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	if strings.TrimSpace(fencedPath) == "" {
		delete(raw, "downloadPath")
	} else {
		raw["downloadPath"] = fencedPath
	}
	return json.Marshal(raw)
}

func isPathSeparator(value byte) bool {
	return value == '\\' || value == '/'
}

func lookupRequestFileContents(context streamPathContext, originalPath string, resolvedPath string) (string, bool) {
	if len(context.requestFileContents) == 0 {
		return "", false
	}
	aliases := buildPathAliases(originalPath, resolvedPath, context.workspacePaths)
	for _, alias := range aliases {
		if value, ok := context.requestFileContents[alias]; ok {
			return value, true
		}
	}
	return "", false
}

func buildPathAliases(originalPath string, resolvedPath string, workspaceRoots []string) []string {
	seen := make(map[string]struct{}, 16)
	aliases := make([]string, 0, 16)
	addAlias := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		aliases = append(aliases, trimmed)
	}

	for _, candidate := range []string{originalPath, resolvedPath} {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		addAlias(trimmed)
		addAlias(filepath.Clean(trimmed))
		addAlias(filepath.ToSlash(filepath.Clean(trimmed)))
	}

	for _, candidate := range []string{originalPath, resolvedPath} {
		cleaned := filepath.Clean(strings.TrimSpace(candidate))
		if cleaned == "" {
			continue
		}
		for _, workspaceRoot := range workspaceRoots {
			if rel, err := filepath.Rel(workspaceRoot, cleaned); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
				addAlias(rel)
				addAlias(filepath.ToSlash(rel))
			}
		}
	}

	return aliases
}

func splitPathParts(path string) []string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return nil
	}
	volumeName := filepath.VolumeName(cleaned)
	if volumeName != "" {
		cleaned = strings.TrimPrefix(cleaned, volumeName)
	}
	cleaned = strings.TrimPrefix(cleaned, string(filepath.Separator))
	if cleaned == "" {
		return nil
	}
	parts := strings.Split(cleaned, string(filepath.Separator))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func lastIndexFold(items []string, target string) int {
	for index := len(items) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(items[index]), strings.TrimSpace(target)) {
			return index
		}
	}
	return -1
}

func joinWorkspaceRootWithSuffix(workspaceRoot string, suffixParts []string) string {
	root := filepath.Clean(strings.TrimSpace(workspaceRoot))
	if root == "" {
		return ""
	}
	parts := append([]string(nil), suffixParts...)
	if len(parts) > 0 && strings.EqualFold(parts[0], filepath.Base(root)) {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return root
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

func pathCandidateUsable(path string, requireExisting bool) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return false
	}
	info, err := os.Stat(cleaned)
	if err == nil {
		return !info.IsDir() || requireExisting
	}
	if requireExisting {
		return false
	}
	parent := filepath.Dir(cleaned)
	if parent == "" || parent == "." || parent == cleaned {
		return false
	}
	parentInfo, parentErr := os.Stat(parent)
	return parentErr == nil && parentInfo.IsDir()
}
