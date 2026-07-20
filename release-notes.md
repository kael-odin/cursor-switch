- 支持 GPT 5.6
- 移除应用内广告 SDK（首页广告位、广告弹层、远程广告拉取与轮询、设备指纹上报）
- 移除作者个人露出（作者按钮、bilibili 引流、作者寄语弹窗、docs.leokun.cn 文档站外链）
- 发布目标迁移至 kael-odin/cursor-byok（自动更新、release 资产、README 同步全部指向本 fork）
- 修复 OpenAI 适配器 thinking disable 不识别小米 MiMo（reasoning=disabled 时 MiMo 仍开思考）
- 修复 Anthropic 适配器 thinking 配置在 override 路径丢失，与 openai 行为对齐
- 标题与品牌去除"永久免费"立场表述
- 更新支持最新版 Cursor 3.9.16
- 支持多工作区模式

## 安全更新（P0）

- **每机器独立 CA**：不再随二进制分发共享 CA 私钥，首次启动为每台机器生成独立 CA（存于用户数据目录）
- **写路径工作区围栏**：LLM 写文件仅限工作区与终端目录，拒绝写入 `~/.ssh`、系统目录等敏感路径
- **更新签名校验**：update.json 支持 ed25519 签名校验；维护者填入公钥后强制校验，篡改的 manifest 一律拒绝
- **CA 可卸载**：停止服务时从系统信任存储移除本机 CA（此前卸载会残留信任锚）
- **loopback 鉴权**：本地 backend 仅接受本进程 mitm 转发的请求，拒绝本机其它进程裸调
- **prompt 注入面闭合**：文件正文 / tool_result / 上下文嵌入串转义 XML 特殊字符，防止标签逃逸
- **自定义请求头黑名单**：禁止自定义头覆盖 `Authorization` / `x-api-key` / `Host` / `Cookie`
- **正则 ReDoS 防护**：AwaitShell 模式限长 + 匹配超时
- **Cursor 配置解析失败改备份**：损坏的 settings.json 改为备份而非删除
- **cursor-tab-server 配置隔离**：默认配置改名为 config.example.yaml，真实 token 不再进 git

