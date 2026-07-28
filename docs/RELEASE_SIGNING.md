# 发版与签名操作指南

> 本文档面向维护者。说明发版流程、如何启用 update.json 的强制签名,以及旧 CA 历史残留的处理判断。

---

## 一、发版前置(版本号三处合一前,先手动同步)

> 注:版本号三处合一的自动化改造在重构清单里,改造完成前需手动保证三处一致。

发布前,手动改这三处为同一版本号(例如 `0.0.41`):

| 文件 | 字段 |
|---|---|
| `build/config.yml` | `info.version` |
| `build/windows/info.json` | `file_version` 与 `ProductVersion` |
| `build/windows/wails.exe.manifest` | `version` |

三处不一致会导致:release 资产按 tag 版本命名,但二进制自报 config.yml 版本,自动更新判断会错位。

然后更新 `release-notes.md` 写本次变更,提交并推送到 `main`。

## 二、触发发布(二选一)

```bash
# 方式一:手动触发(指定版本号,不带前导 v)
gh workflow run release.yml -f version=0.0.41

# 方式二:打 tag 自动触发
git tag v0.0.41
git push origin v0.0.41
```

版本号含 `beta` 字样时自动标记为 prerelease。

---

## 三、启用 update.json 强制签名(关键安全项)

### 背景

自动更新默认只校验下载包的 SHA256 校验和(checksum)。这只能防传输损坏,**不能防** GitHub release token 泄露后被注入恶意 `update.json` + 同谋 checksum,从而给所有自动更新用户推恶意代码。

强制签名后:`update.json` 带 ed25519 签名,二进制内置公钥校验,签名不过一律拒绝更新。即使 release token 泄露,攻击者没有私钥就无法伪造可被接受的更新。

> **当前状态(2026-07-28)：已启用 + CI 自动签名（已彻底切换，本地补签仅作应急）。** 公钥已填入 `internal/updater/pubkey.go`(`releasePublicKeyHex`)。私钥作为 GitHub repo secret `CURSOR_SIGNING_KEY` 存储（加密），维护者本地 `~/.cursor-switch-release.key`(0600，不入库) 仅作应急本地补签用。
>
> **CI 自动签名（方案 A，当前唯一常规路径）**：把私钥作为 GitHub repo secret `CURSOR_SIGNING_KEY` 存储，配 variable `CURSOR_SIGNING_ENABLED=true` 后，每次打 tag 发版 CI 自动签名 + 自检 verify 后把带签名的 `update.json` 发布到 release——**零本地操作**。一键配置见第三节（`bash scripts/setup-ci-signing.sh`）。应急时（`CURSOR_SIGNING_ENABLED=false`）CI 丢弃 `update.json`，需本地补签（见"应急本地补签"）。
>
> **F-11 姿态变化说明（重要）**：F-11 审计项的原始姿态是"CI 不持有私钥"——签名只在维护者本地完成。当前采用 CI 自动签名把私钥放进了 Actions secret，这是**用便利换安全的已知降级**：泄露面从"仅本地"扩大到"本地 + GitHub secret"，任何能 push 恶意 workflow 的人可 exfil 私钥。单人维护 fork、branch protection 开启时判断可接受。ed25519 公私钥分离本身未破，只是"谁能触发使用私钥的 workflow"扩大了。审计 md 中 F-11 已标注此姿态变化。
>
> 改名迁移说明(2026-07-26):产品由 `cursor-byok` 改名为 `cursor-switch`，签名私钥默认路径从 `~/.cursor-byok-release.key` 迁移到 `~/.cursor-switch-release.key`。**公钥不变**（`pubkey.go` 的 `releasePublicKeyHex` 未改），所以无需重新生成密钥对。维护者只需把现有私钥复制到新路径即可：
> ```bash
> cp ~/.cursor-byok-release.key ~/.cursor-switch-release.key
> chmod 600 ~/.cursor-switch-release.key
> ```
> 或在签名时显式指定旧路径：`go run ./scripts/release sign --key ~/.cursor-byok-release.key --manifest ...`

### 启用步骤(只需做一次)

