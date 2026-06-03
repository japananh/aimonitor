<div align="center">

# aimonitor

**面向 macOS 与 Linux 的多账户 Claude Code 会话监控与静默账户切换工具。**

[![CI](https://github.com/japananh/aimonitor/actions/workflows/ci.yml/badge.svg)](https://github.com/japananh/aimonitor/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/japananh/aimonitor?sort=semver)](https://github.com/japananh/aimonitor/releases)

</div>

> [English](README.md) | **中文** | [Tiếng Việt](README-vi.md)

## 功能特性

- 🔍 **菜单栏中按账户显示实时的 5 小时 / 7 天用量条** —— 来自服务端的真实数据，通过 Anthropic 的 `/api/oauth/usage` 自省接口轮询获取，不消耗任何 token。
- 🔀 **静默切换账户** —— `aimonitor switch <label>` 通过 Anthropic 的 token 接口刷新 OAuth access token 并写入当前凭据。无需切换终端，也无需 `claude /login`。
- 🤖 **自动切换**：在用量达到 80%（阈值可配置，单一阈值）时触发，选择剩余额度最多的账户。**运行中的 `claude` 会话不会被打断**——它们会自动采用新凭据。
- 🤝 **与其它工具良好共存。** 通过身份（identity）来识别当前账户，因此当 Claude Code 或其它切换器更改了当前登录时，aimonitor 会自动跟随；发生这种情况时它会通知你，或在遇到尚未纳管的账户时提示你导入。
- 🔐 **凭据存储于操作系统钥匙串**（macOS 通过 `/usr/bin/security` 使用 Keychain，Linux 使用 libsecret）。SQLite 仅保存引用，token 永不离开钥匙串。
- ⬆️ **内置自更新** —— 检查 GitHub 上的新版本，确认后通过 Homebrew 更新。绝不在无人值守时自动安装。
- 📡 **本地优先。** 无遥测，不回传。

## 安装

### macOS（Sonoma 14+）

```sh
brew install --cask japananh/tap/aimonitor
```

> **首次启动：** 该 `.app` 尚未公证（公证已列入路线图）。请先清除 Gatekeeper 隔离属性一次：
> ```sh
> xattr -dr com.apple.quarantine /Applications/AIMonitor.app
> ```
> 或者右键 → 打开 → 确认。详见 [`docs/unsigned-app.md`](docs/unsigned-app.md)。

### Linux（Ubuntu 22.04+）

```sh
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh
```

Linux 上仅提供 CLI；GTK 菜单栏小组件属于 v2.0 的交付内容。

### 通过 `go install`（仅 CLI，任意平台）

```sh
go install github.com/japananh/aimonitor/cmd/aimonitor@latest
```

会将 `aimonitor` 安装到 `$GOBIN`。没有 `.app`，也没有开机自启服务——适合只需要在终端里切换账户、或不想添加 Homebrew tap 的场景。

## 快速开始

```sh
# 1. 把当前的 Claude Code 登录注册为 aimonitor 的第一个账户。
#    --adopt-current 会采用钥匙串中已有的凭据，而不是发起一次全新的 OAuth 流程。
aimonitor add --adopt-current --label personal

# 2. 添加第二个账户。aimonitor 会暂存当前凭据、打印操作说明并轮询钥匙串。
#    你在另一个终端里运行 `claude` + `/login` 完成登录。
aimonitor add --label work

# 3. 静默切换——无需终端，无需 /login。
aimonitor switch work

# 4. 查看每个账户的实时 5h / 7d 用量。
aimonitor list

# 5. 健康检查。
aimonitor doctor
```

已经在用其它切换器（例如 claude-bar）？一步导入它的账户，无需手动逐个添加：

```sh
aimonitor import
```

自动切换默认开启，阈值为 5 小时用量的 80%。常见场景下无需任何其它配置。

## 配置

```sh
aimonitor config set auto_swap.enabled true       # 默认 true
aimonitor config set auto_swap.threshold_pct 80   # 默认 80
aimonitor config set autostart true                # 登录时启动守护进程
```

<details>
<summary><b>完整配置项</b></summary>

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `auto_swap.enabled` | `true` | 基于 OAuth 用量的自动切换总开关 |
| `auto_swap.threshold_pct` | `80` | 触发自动切换的 5 小时用量阈值（%） |
| `auto_swap.grace_sec` | `60` | 从“即将自动切换”通知到真正切换之间的秒数，便于你收尾正在进行的 `claude` 会话。设为 `0` 则立即切换。 |
| `auto_update.enabled` | `true` | 启动时检查 GitHub 上的新版本并通知你。未经确认绝不安装更新。 |
| `autostart` | `false` | 登录时启动守护进程 |
| `autoswitch` | `false` | （遗留项）基于 JSONL 的触发式累加器，已被 `auto_swap.*` 取代。设置该项会被拒绝。 |

</details>

## 工作原理

当前账户的 5 小时用量达到配置阈值时，aimonitor 会找到用量次低的账户并静默切换：

```
                      polled every 5 min ± 30 s jitter
                ┌─────────────────────────────────────────┐
                │  GET  api.anthropic.com/api/oauth/usage │
                │       → 5h % + 7d % + reset times       │
                └─────────────────────────────────────────┘
                                   │
              5h utilization ≥ threshold?
                          │
                          ▼  yes — pick lowest-utilization account
   ┌──────────────────┐   POST platform.claude.com/v1/oauth/token
   │ target account   │ ──────────────────────────────────────────▶
   │ refresh_token    │   grant_type=refresh_token
   └──────────────────┘                  │
                                         ▼ fresh access_token
                          ┌───────────────────────────┐
                          │ Claude Code-credentials   │
                          │   (macOS Keychain slot)   │
                          └───────────────────────────┘
                                         │
                                         ▼
                            running and new `claude`
                            sessions use the new account
                            — no /login, no restart
```

守护进程 / 存储 / 小组件的完整拆解见 [`docs/architecture.md`](docs/architecture.md)。

## 隐私与安全

- **无遥测，不回传。** 任何地方都不会。
- OAuth token 仅存于操作系统钥匙串。SQLite 只保存引用，绝不保存机密。
- 即使在 `--debug` 级别也绝不记录 token 字节。日志脱敏会匹配 `sk-ant-(oat|ort)…`。
- **aimonitor 发起的出站流量**仅限于：
  - `GET https://api.anthropic.com/api/oauth/usage` —— 仅自省，每次约 5 KB，不消耗 token。后台轮询间隔：当前账户为 5 分钟 ± 30 秒抖动，出错时指数退避（上限 1 小时）。非活跃账户以缓慢的轮转方式逐个轮询（仅在其 token 仍有效时——后台绝不刷新它们），或通过每账户 /「刷新用量」按钮按需获取。
  - `POST https://platform.claude.com/v1/oauth/token` —— 在切换、手动刷新用量、或自动切换决策前，刷新临近或已过期的 access token。静默进行（不打开浏览器）。
  - `GET https://api.github.com/repos/japananh/aimonitor/releases` —— 检查更新。无需鉴权，不发送任何关于你的数据；在启动时（若 `auto_update.enabled`）以及你点击「检查更新」时运行。安装更新会调用 Homebrew，且仅在你确认后进行。
- 遗留的 `aimonitor probe` CLI 子命令会发起真实的 `/v1/messages` 请求，已废弃。守护进程不再使用它。

完整威胁模型见 [`docs/security.md`](docs/security.md)。

## 路线图

仅为方向性规划，并非承诺。

- **v1.1：** 每日用量图表、按账户的费用估算、已公证的 macOS 应用。
- **v1.2（取决于 v1.1 的公证）：** 提交到 `homebrew/cask`，让 `brew install aimonitor` 无需添加第三方 tap 即可使用。
- **v2.0：** Ubuntu GTK 菜单栏小组件、第二个 `Provider` 实现（Codex 或 Copilot CLI）。

## 卸载

```sh
aimonitor uninstall              # 关闭开机自启；保留你的数据
aimonitor uninstall --purge      # 同时删除 SQLite 数据库、配置以及 aimonitor 的钥匙串条目

# macOS
brew uninstall --cask aimonitor
brew untap japananh/tap          # 可选

# Linux
systemctl --user disable aimonitor.service
sudo rm /usr/local/bin/aimonitor
```

aimonitor 的卸载**绝不会触碰**你原有的 `Claude Code-credentials` 钥匙串条目——现有的 `claude` CLI 登录会继续正常工作。

## 从源码构建

需要 Go 1.25+。所有平台均为纯 Go——`CGO_ENABLED=0` 在 macOS 上同样可用（钥匙串访问通过调用 `/usr/bin/security` 实现，而非经由 cgo 链接 Security 框架）。

```sh
git clone https://github.com/japananh/aimonitor
cd aimonitor
make build              # Go CLI 二进制
make test               # 单元测试
make widget             # 通过 Swift Package Manager 构建 AIMonitor.app（仅 macOS）
make release-snapshot   # 完整的 goreleaser 演练（不发布；需要已安装 goreleaser）
```

在 macOS 上构建菜单栏小组件需要 Swift 工具链（`xcode-select --install`）。无需完整的 Xcode；小组件可通过 Swift Package Manager 无界面构建。

## 文档

| 主题 | 位置 |
|---|---|
| 架构（守护进程、存储、小组件） | [`docs/architecture.md`](docs/architecture.md) |
| 威胁模型与脱敏规则 | [`docs/security.md`](docs/security.md) |
| 为何 macOS `.app` 尚未公证 | [`docs/unsigned-app.md`](docs/unsigned-app.md) |
| v1 已交付的用户故事 | [`USER_STORIES.md`](USER_STORIES.md) |

## 相关项目

- [ncthanhngo/claude-bar](https://github.com/ncthanhngo/claude-bar) —— 同类的 macOS 菜单栏应用，aimonitor 从中借鉴了不少做法（钥匙串调用、OAuth 刷新流程、账户注册表）。

## 许可证

[MIT](LICENSE) © [@japananh](https://github.com/japananh)
