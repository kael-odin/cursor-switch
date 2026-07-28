package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cursor/internal/updater"

	"gopkg.in/yaml.v3"
)

type buildConfig struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
}

type updateManifest struct {
	Version      string                         `json:"version"`
	ReleaseDate  string                         `json:"release_date"`
	ReleaseNotes string                         `json:"release_notes"`
	Platforms    map[string]updateManifestAsset `json:"platforms"`
	Mandatory    bool                           `json:"mandatory"`
	Signature    string                         `json:"signature,omitempty"`
}

type updateManifestAsset struct {
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
}

type assetSpec struct {
	platform string
	suffix   string
}

var releaseAssets = []assetSpec{
	{platform: "macos-arm64", suffix: ".tar.gz"},
	{platform: "macos-amd64", suffix: ".tar.gz"},
	{platform: "windows-amd64", suffix: ".zip"},
	{platform: "linux-amd64", suffix: ".tar.gz"},
}

func main() {
	if len(os.Args) < 2 {
		exitf("usage: go run ./scripts/release <version|notes|manifest|keypair|sign|verify|verify-versions|sync-versions> [flags]")
	}

	switch os.Args[1] {
	case "version":
		runVersion(os.Args[2:])
	case "notes":
		runNotes(os.Args[2:])
	case "manifest":
		runManifest(os.Args[2:])
	case "keypair":
		runKeypair(os.Args[2:])
	case "sign":
		runSign(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "verify-versions":
		runVerifyVersions(os.Args[2:])
	case "sync-versions":
		runSyncVersions(os.Args[2:])
	default:
		exitf("unknown subcommand: %s", os.Args[1])
	}
}

func runVersion(args []string) {
	flags := flag.NewFlagSet("version", flag.ExitOnError)
	configPath := flags.String("config", "build/config.yml", "path to build config")
	_ = flags.Parse(args)

	version, err := readVersion(*configPath)
	if err != nil {
		exitErr(err)
	}

	fmt.Print(version)
}

func runNotes(args []string) {
	flags := flag.NewFlagSet("notes", flag.ExitOnError)
	_ = flags.String("config", "build/config.yml", "path to build config")
	outputPath := flags.String("out", "", "output file path")
	sourcePath := flags.String("source", "", "source markdown file")
	_ = flags.Parse(args)

	if strings.TrimSpace(*outputPath) == "" {
		exitf("notes output path is required")
	}
	if strings.TrimSpace(*sourcePath) == "" {
		exitf("notes source path is required")
	}

	notes, err := resolveReleaseNotes(*sourcePath)
	if err != nil {
		exitErr(err)
	}

	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		exitErr(err)
	}
	if err := os.WriteFile(*outputPath, []byte(notes), 0o644); err != nil {
		exitErr(err)
	}
}

func runManifest(args []string) {
	flags := flag.NewFlagSet("manifest", flag.ExitOnError)
	configPath := flags.String("config", "build/config.yml", "path to build config")
	assetsDir := flags.String("assets-dir", "", "directory containing release assets")
	outputPath := flags.String("out", "", "manifest output file")
	repo := flags.String("repo", "", "GitHub repo in owner/repo form")
	baseName := flags.String("base-name", "cursor-switch", "release asset basename")
	notesPath := flags.String("notes", "", "release notes file")
	_ = flags.Parse(args)

	if strings.TrimSpace(*assetsDir) == "" {
		exitf("assets-dir is required")
	}
	if strings.TrimSpace(*outputPath) == "" {
		exitf("manifest output path is required")
	}
	if strings.TrimSpace(*repo) == "" {
		exitf("repo is required")
	}
	if strings.TrimSpace(*notesPath) == "" {
		exitf("notes is required")
	}

	version, err := readVersion(*configPath)
	if err != nil {
		exitErr(err)
	}

	notes, err := resolveReleaseNotes(*notesPath)
	if err != nil {
		exitErr(err)
	}

	manifest := updateManifest{
		Version:      version,
		ReleaseDate:  time.Now().UTC().Format(time.RFC3339),
		ReleaseNotes: notes,
		Platforms:    map[string]updateManifestAsset{},
		Mandatory:    false,
	}

	for _, spec := range releaseAssets {
		filename := fmt.Sprintf("%s-%s-%s%s", *baseName, version, spec.platform, spec.suffix)
		fullpath := filepath.Join(*assetsDir, filename)
		asset, err := buildManifestAsset(fullpath, *repo, version, filename)
		if err != nil {
			exitErr(err)
		}
		manifest.Platforms[spec.platform] = asset
	}

	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		exitErr(err)
	}

	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		exitErr(err)
	}
	content = append(content, '\n')

	if err := os.WriteFile(*outputPath, content, 0o644); err != nil {
		exitErr(err)
	}
}

