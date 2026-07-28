package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLineWriterTruncatesLongEntry (F-28) 验证单条超长 log 被截断到 maxLogEntryBytes
// 并以 truncation 后缀 + 换行结尾，不把整条超长 payload 写进文件、不触发全量 ReadFile trim。
func TestLineWriterTruncatesLongEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	writer, err := newLineWindowFileWriter(path, appLogMaxLines, appLogTrimReserveLine)
	if err != nil {
		t.Fatalf("newLineWindowFileWriter: %v", err)
	}

	// 构造一条无换行的超长 payload（> maxLogEntryBytes）。
	oversized := make([]byte, maxLogEntryBytes+4096)
	for i := range oversized {
		oversized[i] = 'x'
	}

	n, err := writer.Write(oversized)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// 返回值按原始 payload 长度上报，让上层 slog 视为已全部消费。
	if n != len(oversized) {
		t.Errorf("F-28: expected Write to report %d bytes, got %d", len(oversized), n)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if !strings.HasSuffix(string(data), logEntryTruncationSuffix) {
		t.Errorf("F-28 FAIL: log entry should end with truncation suffix, got tail=%q",
			string(data[len(data)-len(logEntryTruncationSuffix)-16:]))
	}
	// 截断后总长度 = maxLogEntryBytes + len(suffix)，不应包含原始超长尾部。
	expectedLen := maxLogEntryBytes + len(logEntryTruncationSuffix)
	if len(data) != expectedLen {
		t.Errorf("F-28 FAIL: expected file len=%d, got %d", expectedLen, len(data))
	}
	// openLine 应为 false：截断后强制以 \n 结尾。
	if writer.openLine {
		t.Error("F-28 FAIL: openLine should be false after truncated entry ends with \\n")
	}
	// lineCount 应为 1（截断后补的那一行）。
	if writer.lineCount != 1 {
		t.Errorf("F-28 FAIL: expected lineCount=1 after single truncated entry, got %d", writer.lineCount)
	}
	// 关闭文件句柄，让 t.TempDir 清理可删文件。
	if writer.file != nil {
		_ = writer.file.Close()
	}
}

// TestLineWriterKeepsShortEntry (F-28 回归) 验证不超限的短 entry 原样写入、不被截断。
func TestLineWriterKeepsShortEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writer, err := newLineWindowFileWriter(path, appLogMaxLines, appLogTrimReserveLine)
	if err != nil {
		t.Fatalf("newLineWindowFileWriter: %v", err)
	}

	entry := []byte("short log line\n")
	if _, err := writer.Write(entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != string(entry) {
		t.Errorf("F-28 regression: short entry should be written verbatim, got %q", string(data))
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("short entry should keep its trailing newline")
	}
	if writer.file != nil {
		_ = writer.file.Close()
	}
}
