package forwarder

import "testing"

func TestResolveAndFenceWritePath(t *testing.T) {
	windowsRoot := `C:\Users\dev\workspace`
	unixRoot := "/home/dev/workspace"
	terminals := unixRoot + "/.terminals"

	tests := []struct {
		name     string
		path     string
		ctx      streamPathContext
		wantOK   bool
		wantSub  string // resolved must have this suffix (path-sep agnostic) when ok
	}{
		{
			name: "relative path inside unix workspace",
			path: "src/main.go",
			ctx:  streamPathContext{workspacePaths: []string{unixRoot}},
			wantOK: true,
			wantSub: "src/main.go",
		},
		{
			name: "absolute path inside unix workspace",
			path: unixRoot + "/src/main.go",
			ctx:  streamPathContext{workspacePaths: []string{unixRoot}},
			wantOK: true,
		},
		{
			name: "absolute path inside windows workspace",
			path: windowsRoot + `\src\main.go`,
			ctx:  streamPathContext{workspacePaths: []string{windowsRoot}},
			wantOK: true,
		},
		{
			name: "parent traversal rejected",
			path: "../../../etc/passwd",
			ctx:  streamPathContext{workspacePaths: []string{unixRoot}},
			wantOK: false,
		},
		{
			// On Windows, a unix-style absolute path is treated as relative by filepath.IsAbs,
			// so it joins under the workspace — not an escape. The real cross-platform escape
			// case (ssh path) is covered by the windows-root variant below.
			name: "cross-drive absolute path rejected",
			path: `C:\Windows\System32\drivers\etc\hosts`,
			ctx:  streamPathContext{workspacePaths: []string{unixRoot}},
			wantOK: false,
		},
		{
			name: "windows workspace rejects ssh path",
			path: `C:\Users\dev\.ssh\authorized_keys`,
			ctx:  streamPathContext{workspacePaths: []string{windowsRoot}},
			wantOK: false,
		},
		{
			name: "windows workspace rejects system32 path",
			path: `C:\Windows\System32\evil.exe`,
			ctx:  streamPathContext{workspacePaths: []string{windowsRoot}},
			wantOK: false,
		},
		{
			name: "terminals folder allowed",
			path: terminals + "/shell-1.json",
			ctx:  streamPathContext{workspacePaths: []string{unixRoot}, terminalsFolder: terminals},
			wantOK: true,
		},
		{
			name: "no workspace roots rejects",
			path: unixRoot + "/src/main.go",
			ctx:  streamPathContext{},
			wantOK: false,
		},
		{
			name: "empty path rejected",
			path: "   ",
			ctx:  streamPathContext{workspacePaths: []string{unixRoot}},
			wantOK: false,
		},
		{
			name: "absolute path equals workspace root allowed",
			path: unixRoot,
			ctx:  streamPathContext{workspacePaths: []string{unixRoot}},
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveAndFenceWritePath(tc.path, tc.ctx)
			if ok != tc.wantOK {
				t.Fatalf("resolveAndFenceWritePath(%q) ok=%v want=%v resolved=%q", tc.path, ok, tc.wantOK, got)
			}
			if ok && tc.wantSub != "" {
				if !hasSuffixPath(got, tc.wantSub) {
					t.Fatalf("resolveAndFenceWritePath(%q) resolved=%q want suffix %q", tc.path, got, tc.wantSub)
				}
			}
		})
	}
}

// hasSuffixPath compares path suffix agnostic to separator.
func hasSuffixPath(full, suffix string) bool {
	if len(full) < len(suffix) {
		return false
	}
	tail := full[len(full)-len(suffix):]
	if tail == suffix {
		return true
	}
	// normalize separators
	norm := func(s string) string {
		out := make([]byte, 0, len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '\\' {
				c = '/'
			}
			out = append(out, c)
		}
		return string(out)
	}
	return norm(tail) == norm(suffix)
}