**第 1 步:生成密钥对**

```bash
go run ./scripts/release keypair
```

输出示例:
```
private key written: /home/you/.cursor-switch-release.key (KEEP SECRET — never commit)
public key (hex): b7787d81f16782b485c3c659b2ede84164b514babb57c2c3818013f8e0ba8103
```

- 私钥 `~/.cursor-switch-release.key`(权限 0600),**永远不要提交、不要分享、不要丢**。丢了就得重新生成公钥并发新版强制所有用户更新到带新公钥的版本。
- 命令拒绝覆盖已存在的私钥(防误删旧 key 导致签名断裂)。
- 把公钥 hex 复制下来,进第 2 步。

**第 2 步:填入公钥**

编辑 `internal/updater/pubkey.go`,把 `releasePublicKeyHex` 改成你的公钥:

```go
var releasePublicKeyHex = "b7787d81f16782b485c3c659b2ede84164b514babb57c2c3818013f8e0ba8103"
```

提交这个改动并发布。从这一版起,客户端内置了公钥,可以校验签名。

**第 3 步:给每次发版的 update.json 签名**

CI 生成的 `update.json` 默认是未签名的。两种方式签名发布，任选其一：

#### CI 自动签名（当前唯一常规路径，零本地操作）

把私钥作为 GitHub repo secret 存储，配一个开关 variable，之后每次打 tag 发版 CI 全自动签名 + 自检后发布。**一次性配置，永久受益。**

一键配置脚本（本地已装 gh CLI 并登录 kael-odin 即可）：

```bash
bash scripts/setup-ci-signing.sh
# 或指定私钥路径：bash scripts/setup-ci-signing.sh ~/.cursor-switch-release.key
```

脚本做的事：
1. `gh secret set CURSOR_SIGNING_KEY` —— 把 `~/.cursor-switch-release.key` 内容推到 repo secret（加密存储）
2. `gh variable set CURSOR_SIGNING_ENABLED=true` —— 打开 CI 签名总开关

配好后，release.yml 的 `Sign or discard update.json` 步会在生成 manifest 后用 env 注入的私钥签名 + `verify` 子命令自检，再把带签名的 `update.json` 发布到 release 资产。**之后打 `git push origin vX.Y.Z` 就结束，无需任何本地命令。**

> 安全取舍（已知降级，详见顶部 F-11 姿态变化说明）：GitHub secret 加密存储、不进日志，但泄露面从"仅本地"扩大到"本地 + GitHub secret"——任何能 push 恶意 workflow 到 `main` 的人可 exfil 私钥。单人维护 fork、branch protection 开启时判断可接受。F-11 的实质目标（消除未签名 manifest 公开窗口）被更好达成——再也不存在"维护者忘记补签"的窗口。应急时 `gh variable set CURSOR_SIGNING_ENABLED --repo kael-odin/cursor-switch --body false` 关闭 CI 签名（CI 丢弃 update.json，需本地补签，见"应急本地补签"）。

应急关闭 CI 签名（怀疑 secret 泄露或 CI 签名出问题时）：

```bash
gh variable set CURSOR_SIGNING_ENABLED --repo kael-odin/cursor-switch --body false
```

#### 应急本地补签（仅当 CI 签名关闭时）

正常发版不需要此步骤。仅当 `CURSOR_SIGNING_ENABLED=false`（应急关闭 CI 签名，如怀疑 secret 泄露或 CI 签名出问题）且需要给某版本签名时，本地补签：

```bash
# 0. 找到 release 对应的 workflow run（tag=vX.Y.Z 触发的那次）
# 1. CI 在关闭签名模式下会丢弃 update.json，需本地重新生成 manifest（需先 checkout 对应 tag 并构建产物到 bin/release/<VERSION>）
go run ./scripts/release manifest -config ./build/config.yml -assets-dir bin/release/<VERSION> \
  -out bin/release/<VERSION>/update.json -repo kael-odin/cursor-switch -base-name cursor-switch -notes release-notes.md
# 2. 用私钥签名(原地写回 signature 字段) + 自检
go run ./scripts/release sign --manifest bin/release/<VERSION>/update.json
go run ./scripts/release verify --manifest bin/release/<VERSION>/update.json
# 3. 把签名后的 update.json 作为 release 资产上传
gh release upload vX.Y.Z bin/release/<VERSION>/update.json --clobber
```

