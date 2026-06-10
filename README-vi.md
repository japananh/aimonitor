<div align="center">

# aimonitor

**Giám sát usage & tự động đổi tài khoản Claude Code (nhiều account) cho macOS và Linux.**

[![CI](https://github.com/japananh/aimonitor/actions/workflows/ci.yml/badge.svg)](https://github.com/japananh/aimonitor/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/japananh/aimonitor?sort=semver)](https://github.com/japananh/aimonitor/releases)

> [English](README.md) | [简体中文](README-zh.md) | [繁體中文](README-zh-TW.md) | **Tiếng Việt**

<img src="docs/popover.png" alt="Popover trên menu bar: thanh usage 5h/7d theo từng account" width="340">

</div>

## Tính năng

- 🔍 **Thanh usage 5h + 7d theo từng account** — đọc từ `/api/oauth/usage` của Anthropic (không tốn token), kèm đường xu hướng (`↗ +21% in 45m`).
- 🔀 **Đổi account âm thầm** — `aimonitor switch <label>` refresh token OAuth rồi thay credential live. Không cần `claude /login`, không phải mở terminal.
- 🤖 **Auto-swap** khi chạm ngưỡng 5h *hoặc* 7d (mặc định 80 %) — chọn account còn nhiều headroom nhất (cân cả 2 cửa sổ), bỏ qua account đã cạn/đang bị rate-limit, và đổi ngay nếu account active chạm 100 %. Session `claude` đang chạy tự theo account mới.
- 🔔 **Thông báo khi gần ngưỡng** (khi auto-swap tắt).
- 💾 **Export / import** settings, hoặc chuyển account sang máy khác — credential là tùy chọn, mã hóa bằng passphrase (Argon2id + AES-256-GCM).
- 🔌 **MCP server** — 28 tool Slack + ClickUp cho Claude Code qua stdio, có chế độ read-only theo từng dịch vụ.
- 🔐 **Lưu trong OS keyring** (macOS Keychain, Linux libsecret). SQLite chỉ giữ tham chiếu; token không rời keyring. Không telemetry.

## Cài đặt

```sh
# macOS (Sonoma 14+) — một lệnh: tap, trust, cài, gỡ Gatekeeper
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/macos/install.sh | bash

# Linux (Ubuntu 22.04+) — chỉ CLI
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh

# Mọi nền tảng, chỉ CLI
go install github.com/japananh/aimonitor/cmd/aimonitor@latest
```

> **Thích dùng Homebrew trực tiếp?** `brew trust japananh/tap && brew install --cask japananh/tap/aimonitor`, rồi gỡ Gatekeeper lần đầu: `xattr -dr com.apple.quarantine /Applications/AIMonitor.app` (hoặc chuột phải → Open). Script một dòng ở trên làm sẵn cả hai. Xem [`docs/unsigned-app.md`](docs/unsigned-app.md).

### Nâng cấp

```sh
brew upgrade --cask aimonitor   # macOS
aimonitor update check          # CLI: có bản mới chưa?
aimonitor update install        # CLI: nâng cấp chạy nền
```

App trên menu bar cũng tự kiểm tra GitHub khi mở và mời cập nhật ở **Preferences → Check for updates**. Bản pre-release không bao giờ được phục vụ tự động — `brew upgrade` luôn giữ bạn ở bản stable mới nhất.

## Bắt đầu nhanh

```sh
aimonitor add --adopt-current --label personal   # đăng ký login Claude hiện tại
aimonitor add --label work                        # thêm account khác (chạy claude /login, poll keychain)
aimonitor switch work                             # đổi âm thầm
aimonitor list                                    # xem usage 5h / 7d từng account
aimonitor doctor                                  # health check
```

Đang dùng switcher khác? `aimonitor import` kéo account của nó về một lần. Auto-swap bật sẵn ở 80 % — trường hợp thường không cần chỉnh gì thêm.

## Cấu hình

```sh
aimonitor config set auto_swap.enabled true        # mặc định true
aimonitor config set auto_swap.threshold_pct 80    # ngưỡng 5h
aimonitor config set auto_swap.threshold_7d_pct 80 # ngưỡng 7d
aimonitor config set autostart true                # daemon chạy khi đăng nhập
```

Sao lưu hoặc chuyển sang máy khác:

```sh
aimonitor config export --out backup.json                                          # chỉ settings (không secret)
AIMONITOR_PASSPHRASE=… aimonitor config export --include-tokens --out full.json     # + credential mã hóa
AIMONITOR_PASSPHRASE=… aimonitor config import full.json                            # khôi phục ở máy khác
```

`--include-tokens` đóng gói login đã mã hóa bằng passphrase — khôi phục xong là `claude` chạy được ở máy kia mà không cần login lại, nên hãy giữ file đó như mật khẩu. Cùng thao tác có trong Preferences → Backup.

<details>
<summary><b>Tất cả config key</b></summary>

| Key | Mặc định | Mô tả |
|---|---|---|
| `auto_swap.enabled` | `true` | Bật/tắt auto-swap |
| `auto_swap.threshold_pct` | `80` | Ngưỡng usage 5h (%) để auto-swap |
| `auto_swap.threshold_7d_pct` | `80` | Ngưỡng usage 7d (%) để auto-swap |
| `auto_swap.grace_sec` | `60` | Trễ giữa thông báo "sắp đổi" và lúc đổi thật (`0` = đổi ngay) |
| `notify.enabled` | `true` | Cảnh báo khi account active gần ngưỡng (chỉ khi auto-swap tắt) |
| `notify.warn_pct` / `notify.crit_pct` | `80` / `95` | Mức cảnh báo / nghiêm trọng |
| `auto_update.enabled` | `true` | Kiểm tra release mới trên GitHub khi mở (không tự cài) |
| `autostart` | `false` | Chạy daemon khi đăng nhập |
| `mcp.slack.enabled` / `mcp.clickup.enabled` | `true` | Bật tool MCP của dịch vụ đó |
| `mcp.slack.read_only` / `mcp.clickup.read_only` | `false` | Ẩn các tool ghi của dịch vụ |
| `mcp.disabled_tools` | (rỗng) | Tên tool cần ẩn, ngăn cách bằng dấu phẩy |

</details>

## Cơ chế

Daemon poll `/api/oauth/usage` (~5 phút ± jitter, không tốn token). Khi account active vượt ngưỡng 5h **hoặc** 7d, nó chọn account còn nhiều headroom nhất, refresh token OAuth của account đó (`POST .../v1/oauth/token`) rồi ghi vào slot Keychain live. Session `claude` đang chạy và mở mới đều dùng account mới — không cần `/login`, không restart.

Chi tiết: [`docs/architecture.md`](docs/architecture.md) và [`docs/thresholds.md`](docs/thresholds.md).

## MCP server (Slack + ClickUp cho Claude Code)

Một tiến trình stdio phục vụ 28 tool — không cần runtime phụ.

```sh
aimonitor mcp connect slack     # lưu Slack user token (xoxp-…)
aimonitor mcp connect clickup   # lưu ClickUp token (pk_…)
aimonitor mcp register          # thêm server vào Claude Code
```

- **Slack:** post vào channel/thread (mrkdwn, code block), upload, search, history, permalink.
- **ClickUp:** cây workspace, task, comment, Docs (đọc & ghi).
- **An toàn:** prompt xin-quyền theo từng tool của Claude Code là lớp duyệt; thêm công tắc Enabled / Read-only theo dịch vụ và danh sách ẩn từng tool. Token được verify trực tiếp rồi lưu OS keyring — không vào SQLite hay log.

## Quyền riêng tư & bảo mật

- Không telemetry, không phone-home. Token OAuth chỉ nằm trong OS keyring; SQLite giữ tham chiếu. Không bao giờ log token.
- Lưu lượng ra ngoài chỉ gồm: `GET /api/oauth/usage` (introspection, không tốn token), `POST /v1/oauth/token` (refresh token âm thầm), và kiểm tra release GitHub. Không gửi thông tin gì về bạn.

Xem [`docs/security.md`](docs/security.md) cho mô hình mối đe dọa.

## Xử lý sự cố

```sh
aimonitor doctor   # kiểm tra sức khỏe: config, SQLite, keyring, account
```

- **"Daemon not running" / usage có vẻ cũ.** Khởi động (hoặc khởi động lại) daemon nền bằng `aimonitor config set autostart true`, hoặc bấm **Start daemon** trong popover — nó đăng ký một LaunchAgent tự chạy lại khi đăng nhập.
- **App không mở được lần đầu** (chưa ký). Gỡ Gatekeeper một lần: `xattr -dr com.apple.quarantine /Applications/AIMonitor.app`.
- **Log.** Daemon ghi vào `~/Library/Logs/aimonitor/aimonitor.daemon.log` (INFO/WARN/ERROR — không bao giờ ghi token); nâng cấp chạy nền ghi vào `update.log` bên cạnh.
- **Lịch sử đổi account.** `aimonitor log` in nhật ký audit các lần switch.

## Gỡ cài đặt

```sh
# Gỡ app + daemon, giữ nguyên account
brew uninstall --cask aimonitor

# Xoá sạch, gồm cả login đã lưu trong Keychain. Phải chạy purge trước vì
# Homebrew không với tới các keychain stash — và purge cần binary còn cài
# thì mới xoá được chúng.
aimonitor uninstall --purge && brew uninstall --cask aimonitor
```

`--purge` chỉ xoá Keychain **của riêng aimonitor** (các stash `aimonitor-<uuid>`), **không** đụng slot `Claude Code-credentials` của Claude. Nên tài khoản ấy đang đăng nhập vẫn chạy bình thường trong `claude`, **không phải login lại** — chỉ mất các bản sao mà aimonitor lưu cho những account *khác*.

## Build từ source

Cần Go 1.25+. Thuần Go (`CGO_ENABLED=0` chạy được cả trên macOS; truy cập keychain qua `/usr/bin/security`).

```sh
make build              # binary CLI
make test               # unit test
make widget             # AIMonitor.app (macOS; cần Swift toolchain)
make release-snapshot   # chạy thử goreleaser
```

## Giấy phép

[MIT](LICENSE) © [@japananh](https://github.com/japananh)
