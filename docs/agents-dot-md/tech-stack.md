# 技术栈与验证

> 引入依赖、实现 Go 代码或执行构建时读取。

Go 1.26+；模块 github.com/benenen/ah，版本以 go.mod/go.sum 为准。

- Go crypto/aes、crypto/cipher：AES-256-GCM 密码加密与随机 nonce；主密钥独立存储。
- modernc.org/sqlite、database/sql：无 CGO SQLite 复制历史，参数化搜索和状态持久化。
- Cobra：命令与动态补全；Bash 脚本补充无 bash-completion 时的词法兼容逻辑。
- go-toml/v2：严格 TOML 编解码；gofrs/flock：配置与 known_hosts 文件锁。
- x/crypto/ssh：认证、会话和 known_hosts；pkg/sftp：目录查询与流式文件复制。
- x/net/proxy：按连接的 SOCKS5 代理拨号，转发 dialer 携带超时、context 控制取消。
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