#### 签名后效果

`update.json` 多一个 `"signature": "..."` 字段。维护者上传签名版本前 release 上没有 `update.json`，客户端更新检查会 404 并静默保持当前版本（不会报错、不会被未签名 manifest 欺骗）。

### 验证签名是否生效

- 在装了"已填公钥"版本的客户端上,自动更新应能正常拉到并校验签名。
- 篡改 `update.json` 任意字节后,客户端日志会出现 `manifest signature rejected`,拒绝更新。

### 兼容期(未填公钥前)

`releasePublicKeyHex` 为空时,更新器接受未签名 manifest(只走 checksum 校验)。**这是为了不破坏存量用户的更新**,一旦你填了公钥,从那一版起就强制校验。所以:**填公钥的那一版必须是你能正常推送的版本**,否则旧客户端拿不到新公钥、新 release 又要求签名,会卡住存量用户更新。建议填公钥的版本是一个正常小版本更新,确保存量用户平滑升级到"带公钥"版本。

---

## 四、旧 CA 私钥历史残留(判断:不重写历史)

### 背景

历史上 `internal/certs/ca.key`(一份共享 RSA 私钥)曾被提交进 git。本轮已:
- 从二进制中移除(`//go:embed` 已删)
- 从工作树删除(`git rm`)
- 改为每机器首次启动独立生成

但 git **历史**里仍能 checkout 出旧 key。

### 为什么不重写历史

1. **旧 key 已退役**:不在二进制里 = 失去为新安装签发证书的能力。历史可提取 ≠ 可利用。
2. **重写代价大**:改写所有 commit 哈希,force-push 破坏已 fork/clone 用户的引用,操作风险高。
3. **真正防护已到位**:每台机器独立 CA,旧 key 泄露不影响新安装。

### 残余风险与缓解

- **残余风险**:旧版用户系统里仍信任旧 CA(`whistle.1770808403268131` 主题,SHA1 `C1:4B:…`)。旧 key 已公开,理论上有人可提取并为此 CA 签发新叶子证书——但前提是受害者系统仍信任旧 CA。
- **缓解**:release-notes 已提示用户升级到每机器 CA 版本。升级后旧 CA 可手动卸载:
  - **Windows**(管理员 PowerShell):`certutil -delstore Root <旧CA的SHA1指纹>`
  - **macOS**:`sudo security delete-certificate -Z <旧CA的SHA1指纹> login.keychain-db`
  - 旧 CA 指纹:`C1:4B:…`(可在 certmgr / 钥匙串访问里搜 `whistle` 确认)

如果你之后判断需要彻底清除历史(例如旧 key 被证实被利用),再做一次性 `git filter-repo` 重写,届时需协调所有 fork 重新 clone。当前判断:**不重写,可接受。**

---

## 五、相关文件索引

- 签名工具:`scripts/release/main.go`(`keypair`、`sign`、`verify` 子命令；`sign` 支持 `--key` 文件 / `CURSOR_SWITCH_SIGNING_KEY` env / 默认文件三种私钥来源)
- CI 自动签名一键配置:`scripts/setup-ci-signing.sh`(`gh secret set` + `gh variable set`)
- 更新校验:`internal/updater/pubkey.go`(`releasePublicKeyHex`、`verifyManifestSignature`、`VerifyManifestSignatureHex` 导出包装)
- 更新主流程:`internal/updater/manager.go`(`fetchUpdateInfo` 调校验)
- 版本比较:`internal/updater/semver.go`
- 发版 CI:`.github/workflows/release.yml`(`Sign or discard update.json` 步按 `CURSOR_SIGNING_ENABLED` 开关走 CI 签名或 artifact 回退)
- CA 生成:`internal/certs/ca.go`(`EnsureMachineCA`)
