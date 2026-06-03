<div align="center">

# aimonitor

**Giám sát phiên Claude Code nhiều tài khoản và đổi tài khoản ngầm, cho macOS & Linux.**

[![CI](https://github.com/japananh/aimonitor/actions/workflows/ci.yml/badge.svg)](https://github.com/japananh/aimonitor/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/japananh/aimonitor?sort=semver)](https://github.com/japananh/aimonitor/releases)

</div>

> [English](README.md) | [中文](README-zh.md) | **Tiếng Việt**

## Tính năng

- 🔍 **Xem mức dùng 5 giờ và 7 ngày của từng tài khoản** ngay trên menu bar — số liệu thật lấy thẳng từ endpoint `/api/oauth/usage` của Anthropic, không tốn token nào.
- 🔀 **Đổi tài khoản ngầm** — `aimonitor switch <label>` tự refresh OAuth access token rồi ghi credential mới vào, không phải mở terminal khác, cũng không cần `claude /login`.
- 🤖 **Tự đổi tài khoản** khi dùng tới 80% (ngưỡng tùy chỉnh, chỉ một mức). Nó chọn tài khoản còn dư nhiều nhất. Phiên `claude` đang chạy **không bị ngắt** — tự nhận credential mới luôn.
- 🤝 **Chạy chung êm với tool khác.** aimonitor nhận diện tài khoản đang dùng theo danh tính (email + tổ chức), nên khi Claude Code hay một tool đổi-tài-khoản khác thay credential, nó tự bám theo — đồng thời báo cho bạn biết, hoặc gợi ý import nếu đó là tài khoản nó chưa quản lý.
- 🔐 **Credential nằm trong keyring của hệ điều hành** (macOS dùng Keychain qua `/usr/bin/security`, Linux dùng libsecret). SQLite chỉ lưu tham chiếu; token không bao giờ ra khỏi keyring.
- ⬆️ **Tự cập nhật** — kiểm tra bản mới trên GitHub rồi cập nhật qua Homebrew sau khi bạn đồng ý. Không bao giờ tự cài khi chưa hỏi.
- 📡 **Chạy hoàn toàn ở máy bạn.** Không telemetry, không gửi dữ liệu đi đâu.

## Cài đặt

### macOS (Sonoma 14 trở lên)

```sh
brew install --cask japananh/tap/aimonitor
```

> **Lần đầu mở:** app chưa được notarize (việc này nằm trong kế hoạch). Bạn gỡ cờ cách ly của Gatekeeper một lần:
> ```sh
> xattr -dr com.apple.quarantine /Applications/AIMonitor.app
> ```
> Hoặc chuột phải → Open → xác nhận. Chi tiết ở [`docs/unsigned-app.md`](docs/unsigned-app.md).

### Linux (Ubuntu 22.04 trở lên)

```sh
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh
```

Trên Linux mới chỉ có CLI; widget trên menu bar (GTK) để dành cho v2.0.

### Dùng `go install` (chỉ CLI, mọi nền tảng)

```sh
go install github.com/japananh/aimonitor/cmd/aimonitor@latest
```

Lệnh này cài `aimonitor` vào `$GOBIN`. Không có app, không có dịch vụ tự chạy lúc đăng nhập — hợp khi bạn chỉ cần CLI để đổi tài khoản trong terminal, hoặc không muốn thêm Homebrew tap.

## Bắt đầu nhanh

```sh
# 1. Đăng ký luôn tài khoản Claude Code bạn đang đăng nhập làm tài khoản đầu tiên.
#    --adopt-current lấy credential sẵn có trong keychain, khỏi phải đăng nhập OAuth lại.
aimonitor add --adopt-current --label personal

# 2. Thêm tài khoản thứ hai. aimonitor cất tạm credential hiện tại, in hướng dẫn rồi
#    theo dõi keychain. Bạn mở terminal khác chạy `claude` + `/login` là xong.
aimonitor add --label work

# 3. Đổi tài khoản ngầm — không terminal, không /login.
aimonitor switch work

# 4. Xem mức dùng 5h / 7d của từng tài khoản.
aimonitor list

# 5. Kiểm tra tình trạng.
aimonitor doctor
```

Đang xài tool đổi tài khoản khác (ví dụ claude-bar)? Khỏi thêm tay từng cái — import hết một lần:

```sh
aimonitor import
```

Auto-swap bật sẵn ở mức 80% (mức dùng trong 5 giờ). Dùng bình thường thì khỏi chỉnh gì thêm.

## Cấu hình

```sh
aimonitor config set auto_swap.enabled true       # mặc định true
aimonitor config set auto_swap.threshold_pct 80   # mặc định 80
aimonitor config set autostart true                # chạy daemon lúc đăng nhập
```

<details>
<summary><b>Tất cả khóa cấu hình</b></summary>

| Khóa | Mặc định | Mô tả |
|---|---|---|
| `auto_swap.enabled` | `true` | Công tắc chính cho việc tự đổi tài khoản dựa trên mức dùng OAuth |
| `auto_swap.threshold_pct` | `80` | Mức dùng 5 giờ (%) để bắt đầu tự đổi |
| `auto_swap.grace_sec` | `60` | Số giây từ lúc báo "sắp đổi tài khoản" đến lúc đổi thật, đủ để bạn dứt phiên `claude` đang chạy. Để `0` là đổi ngay. |
| `auto_update.enabled` | `true` | Khởi động lên thì kiểm tra bản mới trên GitHub và báo cho bạn. Không bao giờ tự cài khi chưa xác nhận. |
| `autostart` | `false` | Chạy daemon lúc đăng nhập |
| `autoswitch` | `false` | (Cũ) bộ đếm theo JSONL kiểu tripwire, nay đã thay bằng nhóm khóa `auto_swap.*`. Đặt khóa này sẽ bị từ chối. |

</details>

## Cách hoạt động

Khi tài khoản đang dùng chạm ngưỡng 5 giờ bạn đặt, aimonitor tìm tài khoản còn dư nhiều nhất rồi đổi ngầm:

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

Muốn xem đầy đủ cách daemon / store / widget ráp lại với nhau thì đọc [`docs/architecture.md`](docs/architecture.md).

## Riêng tư & bảo mật

- **Không telemetry, không gửi dữ liệu đi.** Ở bất kỳ đâu.
- OAuth token chỉ nằm trong keyring của hệ điều hành. SQLite chỉ giữ tham chiếu, không bao giờ giữ thứ bí mật.
- Không bao giờ ghi token ra log, kể cả khi bật `--debug`. Bộ lọc log khớp `sk-ant-(oat|ort)…`.
- **aimonitor chỉ gửi ra mạng đúng những request này:**
  - `GET https://api.anthropic.com/api/oauth/usage` — chỉ để tra cứu, mỗi lần ~5 KB, không tốn token. Nhịp chạy nền: tài khoản đang dùng cứ 5 phút ± 30 giây, gặp lỗi thì giãn dần (tối đa 1 giờ). Các tài khoản còn lại được lấy lần lượt từng cái, chậm rãi (chỉ khi token còn hạn — không tự refresh ngầm), hoặc lấy ngay khi bạn bấm nút làm mới của từng tài khoản / nút "Refresh usage".
  - `POST https://platform.claude.com/v1/oauth/token` — refresh access token sắp hoặc đã hết hạn, lúc đổi tài khoản, lúc bạn làm mới mức dùng thủ công, hoặc ngay trước khi quyết định auto-swap. Làm ngầm, không mở trình duyệt.
  - `GET https://api.github.com/repos/japananh/aimonitor/releases` — để kiểm tra cập nhật. Không cần đăng nhập, không gửi thông tin gì về bạn; chạy lúc khởi động (nếu bật `auto_update.enabled`) và khi bạn bấm "Check for Updates". Cài cập nhật thì gọi Homebrew, và chỉ chạy sau khi bạn xác nhận.
- Lệnh CLI cũ `aimonitor probe` có gọi một request `/v1/messages` thật và đã ngừng dùng. Daemon không còn đụng tới nó.

Mô hình rủi ro đầy đủ ở [`docs/security.md`](docs/security.md).

## Lộ trình

Chỉ là định hướng, chưa cam kết.

- **v1.1:** biểu đồ mức dùng theo ngày, ước tính chi phí từng tài khoản, app macOS đã notarize.
- **v1.2 (tùy việc notarize ở v1.1):** đưa lên `homebrew/cask` để `brew install aimonitor` chạy được mà không cần thêm tap bên thứ ba.
- **v2.0:** widget menu bar GTK cho Ubuntu, thêm một `Provider` thứ hai (Codex hoặc Copilot CLI).

## Gỡ cài đặt

```sh
aimonitor uninstall              # tắt tự chạy; vẫn giữ dữ liệu
aimonitor uninstall --purge      # xóa luôn SQLite DB, cấu hình và các mục keyring của aimonitor

# macOS
brew uninstall --cask aimonitor
brew untap japananh/tap          # tùy chọn

# Linux
systemctl --user disable aimonitor.service
sudo rm /usr/local/bin/aimonitor
```

Khi gỡ, aimonitor **không bao giờ đụng** tới mục keyring `Claude Code-credentials` gốc của bạn — các phiên `claude` CLI đang đăng nhập vẫn chạy ngon.

## Build từ mã nguồn

Cần Go 1.25 trở lên. Thuần Go trên mọi nền tảng — `CGO_ENABLED=0` chạy được cả trên macOS (truy cập keychain bằng cách gọi `/usr/bin/security` chứ không link framework Security qua cgo).

```sh
git clone https://github.com/japananh/aimonitor
cd aimonitor
make build              # binary CLI Go
make test               # unit test
make widget             # build AIMonitor.app qua Swift Package Manager (chỉ macOS)
make release-snapshot   # chạy thử goreleaser đầy đủ (không publish; cần cài goreleaser)
```

Trên macOS, build widget cần Swift toolchain (`xcode-select --install`). Không cần Xcode đầy đủ; widget build không cần giao diện qua Swift Package Manager.

## Tài liệu

| Chủ đề | Ở đâu |
|---|---|
| Kiến trúc (daemon, store, widget) | [`docs/architecture.md`](docs/architecture.md) |
| Mô hình rủi ro + luật lọc log | [`docs/security.md`](docs/security.md) |
| Vì sao app macOS chưa notarize | [`docs/unsigned-app.md`](docs/unsigned-app.md) |
| Các user story đã làm trong v1 | [`USER_STORIES.md`](USER_STORIES.md) |

## Xem thêm

- [ncthanhngo/claude-bar](https://github.com/ncthanhngo/claude-bar) — app menu bar macOS cùng kiểu, aimonitor học theo nhiều cách làm từ đây (gọi keychain, luồng refresh OAuth, sổ tài khoản).

## Giấy phép

[MIT](LICENSE) © [@japananh](https://github.com/japananh)