func readVersion(configPath string) (string, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}

	var cfg buildConfig
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return "", err
	}

	version := strings.TrimSpace(strings.TrimPrefix(cfg.Info.Version, "v"))
	if version == "" {
		return "", errors.New("build/config.yml info.version is empty")
	}
	return version, nil
}

func resolveReleaseNotes(sourcePath string) (string, error) {
	candidate := strings.TrimSpace(sourcePath)
	if candidate == "" {
		return "", errors.New("release notes source path is required")
	}

	content, err := os.ReadFile(candidate)
	if err != nil {
		return "", err
	}

	notes := strings.TrimSpace(string(content))
	if notes == "" {
		return "", fmt.Errorf("release notes file %s is empty", candidate)
	}
	return notes, nil
}

func buildManifestAsset(path, repo, version, filename string) (updateManifestAsset, error) {
	info, err := os.Stat(path)
	if err != nil {
		return updateManifestAsset{}, err
	}

	checksum, err := sha256File(path)
	if err != nil {
		return updateManifestAsset{}, err
	}

	return updateManifestAsset{
		URL:      fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", repo, version, filename),
		Size:     info.Size(),
		Checksum: "sha256:" + checksum,
	}, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func exitErr(err error) {
	exitf("%v", err)
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// runKeypair 生成一对 ed25519 密钥：私钥写入 --key（默认 ~/.cursor-switch-release.key，0600），
// 公钥以 hex 打印到 stdout（供填入 internal/updater/pubkey.go 的 releasePublicKeyHex）。
func runKeypair(args []string) {
	flags := flag.NewFlagSet("keypair", flag.ExitOnError)
	keyPath := flags.String("key", "", "private key output path (default $HOME/.cursor-switch-release.key)")
	_ = flags.Parse(args)

	path := strings.TrimSpace(*keyPath)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			exitErr(err)
		}
		path = filepath.Join(home, ".cursor-switch-release.key")
	}

	if _, err := os.Stat(path); err == nil {
		exitf("private key already exists at %s — refusing to overwrite (delete it first to regenerate)", path)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		exitErr(err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		exitErr(err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0o600); err != nil {
		exitErr(err)
	}

	fmt.Printf("private key written: %s (KEEP SECRET — never commit)\n", path)
	fmt.Printf("public key (hex): %s\n", hex.EncodeToString(pub))
	fmt.Println("Paste the public key into internal/updater/pubkey.go: releasePublicKeyHex")
}

// runSign 读取 --manifest 的 update.json，用 --key 的私钥对 canonical manifest（除 signature 外）签名，
// 把 signature 写回 manifest 并落盘。
//
// 私钥来源优先级：--key flag 显式指定 > 环境变量 CURSOR_SWITCH_SIGNING_KEY（hex）> 默认文件 ~/.cursor-switch-release.key。
// env 分支供 CI 注入（GitHub Actions secret），文件分支供维护者本地使用——同一子命令两用。
func runSign(args []string) {
	flags := flag.NewFlagSet("sign", flag.ExitOnError)
	manifestPath := flags.String("manifest", "", "update.json path to sign in-place")
	keyPath := flags.String("key", "", "private key path (default $HOME/.cursor-switch-release.key, or CURSOR_SWITCH_SIGNING_KEY env)")
	_ = flags.Parse(args)

	if strings.TrimSpace(*manifestPath) == "" {
		exitf("manifest path is required")
	}

	priv, err := resolveSigningKey(*keyPath)
	if err != nil {
		exitErr(err)
	}

	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		exitErr(err)
	}

	var m updateManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		exitf("parse manifest: %v", err)
	}

	// canonical bytes = manifest with signature cleared, compact JSON (matches updater.canonicalManifestBytes).
	m.Signature = ""
	canonical, err := json.Marshal(m)
	if err != nil {
		exitErr(err)
	}

	sig := ed25519.Sign(priv, canonical)
	m.Signature = hex.EncodeToString(sig)

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		exitErr(err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(*manifestPath, out, 0o644); err != nil {
		exitErr(err)
	}
	fmt.Printf("signed manifest: %s (signature=%s)\n", *manifestPath, m.Signature)
}

// resolveSigningKey 按优先级解析私钥：--key flag > CURSOR_SWITCH_SIGNING_KEY env > 默认文件路径。
func resolveSigningKey(keyPathFlag string) (ed25519.PrivateKey, error) {
	// 1. --key flag 显式指定 → 文件
	if strings.TrimSpace(keyPathFlag) != "" {
		return loadSigningKey(keyPathFlag)
	}
	// 2. env CURSOR_SWITCH_SIGNING_KEY（CI secret 注入）→ 直接 hex
	if envHex := strings.TrimSpace(os.Getenv("CURSOR_SWITCH_SIGNING_KEY")); envHex != "" {
		return parseSigningKeyHex(envHex)
	}
	// 3. 默认文件 ~/.cursor-switch-release.key
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return loadSigningKey(filepath.Join(home, ".cursor-switch-release.key"))
}

// runVerify 校验 --manifest 的 update.json 签名是否有效。
// 复用 internal/updater.VerifyManifestSignatureHex（与客户端 verifyManifestSignature 同一逻辑），
// 保证 CI 签的 manifest 客户端能验过。供 CI 签名后自检，防私钥配错却静默发未签名 manifest。
func runVerify(args []string) {
	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	manifestPath := flags.String("manifest", "", "update.json path to verify")
	_ = flags.Parse(args)

	if strings.TrimSpace(*manifestPath) == "" {
		exitf("manifest path is required")
	}

	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		exitErr(err)
	}

	if err := updater.VerifyManifestSignatureHex(raw); err != nil {
		exitf("verify manifest %s: %v", *manifestPath, err)
	}
	fmt.Printf("verified manifest signature OK: %s\n", *manifestPath)
}

func loadSigningKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read signing key %s: %w", path, err)
	}
	priv, err := parseSigningKeyHex(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("signing key %s: %w", path, err)
	}
	return priv, nil
}

// parseSigningKeyHex 解析私钥 hex 字符串，接受 32 字节 seed 或 64 字节完整私钥。
// 文件模式（loadSigningKey 读盘后调它）与 env 模式（CI secret 注入）共用。
func parseSigningKeyHex(hexStr string) (ed25519.PrivateKey, error) {
	seed, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("decode signing key hex: %w", err)
	}
	// Accept either the 64-byte private key or the 32-byte seed.
	if len(seed) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if len(seed) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(seed), nil
	}
	return nil, fmt.Errorf("invalid signing key length %d (want %d seed or %d private key)", len(seed), ed25519.SeedSize, ed25519.PrivateKeySize)
}

// runVerifyVersions 校验 build/config.yml 的版本号与 windows/info.json、wails.exe.manifest 三处一致。
// 不一致时非零退出，供 CI 在发版前卡住版本漂移。config.yml 为单一事实源。
func runVerifyVersions(args []string) {
	flags := flag.NewFlagSet("verify-versions", flag.ExitOnError)
	configPath := flags.String("config", "build/config.yml", "path to build config")
	_ = flags.Parse(args)

	version, err := readVersion(*configPath)
	if err != nil {
		exitErr(err)
	}
	configDir := filepath.Dir(*configPath)

	// info.json: fixed.file_version + info.*.ProductVersion
	infoPath := filepath.Join(configDir, "windows", "info.json")
	raw, err := os.ReadFile(infoPath)
	if err != nil {
		exitErr(fmt.Errorf("read info.json: %w", err))
	}
	var info struct {
		Fixed struct {
			FileVersion string `json:"file_version"`
		} `json:"fixed"`
		Infos map[string]struct {
			ProductVersion string `json:"ProductVersion"`
		} `json:"info"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		exitErr(fmt.Errorf("parse info.json: %w", err))
	}
	if strings.TrimSpace(info.Fixed.FileVersion) != version {
		exitf("info.json file_version=%q != config.yml version=%q", info.Fixed.FileVersion, version)
	}
	for key, entry := range info.Infos {
		if strings.TrimSpace(entry.ProductVersion) != version {
			exitf("info.json info.%s.ProductVersion=%q != config.yml version=%q", key, entry.ProductVersion, version)
		}
	}

	// wails.exe.manifest: assemblyIdentity version="..."
	manifestPath := filepath.Join(configDir, "windows", "wails.exe.manifest")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		exitErr(fmt.Errorf("read wails.exe.manifest: %w", err))
	}
	re := regexp.MustCompile(`<assemblyIdentity[^>]*\bversion="([^"]+)"`)
	matches := re.FindSubmatch(manifest)
	if len(matches) < 2 {
		exitf("wails.exe.manifest: no assemblyIdentity version= attribute found")
	}
	manifestVersion := string(matches[1])
	if manifestVersion != version {
		exitf("wails.exe.manifest version=%q != config.yml version=%q", manifestVersion, version)
	}

	fmt.Printf("versions OK: %s\n", version)
}

