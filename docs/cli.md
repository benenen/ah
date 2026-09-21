# ah 命令行参考

本文对应本仓库当前实现。`ah` 使用 TOML 管理 SSH 连接，以 SQLite 记录复制历史。示例地址和连接名为虚构值，请替换为自己的配置。

## 目录

- [准备与命令总览](#准备与命令总览)
- [全局选项](#全局选项)
- [连接管理](#连接管理)
- [交互登录与远程命令](#交互登录与远程命令)
- [本地端口转发](#本地端口转发)
- [文件复制](#文件复制)
- [历史查询与重跑](#历史查询与重跑)
- [SOCKS5 代理链](#socks5-代理链)
- [sudo 默认提权](#sudo-默认提权)
- [Tab 补全](#tab-补全)
- [配置与凭据](#配置与凭据)
- [常见问题与退出码](#常见问题与退出码)
- [Agent 使用](#agent-使用)

## 准备与命令总览

源码构建需要 Go 1.26+，也可以直接使用 [Releases](https://github.com/benenen/ah/releases) 中对应平台的压缩包。SSH 终端及远程命令支持 macOS/Linux。

```sh
make build                 # 生成 ./bin/ah
./bin/ah --help
make install               # 安装到 GOBIN 或 GOPATH/bin
```

下文以已加入 PATH 的 `ah` 为例；在仓库内可换成 `./bin/ah`。使用当前程序的 `ah <command> --help` 检查参数是否受支持。

| 命令 | 用途 |
| --- | --- |
| `ah list` | 列出连接名、主机、端口和登录用户 |
| `ah inspect NAME` | 以 JSON 输出单个连接配置；凭据只显示是否已配置 |
| `ah new NAME --host HOST --user USER` | 创建连接；不会主动连接服务器 |
| `ah edit NAME [flags]` | 只更新明确传入的字段 |
| `ah rm NAME` | 删除保存的连接记录，不删除远端文件 |
| `ah connect NAME [COMMAND [ARG...]]` | 交互登录或执行远程命令 |
| `ah c NAME [COMMAND [ARG...]]` | `connect` 的简写 |
| `ah forward NAME LOCAL TARGET` | 通过 SSH 转发本地 TCP 端口 |
| `ah cp SOURCE DESTINATION` | 单个普通文件的本地/远端复制 |
| `ah history [QUERY...]` | 查询复制历史 |
| `ah history search [QUERY...]` | 显式的历史查询子命令 |
| `ah history show ID` | 输出可复用的 shell 命令 |
| `ah history run ID` | 重跑该次复制，追加新记录 |
| `ah completion bash` / `zsh` / `fish` | 输出对应 shell 的补全脚本 |
| `ah version` | 输出版本、commit、构建时间与运行平台 |

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
| `version` | `v` |

完整命令与简写使用相同参数，均在 `ah --help` 中标注。`ah h` 查询历史，`ah v` 输出版本，`ah -h` 显示帮助。

## 本地端口转发

```sh
ah forward A 8080 80 -d
# 本地 127.0.0.1:8080 → SSH 服务器 A 上的 127.0.0.1:80
ah forward A 0.0.0.0:8080 80
# 显式监听本机所有 IPv4 网卡
ah forward A 15432 database.internal:5432
ah forward A '[::1]:8080' '[::1]:80'
ah forward ls
ah forward ls --json
ah forward kill <ID>
ah forward start <ID>
ah forward restart <ID>
ah forward rm <ID>
ah forward rm -f <ID>
```

用法为 `ah forward NAME LOCAL TARGET`，可简写为 `ah f NAME LOCAL TARGET`，两个地址分别传参，不使用 `-L`。

- `NAME`：已保存的 SSH 连接名。
- 第一个端口 `LOCAL`：**本地监听端口**，例如 `8080` 或 `0.0.0.0:8080`。
- 第二个参数 `TARGET`：**SSH 服务器侧的目标服务端口**，例如 `80` 表示 SSH 服务器上的 `127.0.0.1:80`；也可指定由该服务器访问的 `HOST:PORT`。

例如 `ah forward A 8080 80 -d` 表示「本地 8080 → SSH 服务器 A 上的 80」。
这里的 `80` 是目标服务端口，SSH 登录端口仍使用连接配置中的 `port`。

两端只写端口时均默认 `127.0.0.1`；显式地址使用 `HOST:PORT`，IPv6 地址需加方括号。
目标从 SSH 服务器访问和解析。端口范围为 1–65535，每次命令创建一个 TCP 转发，可同时服务多个连接。

新建转发时自动生成 ID。加 `-d` / `--daemon` 后脱离终端运行，SSH 和本地监听准备就绪后才返回 ID；
不加则在前台运行，Ctrl+C 关闭监听、活动连接及 SSH 连接。后台模式支持 Linux/macOS，使用已保存密码、可用私钥或 SSH agent，不能交互输入密码或私钥口令。
`ah forward ls` 显示当前用户的所有转发记录，包括 ID、连接名、本地/目标地址、PID、状态和错误；
`ah forward ls --json` 以稳定字段输出同一份列表（`id`、`name`、`listen`、`target`、`pid`、`status`、`started`、`error`），便于脚本消费，内部字段不进输出。
`ah forward kill ID` 通过私有控制通道停止对应转发，并等待关闭完成。前台转发也可按 ID 停止。
`ah forward start ID` 按原 ID 在后台启动已停止或失败的转发；对运行中的转发报错。
`ah forward restart ID` 等待旧转发停止后按原 ID 后台启动，已停止或失败时直接启动。
启动会恢复记录的配置/密钥/known_hosts 路径、工作目录和超时，使用当前连接配置；显式全局选项可覆盖路径和超时。
旧版记录未保存这些信息时使用当前默认值或显式选项。首次主机信任授权不会保存供重启复用。
`ah forward rm ID` 删除已停止或失败的记录及日志；`ah forward rm -f ID` 先停止运行中或启动中的转发再删除。
记录不可读（JSON 损坏）时普通 `ah forward rm ID` 报错并提示 `--force`，只有 `ah forward rm -f ID` 会删除，且会在 stderr 警告该记录的控制通道不可知、其工作进程可能仍在监听；文件名不是合法 ID 的游离文件不属记录，需手工清理。
控制通道不可达时不会强删或按 PID 杀进程；无法确认停止时保留记录并报错。生命周期操作按 ID 加锁，删除后保留锁文件。
停止和失败记录保留；异常中断留下的 running 记录在控制通道不可达时显示 stale，不根据旧 PID 杀进程。
单个记录文件损坏或文件名不是合法 ID 时，该条显示 corrupt 并附带原因，不影响其余记录的展示；corrupt 记录的控制通道不可知，不能核验也无法停止其工作进程。
状态与后台日志位于用户配置目录的 `ah/forwards/<ID>.json` 和 `<ID>.log`，独立于 `--config` 指定的连接文件。
后台进程不自动重连，也不在系统重启后自动恢复。
复用连接配置中的 SSH 认证、SOCKS5 代理链及主机密钥校验，支持连接名 Tab 补全。
转发由 SSH 服务处理，不执行 shell 或 sudo；服务端须允许 TCP 转发（`AllowTcpForwarding yes`）。把只在内网可达的数据库、Redis、Web 后台等 TCP 服务拉到本机端口使用，完整示例见 [经 SSH 访问内网服务](internal-services-over-ssh.md)。
监听失败、SSH 断开、目标连接失败或数据传输错误会结束命令并返回非零退出码。
`--timeout` 同时限制 SSH 建连及每次目标连接建立时间，不限制已建立连接的传输时长。

## 全局选项

| 选项 | 默认值/行为 |
| --- | --- |
| `--config PATH` | `os.UserConfigDir()/ah/connections.toml` |
| `--key-file PATH` | `os.UserConfigDir()/ah/master.key`；用于密码加解密 |
| `--history-file PATH` | `os.UserConfigDir()/ah/history.db` |
| `--known-hosts PATH` | `~/.ssh/known_hosts` |
| `--timeout DURATION` | `10s`；正数，约束 SSH/代理链握手及 sudo 初始化，不限制整个文件传输时长 |
| `--trust-new-host` | 不提示直接接受并保存首次见到的主机密钥；已知密钥变化仍报错 |
| `--help` / `-h` | 显示帮助 |

建议将全局选项放在子命令前；对于 `connect/c`，必须放在连接名之前。连接名后的一切属于远程命令。

```sh
ah --config ./connections.toml --timeout 20s c nas uname -a
```

首次连接在交互终端先按可信渠道核对指纹，再回答 yes 才接受并写入 known_hosts：

```text
The authenticity of host '172.16.11.143:22' can't be established.
Key fingerprint is SHA256:QJr5gSs2jZ+jL97fdnqUDr7VtyV41ZjkEOp+hkpjP/I.
Are you sure you want to continue connecting (yes/no)?
```

回答 no，或没有交互终端（含 `cp` 的路径补全），都拒绝连接且不写入信任记录。已有主机密钥发生变化时始终报错，不提示覆盖。`--trust-new-host` 跳过提示直接信任。等待回答的时间不计入 `--timeout`。

## 连接管理

```sh
ah new nas --host nas.example.com --user alice --port 22
ah new build --host build.example.com --user builder --identity-file ~/.ssh/id_ed25519
ah list
ah edit nas --host new-nas.example.com --port 2222
ah rm build
```

名称首字符为字母或数字，其余可用字母、数字、下划线、连字符。重复创建、编辑或删除不存在的名称都会报错。`new` 要求主机和用户名，端口默认 22。

`new` 和 `edit` 共用以下配置选项：

| 选项 | 行为 |
| --- | --- |
| `--host HOST` | 主机名或 IP |
| `--port PORT` / `-p` | 1–65535 |
| `--user USER` / `-u` | SSH 登录用户 |
| `--identity-file PATH` / `-i` | 显式私钥路径；空字符串恢复 agent/默认密钥 |
| `--password` | 隐藏输入 SSH 密码并加密保存 |
| `--proxy ADDRESS` | 保存 SOCKS5 代理；重复传入表示依次多跳 |
| `--sudo` / `--sudo=false` | 启用/关闭该连接的默认 root 提权 |
| `--sudo-password` | 隐藏输入独立的 sudo 密码并加密保存；本身不启用 sudo |
| `--sftp-server PATH` | sudo 模式使用的独立 SFTP 服务绝对路径；空字符串恢复自动探测 |

`edit` 还支持 `--no-sudo`（等价 `--sudo=false`）、`--clear-password`、`--clear-sudo-password`、`--clear-proxy`。同一项的设置和清除选项互斥。修改其他字段会保留原密码、代理和 sudo 设置。

**密码选项是开关，不接收明文参数。**

```sh
ah edit nas --password          # 出现 SSH password: 后输入并回车
ah edit nas --sudo-password     # 出现 sudo password: 后输入并回车
ah edit nas --clear-password
```

保存的 SSH 密码优先于密钥认证。未保存时支持显式私钥、agent、默认私钥及交互密码认证。临时登录时输入的密码不会自动保存。

## 交互登录与远程命令

```sh
ah c nas
ah connect nas
ah c nas ls -lah /home
ah c nas 'cd /home && ls -lah | head -20'
printf 'hello\n' | ah c nas cat
ah c nas "cat '/home/alice/a file.txt'"
```

不带命令时进入交互 shell，stdin 是终端时申请 PTY。带命令时不申请 PTY，分别转发 stdin/stdout/stderr，结束后退出。

与 SSH 一样，连接名后的参数用空格连接，再交给远程 shell 解析。需要远端执行管道、重定向、变量展开或保留含空格的单个参数时，给整个命令加本地引号，并保留必要的远端引号。未引用的 `|`、`>`、`$VAR` 由本地 shell 处理。

`ah c nas ls --help` 的 `--help` 发给远端 `ls`。查看本地连接命令帮助请用 `ah c --help`。命令模式目前没有 `-t`/`-tt` 强制 PTY 选项。

## 文件复制

端点规则：`NAME:PATH` 表示远端；没有连接名前缀就是本地。四种方向均可用：

```sh
ah cp ./README.md nas:/home/alice/       # 上传
ah cp nas:/home/alice/report.csv ./     # 下载
ah cp A:/data/report.csv B:/backup/     # 远端到远端，经本机流式中转
ah cp ./report.csv ./backup.csv        # 本地到本地
ah cp 'nas:~/季度 报告.csv' './季度 报告.csv'
ah cp ./name:part nas:/home/alice/      # 本地文件名含冒号，用 ./ 明确本地语义
```

本地相对路径基于当前工作目录。本地 `~` 指本机用户目录；远端 `~` 指 SFTP `Getwd` 返回的目录，不执行 shell 展开，也不支持 `~otheruser`。sudo 模式下不要假设 SFTP 的 `~` 一定为 `/root`，需要 root 目录时使用绝对路径。

目标为已有目录时采用源文件名；父目录须已存在。仅复制单个普通文件，不支持目录递归、远端通配符展开或断点续传。

默认拒绝覆盖；确认需要替换时使用：

```sh
ah cp --force ./report.csv nas:/home/alice/report.csv
# -f 等价于 --force
```

复制先写同目录临时文件再原子发布。远端默认要求 `hardlink@openssh.com`，覆盖要求 `posix-rename@openssh.com`；缺少扩展会报错。本地使用对应的硬链接或原子重命名。保留普通权限位，不复制属主、时间戳或 ACL。

失败时尽力清理临时文件；断线可能留下错误中指出的 `.ah-copy-*` 文件。发布响应丢失时，目标可能已存在，重试前先检查目标。

## 历史查询与重跑

```sh
ah history                         # 最近 20 条
ah history README nas              # 多关键词同时匹配
ah history search report --limit 50
ah history show 12                 # 查看具体命令
ah history run 12                  # 再执行一次复制
```

查询为大小写不敏感的关键词子串匹配，可匹配源/目标、状态、错误。多个关键词为 AND；`--limit` 范围 1–1000。

每次参数数量正确的 `cp` 在传输前写入 `running`，结束时更新为 `success`、`failed` 或 `canceled`。记录时间、字节数、源/目标、工作目录、force、配置及凭据文件路径、错误；不记录密码或密钥内容。进程被强制终止可能留下 `running`。历史无法持久化时不会开始复制。

重跑保留原工作目录、源/目标和 force，默认使用原 config/key-file/known-hosts 路径；通过全局选项可显式覆盖这些路径。连接定义使用配置文件的当前值，包括代理和 sudo。历史不会保存首次主机信任授权，也不会还原过去的文件内容。

先用 `show` 确认目标及覆盖选项，再用 `run ID`；无需对数据库内容执行 `eval`。重跑会新增记录；原记录的 force 也会继承，必须先核对覆盖意图。本地目标不能覆盖当前历史库及 SQLite 辅助文件。

## SOCKS5 代理链

```sh
ah edit nas --proxy socks5://127.0.0.1:1080
ah edit nas --proxy socks5://127.0.0.1:1080 --proxy socks5://proxy2.internal:1080
ah edit nas --clear-proxy
```

第二条命令保存“本机 → 代理 1 → 代理 2 → nas”的整条链；`edit --proxy` 替换旧链，不是追加。`new` 同样支持重复选项。

地址支持 `HOST:PORT` 或 `socks5://HOST:PORT`，IPv6 使用 `[::1]:1080`。也接受 `socks5h://HOST:PORT`。保留代理 URL 认证支持，但 URL 中的用户名/密码原样存入 TOML，不受 SSH/sudo 密码加密保护；示例使用无认证代理，避免把代理凭据写入命令记录或共享文件。它是 SOCKS5 代理链，不是 SSH `ProxyJump`。

只有第一跳在本机解析；后续代理和目标域名交给上一跳解析。链路失败不回退直连。规则随连接保存，供登录、命令、复制、历史重跑和远程补全共用。旧 `proxy = "socks5://..."` 单代理配置仍可读取，设置新的 `--proxy` 后改存 `proxies` 数组；两种字段不能同时设置，`--clear-proxy` 清除两者。

## sudo 默认提权

```sh
ah edit nas --sudo
ah edit nas --sudo-password
ah c nas
ah c nas id
ah cp ./README.md nas:/root/
ah edit nas --clear-sudo-password
ah edit nas --sudo=false
```

启用后，连接进入 root 的 `/bin/sh -l`；远程命令和 SFTP 操作也以 root 执行。sudo 请求密码时，优先使用保存的 sudo 密码，否则在终端隐藏输入。免密 sudo 不要求密码。旧版本保存的 sudo 密码仍能解密。SSH 密码与 sudo 密码分别保存，不再自动复用 SSH 密码；只保存 sudo 密码不会开启提权。

自动化或补全需要免密 sudo 或已保存的 sudo 密码；补全不会弹提示。登录账户须已有相应 sudo 权限，ah 不修改 sudoers。本地复制端仍使用本机当前用户；远端新文件由 root 创建。

提权复制需要独立 `sftp-server`，仅启用 `internal-sftp` 不够。常见安装路径会自动探测，也可设置：

```sh
ah edit nas --sftp-server /usr/lib/openssh/sftp-server
ah edit nas --sftp-server ''
```

要求强制 TTY 的 sudo 策略不适用于当前无 PTY 的复制/远程命令模式。sudo 认证和 SFTP 初始化支持超时、取消；失败不会降级成普通用户操作。

## Tab 补全

```sh
# Bash
source <(ah completion bash)
# Zsh
autoload -Uz compinit && compinit
source <(ah completion zsh)
# Fish
ah completion fish | source
```

```text
ah c n<Tab>
ah cp ./REA<Tab> nas:/home/alice/<Tab>
ah cp nas:~/rep<Tab> ./
```

本地与远端目录候选追加 `/`。Bash/Zsh/Fish 处理空格、中文和特殊字符的引用；唯一文件候选可闭合引号，目录候选可能继续保持引号开启以便输入下一级。

远端查询最长约 3 秒，不提示密码或主机信任。先建立正确主机信任；加密私钥可先由 agent 解锁。保存的 SSH/sudo 密码和代理规则会自动用于补全。

## 配置与凭据

默认目录来自 `os.UserConfigDir()`：macOS 通常为 `~/Library/Application Support/ah/`，Linux 通常为 `~/.config/ah/`（遵循 `XDG_CONFIG_HOME`）。全局选项可分别覆盖三个文件位置。

| 文件 | 内容 |
| --- | --- |
| `connections.toml` | 命名连接和加密密码 |
| `master.key` | 随机生成的密码加密主密钥 |
| `history.db` | SQLite 复制历史 |

```toml
[connections.nas]
host = "nas.example.com"
port = 22
user = "alice"
identity_file = "~/.ssh/id_ed25519"
proxies = ["socks5://127.0.0.1:1080", "socks5://proxy2.internal:1080"]
sudo = true
sftp_server = "/usr/lib/openssh/sftp-server"
# password / sudo_password 由命令生成 enc:v1: 密文，不在这里填写明文。
```

相对 `identity_file` 以执行目录解析，建议使用绝对路径或 `~/`。TOML 未知字段、无效配置和明文密码会报错；连接名参与密码认证绑定，手工改名后需要重新设置密码。

密码采用 AES-256-GCM，主密钥独立存放。配置、主密钥和历史文件权限为 0600，新建私有目录为 0700。备份和迁移需妥善保管配置及对应密钥；只备份 TOML 不能恢复密码，拿到两者的人可以解密。解密时找不到主密钥会报错，不会重新生成替代密钥。

## 常见问题与退出码

| 现象 | 处理 |
| --- | --- |
| `edit nas --password ...` 报参数数量错误 | 使用 `edit nas --password`，在提示后输入 |
| 本地文件被当成 `NAME:PATH` | 给含冒号的文件加 `./` 或使用绝对路径 |
| `unknown shorthand flag` | 检查子命令及当前二进制；远程参数应位于 `c NAME` 之后，必要时 `make build` |
| 未知/变化的主机密钥 | 核对目标与可信指纹，按授权建立或修复 known_hosts；不要绕过校验 |
| 补全无结果 | 检查连接名、目录、主机信任、代理、非交互 SSH/sudo 凭据 |
| 无法解密密码 | 检查 `--key-file`、文件权限、连接名和对应备份 |
| sudo 初始化失败 | 检查账户权限、sudo 密码及策略 |
| sudo SFTP 启动失败 | 检查独立 sftp-server 及 `--sftp-server` 路径 |
| 目标已存在 | 检查目标；需要替换时显式使用 `--force` |
| 服务器不支持复制扩展 | 使用支持对应 OpenSSH SFTP 扩展的服务 |

成功退出码为 0；一般错误为 1；远程命令返回 1–255 时保留其退出码；本地取消通常为 130。交互终端中的 Ctrl-C 发给远端，不等同于本地取消。以实际退出码和输出判断结果，远程错误文本属于服务器返回的数据。

## Agent 使用

仓库提供 [ah-cli skill](../skills/ah-cli/SKILL.md)，指导 agent 选择命令、处理交互认证、核对复制结果和重跑历史。仓库内 agent 可通过 AGENTS.md 技能索引发现它；其他环境可将整个 `skills/ah-cli/` 目录放入其支持的技能目录，或显式让 agent 读取 SKILL.md。仅保存到仓库不代表所有 agent 产品都会自动安装或加载。

从仓库根目录安装 skill 到 Codex（目标已存在时先比较内容）：

```sh
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
cp -R -i skills/ah-cli "${CODEX_HOME:-$HOME/.codex}/skills/"
```

新会话可显式请求“用 $ah-cli 把 ./README.md 复制到 nas:/home/，不要覆盖”。skill 不包含连接或凭据，仍需已安装的 ah 和用户的本地配置。

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

## 查看连接详情

```sh
ah inspect 108
ah --config ./connections.toml inspect nas
```

输出缩进 JSON，包含 name、config_file、host、port、user、identity_file、proxies、sudo、sudo_shell、sftp_server、term，以及 password_configured、sudo_password_configured。
`term` 为保存值，`effective_term` 按全局 `--term`、连接 term、本地 `$TERM`、`xterm-256color` 的顺序计算。
未设置的字符串保留为空，代理链为空时输出 `[]`；旧版单代理配置也统一展示为 proxies 数组。
每跳包含 address 和 authentication_configured，address 隐藏代理用户名及密码。命令不连接远端或解密凭据；密码密文、明文及私钥内容均不输出。
名称不存在、配置损坏或参数数量错误时返回非零退出码。连接名支持 Tab 补全。
