# ah

Go 编写的 SSH 连接管理 CLI。使用 TOML 保存命名连接，通过 SFTP 在两台远程主机间复制文件，并支持 Bash/Zsh 远程路径 Tab 补全。

## 安装

需要 Go 1.26 或更新版本。交互终端支持 macOS、Linux。

```sh
go build -o bin/ah ./cmd/ah
# 或安装到 $(go env GOPATH)/bin：
go install ./cmd/ah
```

后续示例假设 `ah` 已加入 PATH。

## 管理连接

```sh
ah new A --host a.example.com --user alice --identity-file ~/.ssh/id_ed25519
ah new B --host b.example.com --user bob --port 2222
ah list
ah edit B --host new-b.example.com --port 22
ah edit A --identity-file ''   # 清除显式密钥，使用 agent / 默认密钥
ah rm B
ah connect A
```

连接别名只允许字母、数字、下划线和连字符，首字符必须为字母或数字。`new` 要求 host 和 user；`edit` 只修改明确传入的字段。`rm` 仅删除连接配置。

配置默认存放在 `os.UserConfigDir()/ah/connections.toml`：macOS 通常为 `~/Library/Application Support/ah/connections.toml`，Linux 通常为 `~/.config/ah/connections.toml`（遵循 `XDG_CONFIG_HOME`）。也可以每条命令传入 `--config /path/connections.toml`。

```toml
[connections.A]
host = "a.example.com"
port = 22
user = "alice"
identity_file = "~/.ssh/id_ed25519"

[connections.B]
host = "b.example.com"
port = 2222
user = "bob"
```

未填写 port 时默认 22。未知字段和损坏 TOML 会报错；修改使用文件锁和原子替换，Unix 配置文件权限为 0600，新建配置目录为 0700。手工编辑配置不参与 ah 的文件锁，应避免与管理命令同时写入。

## 认证与主机校验

- 支持显式私钥、`SSH_AUTH_SOCK` agent，以及默认的 `~/.ssh/id_ed25519`、`~/.ssh/id_rsa`。
- 已解锁的显式加密密钥可以从 agent 使用；否则在交互终端输入私钥口令。密钥认证失败后，可按服务端认证方式提示输入密码。
- 密码和口令不写入 TOML。自动化和 Tab 补全使用无需提示的密钥或已解锁 agent。
- 默认校验 `~/.ssh/known_hosts`；`--known-hosts /path/known_hosts` 可覆盖。
- 未知主机默认拒绝。先按自己的可信渠道核验主机指纹，再显式使用 `--trust-new-host` 接受并记录首次见到的密钥；已有主机密钥发生变化时仍拒绝。该选项不使补全操作写入信任记录。

```sh
ah --trust-new-host connect A
ah --known-hosts ./test-known-hosts --timeout 15s connect A
```

`--timeout` 默认 10 秒，约束认证、连接与握手阶段；不会给整次文件传输设置总时长上限。Ctrl-C 可取消复制或密码输入；交互 SSH 中 Ctrl-C 发送给远端终端。

## 远程复制

```sh
ah cp A:/var/data/report.csv B:/home/bob/report.csv
ah cp 'A:~/reports/季度 报告.csv' B:~/uploads/
ah cp --force A:~/report.csv B:~/report.csv
```

两端分别建立 SSH/SFTP 连接，文件流经运行 ah 的机器，无需 A 能直接连接 B。目标为已有目录时使用源文件名；目标父目录须已存在。`~` 表示该连接的 SFTP 登录目录；不支持 `~otheruser`。

默认拒绝覆盖，采用同目录临时文件与 `hardlink@openssh.com` 扩展原子发布，防止并发创建时覆盖目标。`--force` 使用 `posix-rename@openssh.com` 原子替换。目标服务端不支持对应扩展时明确报错。目标最终权限取源文件的普通 Unix 权限位；不复制属主、时间戳或 ACL。

复制失败不报告成功；能连通时清理临时文件。断线或取消导致无法清理时，错误会指出可能残留的 `.ah-copy-*` 路径。传输完成但发布响应丢失时，目标可能已经存在，应检查目标后再重试。

当前支持单个普通文件；目录递归、本地端点、跳板机和断点续传尚未实现。

## Tab 补全

为当前终端加载：

```sh
# Bash
source <(ah completion bash)

# Zsh
autoload -Uz compinit && compinit
source <(ah completion zsh)
```

可将对应命令写入 `~/.bashrc` 或 `~/.zshrc`。Bash 脚本包含未安装 bash-completion 时的兼容逻辑；Zsh 使用自带的 compinit。

```text
ah connect <Tab>             # 连接名
ah cp A:<Tab>               # A 的登录目录
ah cp A:~/rep<Tab>           # A 的远程源路径
ah cp A:~/report.csv B:~/u<Tab>  # B 的远程目标路径
```

目录候选追加 `/`，可以继续补全下一级；空格、中文及 shell 特殊字符由补全脚本转义。输入开引号后按 Tab，唯一文件候选会由 shell 自动闭合引号，无需再手动输入同一个闭合引号。远程查询最多 3 秒，失败时无候选，不弹出密码或信任提示。首次使用前应先完成主机信任，使用加密私钥时可先执行 `ssh-add`。

## 开发与验证

```sh
go test ./...
go vet ./...
go test -race ./...
go build ./...
```

测试启动本地 SSH/SFTP 服务，使用临时密钥和配置；Bash/Zsh 测试在隔离的伪终端中实际按 Tab 并复制含特殊字符的文件。缺少对应 shell 时跳过该 shell 测试。

设计和开发约定见 [AGENTS.md](AGENTS.md)。依赖接口参考 [Cobra 补全文档](https://cobra.dev/docs/how-to-guides/shell-completion/)、[SFTP API](https://pkg.go.dev/github.com/pkg/sftp)、[SSH API](https://pkg.go.dev/golang.org/x/crypto/ssh) 和 [TOML API](https://pkg.go.dev/github.com/pelletier/go-toml/v2)。