// runSyncVersions 以 build/config.yml 的 info.version 为单一事实源，
// 把 windows/info.json（fixed.file_version + info.*.ProductVersion）与
// windows/wails.exe.manifest（assemblyIdentity version）同步到同一版本号。
// 这是 verify-versions 的写入对应：改 config.yml 后跑一次 sync-versions 即三处合一。
func runSyncVersions(args []string) {
	flags := flag.NewFlagSet("sync-versions", flag.ExitOnError)
	configPath := flags.String("config", "build/config.yml", "path to build config")
	_ = flags.Parse(args)

	version, err := readVersion(*configPath)
	if err != nil {
		exitErr(err)
	}
	configDir := filepath.Dir(*configPath)

	// info.json：解析后改字段，写回时用与源文件相同的缩进。
	infoPath := filepath.Join(configDir, "windows", "info.json")
	raw, err := os.ReadFile(infoPath)
	if err != nil {
		exitErr(fmt.Errorf("read info.json: %w", err))
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		exitErr(fmt.Errorf("parse info.json: %w", err))
	}
	if fixed, ok := info["fixed"].(map[string]any); ok {
		fixed["file_version"] = version
	}
	if infos, ok := info["info"].(map[string]any); ok {
		for _, entry := range infos {
			if m, ok := entry.(map[string]any); ok {
				m["ProductVersion"] = version
			}
		}
	}
	out, err := json.MarshalIndent(info, "", "\t")
	if err != nil {
		exitErr(err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(infoPath, out, 0o644); err != nil {
		exitErr(fmt.Errorf("write info.json: %w", err))
	}

	// manifest：替换主 assemblyIdentity 的 version。
	// 只替换不含 publicKeyToken 的 assemblyIdentity 行（name="com.cursor.wuxianxubei"）；
	// <dependency> 里的 Microsoft.Windows.Common-Controls 带 publicKeyToken，其 version
	// 恒为 6.0.0.0（Windows 通用控件 6.0 标准版本号，与项目版本无关），保留不动。
	manifestPath := filepath.Join(configDir, "windows", "wails.exe.manifest")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		exitErr(fmt.Errorf("read wails.exe.manifest: %w", err))
	}
	updated, ok := rewriteManifestVersions(manifest, version)
	if !ok {
		exitf("wails.exe.manifest: no main assemblyIdentity (without publicKeyToken) version= found to update")
	}
	if err := os.WriteFile(manifestPath, updated, 0o644); err != nil {
		exitErr(fmt.Errorf("write wails.exe.manifest: %w", err))
	}

	fmt.Printf("synced versions to %s (info.json + wails.exe.manifest)\n", version)
}

// rewriteManifestVersions 把 manifest 主 assemblyIdentity 的 version 改为指定值。
// 只改不带 publicKeyToken 的主 assemblyIdentity（com.cursor.wuxianxubei）；
// Common-Controls 依赖项（带 publicKeyToken，version 恒 6.0.0.0）原样保留。
// 返回 (newManifest, ok)，ok=false 表示没找到主 assemblyIdentity 可改（manifest 结构异常）。
// 幂等：传入已是目标 version 的 manifest 不会报错，原样返回 true。
func rewriteManifestVersions(manifest []byte, version string) ([]byte, bool) {
	assemblyRe := regexp.MustCompile(`<assemblyIdentity[^>]*\bversion="[^"]+"[^>]*>`)
	versionRe := regexp.MustCompile(`(version=")[^"]+(")`)
	mainSeen := false
	updated := assemblyRe.ReplaceAllStringFunc(string(manifest), func(line string) string {
		if strings.Contains(line, "publicKeyToken") {
			// Common-Controls 依赖项：保留 6.0.0.0，不随项目版本变。
			return line
		}
		mainSeen = true
		return versionRe.ReplaceAllString(line, "${1}"+version+"${2}")
	})
	return []byte(updated), mainSeen
}
