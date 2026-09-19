# ah

Go 编写的 SSH 连接管理 CLI。使用 TOML 保存命名连接，支持 SSH 本地端口转发与后台管理、本地与远端文件互传、Bash/Zsh/Fish 路径 Tab 补全，并使用 SQLite 保存可查询、可重跑的复制历史。

完整参数与配置见 [CLI 文档](docs/cli.md)；agent 操作与安装见 [ah-cli skill](skills/ah-cli/SKILL.md) 和 [安装说明](docs/cli.md#agent-使用)。

## 命令简写

| 完整命令 | 简写 |
| --- | --- |
| `connect` | `c` |
| `copy` | `cp` |
| `edit` | `e` |
| `forward` | `f` |
| `history` | `h` |
| `list` | `ls` |
| `new` | `n` |
| `remove` | `rm` |

完整命令与简写使用相同参数，均在 `ah --help` 中标注。`ah h` 查询历史，`ah -h` 显示帮助。

## 安装

需要 Go 1.26 或更新版本。交互终端支持 macOS、Linux。

```sh
make build
# 或安装到 $(go env GOPATH)/bin：
make install
```

后续示例假设 `ah` 已加入 PATH。

## 本地端口转发

```sh
ah f A 8080 80 -d
# 本地 127.0.0.1:8080 → SSH 服务器 A 上的 127.0.0.1:80
ah f A 0.0.0.0:8080 80
# 显式监听本机所有 IPv4 网卡
ah f A 15432 database.internal:5432
ah f A '[::1]:8080' '[::1]:80'
ah f ls
ah f kill <ID>
```

用法为 `ah forward NAME LOCAL TARGET`，可简写为 `ah f NAME LOCAL TARGET`，两个地址分别传参，不使用 `-L`。

- `NAME`：已保存的 SSH 连接名。
- 第一个端口 `LOCAL`：**本地监听端口**，例如 `8080` 或 `0.0.0.0:8080`。
- 第二个参数 `TARGET`：**SSH 服务器侧的目标服务端口**，例如 `80` 表示 SSH 服务器上的 `127.0.0.1:80`；也可指定由该服务器访问的 `HOST:PORT`。

例如 `ah f A 8080 80 -d` 表示「本地 8080 → SSH 服务器 A 上的 80」。
这里的 `80` 是目标服务端口，SSH 登录端口仍使用连接配置中的 `port`。

两端只写端口时均默认 `127.0.0.1`；显式地址使用 `HOST:PORT`，IPv6 地址需加方括号。
目标从 SSH 服务器访问和解析。端口范围为 1–65535，每次命令创建一个 TCP 转发，可同时服务多个连接。

每次启动自动生成 ID。加 `-d` / `--daemon` 后脱离终端运行，SSH 和本地监听准备就绪后才返回 ID；
不加则在前台运行，Ctrl+C 关闭监听、活动连接及 SSH 连接。后台模式支持 Linux/macOS，使用已保存密码、可用私钥或 SSH agent，不能交互输入密码或私钥口令。
`ah f ls` 显示当前用户的所有转发记录，包括 ID、连接名、本地/目标地址、PID、状态和错误；
`ah f kill ID` 通过私有控制通道停止对应转发，并等待关闭完成。前台转发也可按 ID 停止。
停止和失败记录保留；异常中断留下的 running 记录在控制通道不可达时显示 stale，不根据旧 PID 杀进程。
状态与后台日志位于用户配置目录的 `ah/forwards/<ID>.json` 和 `<ID>.log`，独立于 `--config` 指定的连接文件。
后台进程不自动重连，也不在系统重启后自动恢复。
复用连接配置中的 SSH 认证、SOCKS5 代理链及主机密钥校验，支持连接名 Tab 补全。
转发由 SSH 服务处理，不执行 shell 或 sudo；服务端须允许 TCP 转发。
监听失败、SSH 断开、目标连接失败或数据传输错误会结束命令并返回非零退出码。
`--timeout` 同时限制 SSH 建连及每次目标连接建立时间，不限制已建立连接的传输时长。

## 管理连接

```sh
ah new A --host a.example.com --user alice --identity-file ~/.ssh/id_ed25519
ah new B --host b.example.com --user bob --port 2222 --password
# 隐藏输入密码，加密保存；new 只保存配置，不要求主机在线。
ah list
ah edit B --host new-b.example.com --port 22
ah edit B --password         # 更新保存的密码
ah edit B --clear-password   # 删除保存的密码
ah edit A --identity-file ''   # 清除显式密钥，使用 agent / 默认密钥
ah new P --host 10.0.0.9 --user dave --password --proxy socks5://127.0.0.1:1080
# 经 SOCKS5 代理连接（含 connect/c、cp 与远程补全）；host 存目标真实地址。
ah edit P --clear-proxy       # 取消代理，恢复直连
ah new C --host c.example.com --user carol --password --sudo
# 连接后自动 sudo 到 root；sudo 需要密码时使用独立保存的密码或交互输入。
ah edit C --sudo-password     # 单独设置并加密保存 sudo 密码
ah edit C --no-sudo           # 关闭自动 sudo
ah edit C --clear-sudo-password   # 删除保存的 sudo 密码，需要时交互输入
ah edit C --term xterm-256color   # 远端 terminfo 没有本地 TERM 时固定一个它认识的名字
ah edit C --clear-term            # 恢复使用本地 $TERM
ah rm B
ah c A                  # 打开交互 SSH 命令行
ah connect A                 # 等价命令
```

`ah c <name>` 是 `connect` 的别名。不带命令时打开交互 SSH 终端，带命令时执行远程命令后退出：

```sh
ah c nas ls -lah /home
ah connect nas 'cd /home && ls -lah | head -20'
printf 'hello\n' | ah c nas cat
ah --timeout 15s c nas uname -a
```

与 SSH 一样，连接名后的参数用空格连接后交给远程 shell 解析；包含管道、重定向或需要保留的引号时，用引号包住完整远程命令。`ah` 的全局选项须放在连接名前，连接名后的 `--help` 等选项也属于远程命令。

远程命令模式不申请 PTY，支持标准输入管道，分别转发 stdout/stderr，并保留远端非零退出码。连接名 Tab 补全和原有认证选项继续可用。

启用 `--sudo` 后，`connect/c`、`cp` 的远端操作和远程补全均以 root 执行。sudo 请求密码时解密独立保存的 sudo 密码；未保存则在交互终端隐藏输入，免密 sudo 不要求密码。SSH 登录密码不自动用于 sudo。补全不弹提示，需要保存的 sudo 密码或免密 sudo。sudo 复制需要远端独立 `sftp-server`，可用 `edit NAME --sftp-server /usr/lib/openssh/sftp-server` 指定路径。旧版加密 sudo 密码和 `--no-sudo` 用法仍兼容。

交互会话按 OpenSSH 的做法申请 PTY：把本地终端设置（erase 等控制字符、IUTF8 等标志、波特率）随请求发给远端，并转发本地 `TERM`。如果远端 terminfo 数据库里没有这个终端类型（例如 Ghostty 的 `xterm-ghostty` 遇上较旧的发行版），readline 会退化成哑终端行为，最典型的症状是退格只让光标移动、字符不消失。此时用 `edit NAME --term xterm-256color` 为该连接固定一个远端认识的名字，或临时用全局选项 `ah --term xterm-256color c NAME` 覆盖；优先级为 `--term` > 连接配置 `term` > 本地 `$TERM`。另一种做法是把本地 terminfo 装到远端：`infocmp -x $TERM | ah c NAME 'tic -x -'`。

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
- `new/edit --password` 隐藏输入 SSH 密码，以 AES-256-GCM 加密后写入 TOML 的 `password` 字段（`enc:v1:` 格式）；保存的密码用于 `connect/c/cp` 和远程补全，并优先于密钥认证。普通连接时临时输入的密码和私钥口令不会保存。
- 加密主密钥独立保存在 `os.UserConfigDir()/ah/master.key`，可用 `--key-file` 指定。首次保存密码时随机生成，文件权限为 0600；解密时缺失密钥会报错，不会重新生成。备份和迁移时需要同时保管 TOML 与对应密钥，丢失密钥后须重新设置密码。拿到两者的人可以解密，因此密钥应单独妥善保管。
- 默认校验 `~/.ssh/known_hosts`；`--known-hosts /path/known_hosts` 可覆盖。
- 未知主机默认拒绝。先按自己的可信渠道核验主机指纹，再显式使用 `--trust-new-host` 接受并记录首次见到的密钥；已有主机密钥发生变化时仍拒绝。该选项不使补全操作写入信任记录。

```sh
ah --trust-new-host connect A
ah --known-hosts ./test-known-hosts --timeout 15s connect A
```

`--timeout` 默认 10 秒，约束认证、连接与握手阶段；不会给整次文件传输设置总时长上限。Ctrl-C 可取消复制或密码输入；交互 SSH 中 Ctrl-C 发送给远端终端。

## SOCKS5 多跳

```sh
# 本机 → 第一跳 → 第二跳 → nas
ah edit nas --proxy socks5://127.0.0.1:1080 --proxy socks5://proxy2.internal:1080
ah edit nas --clear-proxy
```

`new/edit` 支持重复 `--proxy`，按顺序保存到 TOML 的 `proxies` 数组。edit 替换整条链，未指定则保留。旧 `proxy` 字符串仍兼容，但不能与数组同时配置。登录、复制、历史重跑与远程补全共用代理链；后续域名由上一跳解析，失败不回退直连。代理 URL 认证信息仍以原文保存，不受 SSH/sudo 密码加密保护。

## 文件复制

```sh
ah cp ./README.md nas:/home/       # 本地上传
ah cp nas:/home/report.csv ./      # 下载到本地
ah cp ./report.csv ./backup.csv   # 本地复制
ah cp A:/var/data/report.csv B:/home/bob/report.csv
ah cp 'A:~/reports/季度 报告.csv' B:~/uploads/
ah cp --force A:~/report.csv B:~/report.csv
```

没有 `NAME:` 前缀的路径表示本地文件，相对路径按执行目录解析；文件名含冒号时可使用 `./name:part` 避免被识别为连接别名。本地 `~` 表示本机用户目录。远端到远端时，两端分别建立 SSH/SFTP 连接，文件流经运行 ah 的机器，无需 A 能直接连接 B。目标为已有目录时使用源文件名；目标父目录须已存在。远端 `~` 表示该连接的 SFTP 登录目录；不支持 `~otheruser`。

默认拒绝覆盖，采用同目录临时文件与 `hardlink@openssh.com` 扩展原子发布，防止并发创建时覆盖目标。`--force` 使用 `posix-rename@openssh.com` 原子替换。目标服务端不支持对应扩展时明确报错。本地目标使用对应的本地硬链接或原子重命名。目标最终权限取源文件的普通 Unix 权限位；不复制属主、时间戳或 ACL。

复制失败不报告成功；能连通时清理临时文件。断线或取消导致无法清理时，错误会指出可能残留的 `.ah-copy-*` 路径。传输完成但发布响应丢失时，目标可能已经存在，应检查目标后再重试。

当前支持单个普通文件；目录递归、跳板机和断点续传尚未实现。

## 复制历史

```sh
ah history                     # 最近 20 条
ah history README nas          # 多个关键词同时匹配，不区分大小写
ah history search report --limit 50
ah history show 12             # 输出可复制的 shell 命令
ah history run 12              # 重跑，生成新的历史记录
```

历史默认保存在 `os.UserConfigDir()/ah/history.db`，可用全局 `--history-file /path/history.db` 覆盖。SQLite 文件权限为 0600，记录源/目标、执行目录、配置路径、开始/结束时间、字节数、覆盖选项、状态与错误；不保存密码或密钥内容。查询支持路径、状态和错误的关键词子串匹配。

每次参数数量正确的 `cp` 在传输前记为 `running`，结束后更新为 `success`、`failed` 或 `canceled`；进程被强制终止时可能保留 `running`。无法打开或写入历史时不会开始复制。本地目标不能覆盖当前历史数据库及其 SQLite 辅助文件。

重跑沿用原来的工作目录、覆盖选项和配置/主密钥/known_hosts 路径，连接定义使用该配置文件中的当前值；可通过全局选项显式覆盖配置路径。`history run` 直接调用复制逻辑，不执行数据库中的 shell 文本。原记录未使用 `--force` 时，重跑也会拒绝覆盖已有目标。首次主机信任授权不会保存在历史中。

## Tab 补全

**必须先加载补全脚本**，否则 `ah cp NAME:PATH<Tab>` 走的是 shell 默认文件名补全，不会提示远程路径。

为当前终端临时加载：

```sh
# Bash
source <(ah completion bash)

# Zsh
autoload -Uz compinit && compinit
source <(ah completion zsh)
# Fish
ah completion fish | source
```

永久启用（每次开终端自动加载），把对应片段写入 `~/.bashrc` 或 `~/.zshrc`：

```sh
# Bash（写入 ~/.bashrc）
if command -v ah >/dev/null 2>&1; then
    source <(ah completion bash)
fi

# Zsh（写入 ~/.zshrc）
autoload -Uz compinit && compinit
if command -v ah >/dev/null 2>&1; then
    source <(ah completion zsh)
fi
```

写入后新开终端，或在当前终端重新 `source` 上面的命令即可生效。Bash 脚本包含未安装 bash-completion 时的兼容逻辑，也能在装有 bash-completion（`COMP_WORDBREAKS` 含 `:`）时正确处理 `NAME:PATH` 的冒号；Zsh 使用自带的 compinit。

```text
ah c <Tab>                  # 连接名
ah cp ./REA<Tab>             # 本地源路径
ah cp A:~/report.csv ./ba<Tab> # 本地目标路径
ah cp A:<Tab>               # A 的登录目录
ah cp A:~/rep<Tab>           # A 的远程源路径
ah cp A:~/report.csv B:~/u<Tab>  # B 的远程目标路径
```

目录候选追加 `/`，可以继续补全下一级；空格、中文及 shell 特殊字符由补全脚本转义。输入开引号后按 Tab，唯一文件候选会由 shell 自动闭合引号，无需再手动输入同一个闭合引号。远程查询最多 3 秒，失败时无候选，不弹出密码或信任提示。首次使用前应先完成主机信任，使用加密私钥时可先执行 `ssh-add`。

## 开发与验证

```sh
make help                  # 查看可用目标
make                       # 默认构建 bin/ah
make run ARGS="c A"         # 构建并连接 A
make fmt                   # 格式化
make check                 # 构建、静态检查和全部测试
make test-race             # 竞态检测
make clean                 # 清理构建产物
```

也可以直接使用 `go build -o bin/ah ./cmd/ah`、`go install ./cmd/ah`、`go test ./...` 等 Go 命令。

测试启动本地 SSH/SFTP 服务，使用临时密钥和配置；Bash/Zsh/Fish 测试在隔离的伪终端中实际按 Tab 并复制含特殊字符的文件。缺少对应 shell 时跳过该 shell 测试。

设计和开发约定见 [AGENTS.md](AGENTS.md)。依赖接口参考 [Cobra 补全文档](https://cobra.dev/docs/how-to-guides/shell-completion/)、[SFTP API](https://pkg.go.dev/github.com/pkg/sftp)、[SSH API](https://pkg.go.dev/golang.org/x/crypto/ssh) 和 [TOML API](https://pkg.go.dev/github.com/pelletier/go-toml/v2)。

### Fish 持久加载与目录选择

在仓库中临时启用：

```fish
./bin/ah completion fish | source
```

永久启用（已通过 make install 安装 ah 并加入 PATH）：

```fish
mkdir -p ~/.config/fish/completions
ah completion fish > ~/.config/fish/completions/ah.fish
```

`/home` 与登录目录 `~` 不等价。先用 `ah c NAME pwd` 确认登录目录；例如 root 通常登录到 `/root`，应补全 `NAME:/root/` 或 `NAME:~/`。空目录没有候选。可以用 `ah __complete cp ./README.md NAME:/root/` 单独检查候选；末尾 `:数字` 是 shell 补全协议，不是文件名。

历史重跑默认继承原覆盖设置。目标已存在且确定需要替换时，使用 `ah h run 1 --force`（简写 `-f`）；使用 `--force=false` 可显式禁止继承覆盖。每次重跑都会生成新的历史 ID，所以错误中的 ID 可能不同于重跑的原 ID。

`ah history clean`（`ah h clean`）删除 success/failed/canceled 历史并显示删除条数，保留 running 记录以免干扰进行中的复制；不重置历史 ID。仅清理 `--history-file` 指定的历史库，不删除连接配置或复制文件。此操作不能撤销，agent 仅在用户要求清理历史时执行。

历史清理支持筛选：

```sh
ah h clean --failed                # 仅删除失败记录
ah h clean --keep-days 7           # 保留最近 7 天，删除更早的已结束记录
ah h clean --failed --keep-days 7  # 仅删除 7 天前的失败记录
```

天数取值 1–36500，按开始时间计算，每天为 24 小时；条件组合取交集，截止时间及之后的记录保留。running 始终保留；不带筛选时仍清理全部已结束记录。
