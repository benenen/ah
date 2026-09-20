# 技术栈与验证

> 引入依赖、实现 Go 代码或执行构建时读取。

Go 1.26+；模块 github.com/benenen/ah，版本以 go.mod/go.sum 为准。

- Go crypto/aes、crypto/cipher：AES-256-GCM 密码加密与随机 nonce；主密钥独立存储。
- modernc.org/sqlite、database/sql：无 CGO SQLite 复制历史，参数化搜索和状态持久化。
- Cobra：命令与动态补全；Bash 脚本补充无 bash-completion 时的词法兼容逻辑。
- go-toml/v2：严格 TOML 编解码；gofrs/flock：配置与 known_hosts 文件锁。
- x/crypto/ssh：认证、会话和 known_hosts；pkg/sftp：目录查询与流式文件复制。
- x/net/proxy：按顺序嵌套 SOCKS5 拨号，整条代理链与 SSH 握手共享超时、context 控制取消。
- x/term、x/sys/unix：macOS/Linux 终端、窗口大小、可取消输入；creack/pty 用于测试。

新增依赖前复用已有能力，核实官方文档兼容性并固定版本。底层不依赖 Cobra；密码输入与信任决策由 CLI 提供，补全不传交互回调，可提供保存密码的非交互解密回调。

```sh
gofmt -w <本次修改的Go文件>
go build ./...
go test ./...
go vet ./...
go test -race ./...
```

测试使用本地受控 SSH/SFTP 服务、临时密钥及真实 Bash/Zsh PTY，不连接业务主机。shell 不存在时相关测试明确跳过。交互 SSH 支持 macOS/Linux，其他平台返回明确的不支持错误。

`.github/workflows/ci.yml` 在 ubuntu 与 macOS 上跑同一组命令（gofmt 检查、vet、build、test、race），Linux 额外校验 go.mod/go.sum 已 tidy。终端与守护进程有独立的 darwin/linux 分支，因此两个平台都不能省。补全用例按 shell 查找结果跳过，CI 在 Linux 额外安装 zsh/fish，否则 Ubuntu 只会覆盖 bash。macOS runner 上没有 fish，fish 补全只在 Linux 覆盖。

另一个 job 在 ubuntu 上做 Windows 交叉编译（`GOOS=windows go vet/build`），不跑测试——终端、pty 与守护进程用例需要真实 unix 主机。它守住 darwin/linux 之外的平台分支：终端与密码输入返回明确的不支持错误，`internal/history` 的 `prepare_unix.go`/`prepare_other.go` 让非 unix 平台也能编译（非 unix 没有 `O_NOFOLLOW`，不拒绝符号链接）。dragonfly 与 solaris 因 modernc.org/libc 不支持而无法编译，与 ah 无关。

`.github/workflows/release.yml` 由 `v*` 标签触发发布，也可手动 dry run（照常构建与算校验和，但不发布）。check 任务在 ubuntu 重跑 vet/test，因为标签能指向任意提交，不能假定 CI 已在该树上跑过。build 用 3×2 矩阵编译 linux/darwin/windows × amd64/arm64：`CGO_ENABLED=0`（SQLite 驱动是纯 Go，产物不依赖宿主 libc）、`-trimpath`、`-ldflags "-s -w -X …"` 注入版本/commit/构建时间，产物为含 `LICENSE` 的 tar.gz（Windows 为 zip）。publish 汇总 SHA-256 并创建 Release，Release 已存在时改为补传。注入的变量在 `internal/cli/version.go`，源码构建保持 `dev`。
