# 已知坑

> 测试与 CI 中不显然、容易复发的问题及做法。

- 2026-09-30 测试里 `t.Setenv("HOME", tmp)` 之后再跑 `go build` / `go env GOCACHE GOMODCACHE` → 默认缓存路径随 HOME 落进临时目录，Go 下载的模块文件只读，`t.TempDir` 清理报 `unlinkat …/go/pkg/mod/…: permission denied`，ubuntu/macOS CI 双双失败；本机因设了 `GOPATH` 不复现。做法：先编译二进制再改 HOME（`internal/cli/forward_daemon_unix_test.go` 的 `buildAh`）；本地复现用 `env -u GOPATH go test ./internal/cli -run TestForwardDaemon`。修复提交 bc8e94e。
- 2026-09-30 推送后要看 CI 结果（`gh run list --branch main`），`d73c188` 之后两次推送的 CI 失败曾被漏看；`gh run watch` 遇到 GitHub API 502 会提前退出，改用轮询 `gh run view ID --json status,conclusion`。
