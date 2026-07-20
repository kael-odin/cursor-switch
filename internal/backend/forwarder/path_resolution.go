package forwarder

import (
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

// resolveAndFenceWritePath 把模型给定的写入路径解析并围栏在工作区内。
// 规则：
//   - 路径必须解析后落在某 workspaceRoot 之下，或落在 terminalsFolder 之下；否则拒绝。
//   - 相对路径按各 workspaceRoot 解析；绝对路径需本身就在某 workspace 内。
//   - 不做"base 名后缀匹配"这类可能逃逸工作区的启发式——写入必须显式落在工作区。
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
		if pathWithinWorkspace(cleanedPath, tf) || cleanedPath == filepath.Clean(tf) {
			return cleanedPath, true
		}
	}

	if len(ctx.workspacePaths) == 0 {
		return "", false
	}

	// 绝对路径：必须本身已落在某 workspace 内。
	if filepath.IsAbs(cleanedPath) {
		for _, root := range ctx.workspacePaths {
			if pathWithinWorkspace(cleanedPath, root) {
				return cleanedPath, true
			}
		}
		return "", false
	}

	// 相对路径：按各 workspace 解析后落定。
	for _, root := range ctx.workspacePaths {
		candidate := filepath.Clean(filepath.Join(root, cleanedPath))
		if pathWithinWorkspace(candidate, root) {
			return candidate, true
		}
	}
	return "", false
}

// ensureWritePathWithinWorkspace 是面向 tool 调用方的包装：取 stream 上下文并围栏。
// 返回围栏后的绝对路径，或 error（调用方应作为可恢复的工具调用错误返回）。
//
// ponytail: 当 stream 既无 workspace 也无 terminals 上下文时（如异常 resume / 老对话），
// 围栏无法判定，此时回退到"仅要求绝对路径"的旧行为并放行，避免把用户锁死无法写入。
// 真正的防护发生在"有工作区上下文但路径在区外"时。upgrade path：若确认 Cursor 总是
// 携带 workspace，可在后续版本移除此回退，强制围栏。
func ensureWritePathWithinWorkspace(stream *ActiveStream, path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("write path is required")
	}
	ctx := snapshotStreamPathContext(stream)
	if len(ctx.workspacePaths) == 0 && strings.TrimSpace(ctx.terminalsFolder) == "" {
		if !isAbsoluteToolPath(trimmed) {
			return "", fmt.Errorf("write path must be absolute")
		}
		return filepath.Clean(trimmed), nil
	}
	resolved, ok := resolveAndFenceWritePath(trimmed, ctx)
	if !ok {
		return "", fmt.Errorf("refused write outside workspace: %s", trimmed)
	}
	return resolved, nil
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z')
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
