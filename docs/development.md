# 开发与验证

[文档首页](README.md) · [安装](installation.md)

需要 Go 1.26+；从仓库根目录执行下列命令。

```sh
make help                  # 查看可用目标
make                       # 默认构建 bin/ah
make run ARGS="c A"         # 构建并连接 A
make fmt                   # 格式化
make check                 # 构建、静态检查和全部测试
make test-race             # 竞态检测
make e2e                   # Docker 中真实 OpenSSH 端到端测试
make clean                 # 清理构建产物
```

也可以直接使用 `go build -o bin/ah ./cmd/ah`、`go install ./cmd/ah`、`go test ./...` 等 Go 命令。

测试启动本地 SSH/SFTP 服务，使用临时密钥和配置；Bash/Zsh/Fish 测试在隔离的伪终端中实际按 Tab 并复制含特殊字符的文件。缺少对应 shell 时跳过该 shell 测试。

## Docker 端到端测试

`make e2e` 需要 Docker，并需在首次构建镜像时访问 Alpine 软件源。测试用 `e2e/sshd` 构建真实 OpenSSH（Alpine）镜像，另起一台仅在 Docker 内网可达的 Web 服务，用编译出的 `ah` 验证以下场景：

- 首次主机指纹确认、拒绝连接与密钥变化后的拒绝。
- 远程命令的输入输出和退出码、交互 shell、保存密码登录，以及 `pick` 和 `exec`。
- 文件与目录互传、权限、中文路径、特殊文件跳过、合并、备份与还原、相同文件跳过、预览、排除、续传和同步。
- 复制历史查询与重跑、配置迁移、命名 SSH 跳板。
- 本地、动态与远程转发，包括 GatewayPorts、日志和生命周期操作。
- 免密和带密码的 sudo，以及 root 权限下的复制。

交互提示通过伪终端驱动。

每次运行使用独立的临时 HOME、配置、known_hosts、主密钥和历史，结束时删除容器与网络，只保留 `ah-e2e-sshd` 镜像作构建缓存。镜像里的密码和运行时注入的密钥都是一次性的测试数据。未安装 Docker 或守护进程不可用时测试跳过；普通 `go test ./...` 不会运行它（`e2e` 构建标签）。

## 项目约定与依赖

设计和开发约定见 [AGENTS.md](../AGENTS.md)。依赖接口参考 [Cobra 补全文档](https://cobra.dev/docs/how-to-guides/shell-completion/)、[SFTP API](https://pkg.go.dev/github.com/pkg/sftp)、[SSH API](https://pkg.go.dev/golang.org/x/crypto/ssh) 和 [TOML API](https://pkg.go.dev/github.com/pelletier/go-toml/v2)。
