<div align="center">

# aimonitor

**面向 macOS 与 Linux 的多账户 Claude Code 用量监控与静默账户切换工具。**

[![CI](https://github.com/japananh/aimonitor/actions/workflows/ci.yml/badge.svg)](https://github.com/japananh/aimonitor/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/japananh/aimonitor?sort=semver)](https://github.com/japananh/aimonitor/releases)

> [English](README.md) | **简体中文** | [繁體中文](README-zh-TW.md) | [Tiếng Việt](README-vi.md)

<img src="docs/popover.png" alt="菜单栏弹窗：每个账户的 5h/7d 用量条" width="340">

</div>

## 功能特性

- 🔍 **每个账户的 5h + 7d 用量条** —— 取自 Anthropic 的 `/api/oauth/usage`（不消耗 token），并附趋势行（`↗ +21% in 45m`）。
- 🔀 **静默切换** —— `aimonitor switch <label>` 刷新 OAuth token 并替换当前凭据。无需 `claude /login`，无需切到终端。
- 🤖 **自动切换**：当活跃账户达到 5h *或* 7d 阈值（默认 80 %）时触发 —— 选择整体余量最多的账户（兼顾两个窗口），跳过已用尽/被限流的账户；若活跃账户达到 100 % 立即切换。正在运行的 `claude` 会话会自动跟随。
- 🔔 **接近上限时通知**（在自动切换关闭时生效）。
- 💾 **导出 / 导入** 设置，或把账户迁移到另一台机器 —— 凭据可选，并用口令加密（Argon2id + AES-256-GCM）。
- 🔌 **MCP 服务器** —— 通过 stdio 向 Claude Code 提供 30 个 Slack + ClickUp 工具，支持按服务的只读模式。
- 🔐 **存于系统钥匙串**（macOS Keychain、Linux libsecret）。SQLite 仅保存引用；token 不离开钥匙串。无遥测。

## 安装

```sh
# macOS (Sonoma 14+) —— 一条命令：tap、信任、安装、清除 Gatekeeper
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/macos/install.sh | bash

# Linux (Ubuntu 22.04+) —— 仅 CLI
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh

# 任意平台，仅 CLI
go install github.com/japananh/aimonitor/cmd/aimonitor@latest
```

> **想直接用 Homebrew？** `brew trust japananh/tap && brew install --cask japananh/tap/aimonitor`，然后首次启动时清除 Gatekeeper 隔离：`xattr -dr com.apple.quarantine /Applications/AIMonitor.app`（或右键 → 打开）。上面的一行安装脚本已替你完成这两步。见 [`docs/unsigned-app.md`](docs/unsigned-app.md)。

### 升级

```sh
brew upgrade --cask aimonitor   # macOS
aimonitor update check          # CLI：是否有新版本？
aimonitor update install        # CLI：后台升级
```

菜单栏应用也会在启动时检查 GitHub，并在 **Preferences → Check for updates** 提示更新。预发布版本绝不会自动推送 —— `brew upgrade` 始终让你停留在最新的稳定版。

## 快速开始

```sh
aimonitor add --adopt-current --label personal   # 注册当前的 Claude 登录
aimonitor add --label work                        # 添加另一个账户（运行 claude /login，轮询钥匙串）
aimonitor switch work                             # 静默切换
aimonitor list                                    # 查看每个账户的 5h / 7d 用量
aimonitor doctor                                  # 健康检查
```

已在用别的切换器？`aimonitor import` 一步导入其账户。自动切换默认在 80 % 开启 —— 常见场景无需额外配置。

## 配置

```sh
aimonitor config set auto_swap.enabled true        # 默认 true
aimonitor config set auto_swap.threshold_pct 80    # 5h 阈值
aimonitor config set auto_swap.threshold_7d_pct 80 # 7d 阈值
aimonitor config set autostart true                # 登录时启动 daemon
```

备份或迁移到另一台机器：

```sh
aimonitor config export --out backup.json                                          # 仅设置（无敏感数据）
AIMONITOR_PASSPHRASE=… aimonitor config export --include-tokens --out full.json     # + 加密凭据
AIMONITOR_PASSPHRASE=… aimonitor config import full.json                            # 在另一台机器恢复
```

`--include-tokens` 会把登录凭据用口令加密打包 —— 恢复后无需重新登录即可在另一台机器运行 `claude`，因此请把该文件当作密码保管。相同操作也在 Preferences → Backup 中。

<details>
<summary><b>全部配置项</b></summary>

| 键 | 默认 | 说明 |
|---|---|---|
| `auto_swap.enabled` | `true` | 自动切换总开关 |
| `auto_swap.threshold_pct` | `80` | 触发自动切换的 5h 用量（%） |
| `auto_swap.threshold_7d_pct` | `80` | 触发自动切换的 7d 用量（%） |
| `auto_swap.grace_sec` | `60` | “即将切换”通知与实际切换之间的延迟（`0` = 立即） |
| `notify.enabled` | `true` | 活跃账户接近上限时通知（仅在自动切换关闭时） |
| `notify.warn_pct` / `notify.crit_pct` | `80` / `95` | 警告 / 严重 通知级别 |
| `auto_update.enabled` | `true` | 启动时检查 GitHub 新版本（绝不自动安装） |
| `autostart` | `false` | 登录时启动 daemon |
| `mcp.slack.enabled` / `mcp.clickup.enabled` | `true` | 暴露该服务的 MCP 工具 |
| `mcp.slack.read_only` / `mcp.clickup.read_only` | `false` | 隐藏该服务的写入类工具 |
| `mcp.disabled_tools` | （空） | 需隐藏的工具名，逗号分隔 |

</details>

## 工作原理

daemon 轮询 `/api/oauth/usage`（约 5 分钟 ± 抖动，不消耗 token）。当活跃账户越过 5h **或** 7d 阈值时，选出整体余量最多的账户，刷新该账户的 OAuth token（`POST .../v1/oauth/token`），写入当前 Keychain 槽。正在运行和新开的 `claude` 会话都会使用新账户 —— 无需 `/login`，无需重启。

详见 [`docs/architecture.md`](docs/architecture.md) 与 [`docs/thresholds.md`](docs/thresholds.md)。

## MCP 服务器（为 Claude Code 提供 Slack + ClickUp）

单个 stdio 进程提供 30 个工具 —— 无需额外运行时。

```sh
aimonitor mcp connect slack     # 保存 Slack 用户 token（xoxp-…）
aimonitor mcp connect clickup   # 保存 ClickUp token（pk_…）
aimonitor mcp register          # 把服务器加入 Claude Code
```

- **Slack：** 发到频道/线程（mrkdwn、代码块）、上传、搜索、历史、permalink。
- **ClickUp：** 工作区层级、任务、评论、Docs（读写）。
- **安全：** Claude Code 自身的逐工具授权提示是审批层；再加上按服务的 Enabled / Read-only 开关和逐工具禁用列表。token 先实时校验，再存入系统钥匙串 —— 不进 SQLite 或日志。

## 隐私与安全

- 无遥测、无回传。OAuth token 仅存于系统钥匙串；SQLite 只保存引用。绝不记录 token。
- 对外流量仅限：`GET /api/oauth/usage`（自省，不消耗 token）、`POST /v1/oauth/token`（静默刷新 token）、以及 GitHub 版本检查。不发送任何关于你的信息。

威胁模型见 [`docs/security.md`](docs/security.md)。

## 故障排查

```sh
aimonitor doctor   # 健康检查：配置、SQLite、钥匙串、账户
```

- **"Daemon not running" / 用量看起来是旧的。** 用 `aimonitor config set autostart true` 启动（或重启）后台 daemon，或在弹窗里点 **Start daemon** —— 它会注册一个登录时自动重启的 LaunchAgent。
- **首次启动打不开**（未签名）。清除一次 Gatekeeper 隔离：`xattr -dr com.apple.quarantine /Applications/AIMonitor.app`。
- **日志。** daemon 写入 `~/Library/Logs/aimonitor/aimonitor.daemon.log`（INFO/WARN/ERROR —— 绝不记录 token）；后台升级写入旁边的 `update.log`。
- **最近的切换。** `aimonitor log` 打印切换审计记录。

## 卸载

```sh
# 删除 app + daemon，保留账户
brew uninstall --cask aimonitor

# 彻底清除，包括保存在钥匙串里的登录。purge 必须先跑：Homebrew 够不到钥匙串里
# 的 stash，而且 purge 需要 binary 还在才能清除它们。
aimonitor uninstall --purge && brew uninstall --cask aimonitor
```

`--purge` 只删除 **aimonitor 自己的**钥匙串条目（`aimonitor-<uuid>` stash），**绝不**碰 Claude 的 `Claude Code-credentials` 槽。所以你当前登录的账户在 `claude` 里照常可用，**无需重新登录** —— 只是 aimonitor 为*其他*账户保存的副本被清掉了。

## 从源码构建

需要 Go 1.25+。纯 Go（`CGO_ENABLED=0` 在 macOS 也可用；钥匙串访问通过 `/usr/bin/security`）。

```sh
make build              # CLI 二进制
make test               # 单元测试
make widget             # AIMonitor.app（macOS；需 Swift 工具链）
make release-snapshot   # goreleaser 试运行
```

## 许可证

[MIT](LICENSE) © [@japananh](https://github.com/japananh)
