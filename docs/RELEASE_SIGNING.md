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

> **当前状态(2026-07-20):已启用。** 公钥已填入 `internal/updater/pubkey.go`(`releasePublicKeyHex`)。私钥在维护者本地 `~/.cursor-byok-release.key`(0600,不入库)。**从此刻起每次发版后必须本地 `sign` 再重传 update.json**(见第 3 步),否则新版客户端会因 `manifest missing required signature` 拒绝更新。

### 启用步骤(只需做一次)

**第 1 步:生成密钥对**

```bash
go run ./scripts/release keypair
```

输出示例:
```
private key written: /home/you/.cursor-byok-release.key (KEEP SECRET — never commit)
public key (hex): b7787d81f16782b485c3c659b2ede84164b514babb57c2c3818013f8e0ba8103
```

- 私钥 `~/.cursor-byok-release.key`(权限 0600),**永远不要提交、不要分享、不要丢**。丢了就得重新生成公钥并发新版强制所有用户更新到带新公钥的版本。
- 命令拒绝覆盖已存在的私钥(防误删旧 key 导致签名断裂)。
- 把公钥 hex 复制下来,进第 2 步。

**第 2 步:填入公钥**

编辑 `internal/updater/pubkey.go`,把 `releasePublicKeyHex` 改成你的公钥:

```go
var releasePublicKeyHex = "b7787d81f16782b485c3c659b2ede84164b514babb57c2c3818013f8e0ba8103"
```

提交这个改动并发布。从这一版起,客户端内置了公钥,可以校验签名。

**第 3 步:给每次发版的 update.json 签名**

CI 生成的 `update.json` 是**未签名**的(CI 不持有私钥)。发版后,在本地补签:

```bash
# 1. 从 GitHub release 下载刚生成的 update.json
gh release download v0.0.41 --pattern update.json --dir /tmp/

# 2. 用私钥签名(原地写回 signature 字段)
go run ./scripts/release sign --manifest /tmp/update.json

# 3. 重新上传签名后的 update.json(覆盖)
gh release upload v0.0.41 /tmp/update.json --clobber
```

签好后 `update.json` 会多一个 `"signature": "..."` 字段。

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

- 签名工具:`scripts/release/main.go`(`keypair`、`sign` 子命令)
- 更新校验:`internal/updater/pubkey.go`(`releasePublicKeyHex`、`verifyManifestSignature`)
- 更新主流程:`internal/updater/manager.go`(`fetchUpdateInfo` 调校验)
- 版本比较:`internal/updater/semver.go`
- 发版 CI:`.github/workflows/release.yml`
- CA 生成:`internal/certs/ca.go`(`EnsureMachineCA`)
