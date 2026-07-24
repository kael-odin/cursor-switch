package server

import (
	"fmt"
	"net/url"
	"strings"
)

func ParseAndValidateRawURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, fmt.Errorf("empty raw url")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, fmt.Errorf("empty host")
	}
	// 拒绝携带 userinfo 的 URL，避免凭证绑定与目标校验被 user:pass@host 形式绕过。
	if parsed.User != nil {
		return nil, fmt.Errorf("raw url must not contain userinfo")
	}
	return parsed, nil
}
