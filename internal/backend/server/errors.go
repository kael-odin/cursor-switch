package server

import "errors"

var ErrInvalidBidiAppendPayload = errors.New("invalid bidi append payload")

// ErrCompatRouteBodyTooLarge 表示 compat 路由入站 body 超过上限（F-28），由 writeServerError 映射为 413。
var ErrCompatRouteBodyTooLarge = errors.New("compat route body too large")
