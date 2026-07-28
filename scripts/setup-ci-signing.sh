#!/usr/bin/env bash
# 一键配置 CI 自动签名（方案 A）：把本地私钥推到 GitHub repo secret，
# 并打开 CURSOR_SIGNING_ENABLED 开关。之后每次打 tag 发版，CI 自动签名 update.json。
#
# 幂等：重复跑只更新 secret/variable 的值，不会产生副作用。
#
# 前置：本地已装 gh CLI 并登录（gh auth status 通过，账号对 kael-odin/cursor-switch 有 admin）。
# 用法：bash scripts/setup-ci-signing.sh [私钥路径，默认 ~/.cursor-switch-release.key]
set -euo pipefail

KEY_PATH="${1:-$HOME/.cursor-switch-release.key}"
REPO="kael-odin/cursor-switch"

if [ ! -f "$KEY_PATH" ]; then
  echo "❌ 私钥不存在：$KEY_PATH" >&2
  echo "   先生成：go run ./scripts/release keypair（再按 RELEASE_SIGNING.md 填公钥到 pubkey.go）" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "❌ 未找到 gh CLI，请先安装：https://cli.github.com/" >&2
  exit 1
fi

# 校验 gh 已登录且对目标 repo 有权限
if ! gh auth status >/dev/null 2>&1; then
  echo "❌ gh 未登录，先跑：gh auth login" >&2
  exit 1
fi

# 去掉所有空白/换行，secret 值必须是纯净 hex
KEY_HEX="$(tr -d '[:space:]' < "$KEY_PATH")"
if [ -z "$KEY_HEX" ]; then
  echo "❌ 私钥文件为空：$KEY_PATH" >&2
  exit 1
fi

echo "→ 推送私钥到 repo secret CURSOR_SIGNING_KEY（值不回显）..."
gh secret set CURSOR_SIGNING_KEY --repo "$REPO" --body "$KEY_HEX"

echo "→ 设置开关 variable CURSOR_SIGNING_ENABLED=true..."
gh variable set CURSOR_SIGNING_ENABLED --repo "$REPO" --body "true"

echo ""
echo "✅ CI 自动签名已配置："
echo "   secret   CURSOR_SIGNING_KEY        （私钥 hex，已加密存储于 GitHub）"
echo "   variable CURSOR_SIGNING_ENABLED=true（CI 签名总开关）"
echo ""
echo "   下次打 tag 发版（git push origin vX.Y.Z），CI 会自动签名 update.json 并发布到 release。"
echo "   验证：发版后在 release 资产里看 update.json 是否含 \"signature\" 字段。"
echo ""
echo "撤销（切回本地补签）：gh variable set CURSOR_SIGNING_ENABLED --repo $REPO --body false"
