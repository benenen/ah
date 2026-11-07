# ah

Go 编写的 SSH 连接管理 CLI。使用 TOML 保存命名连接，支持 SSH 本地/远程端口转发、动态 SOCKS5 代理与后台管理、本地与远端文件互传、Bash/Zsh/Fish 路径 Tab 补全，并使用 SQLite 保存可查询、可重跑的复制历史。

完整参数与配置见 [CLI 文档](docs/cli.md)；agent 操作与安装见 [ah-cli skill](skills/ah-cli/SKILL.md) 和 [安装说明](docs/cli.md#agent-使用)。经 SSH 访问内网服务（数据库、Redis、Web 后台等）见 [internal-services-over-ssh](docs/internal-services-over-ssh.md)。

## 命令简写

| 完整命令 | 简写 |
| --- | --- |
| `connect` | `c` |
| `backup` | `b` |
| `copy` | `cp` |
| `edit` | `e` |
| `forward` | `f` |
| `history` | `h` |
| `list` | `ls` |
| `new` | `n` |
| `remove` | `rm` |
| `version` | `v` |

完整命令与简写使用相同参数，均在 `ah --help` 中标注。`ah h` 查询历史，`ah v` 输出版本，`ah -h` 显示帮助。

## 安装

需要 Go 1.26 或更新版本。交互终端支持 macOS、Linux。

```sh
make build
# 或安装到 $(go env GOPATH)/bin：
make install
```

也可以从 [Releases](https://github.com/benenen/ah/releases) 下载对应平台的压缩包（`ah_<版本>_<系统>_<架构>.tar.gz`，Windows 为 `.zip`），解压后把 `ah` 加入 PATH：

```sh
tar -xzf ah_0.1.0_linux_amd64.tar.gz
```

压缩包内含 `LICENSE`，`checksums.txt` 列出全部产物的 SHA-256。Windows 产物可完成 `list`、`cp`、`history`、`version` 等操作；`connect`（交互登录与远程命令）、密码输入、主机信任确认和后台转发会返回明确的不支持错误。`ah version` 输出版本、commit、构建时间与运行平台，报告问题时可以附上。

后续示例假设 `ah` 已加入 PATH。

## 端口转发

```sh
ah f A 8080 80 -d
# 本地 127.0.0.1:8080 → SSH 服务器 A 上的 127.0.0.1:80
ah f A 0.0.0.0:8080 80
# 显式监听本机所有 IPv4 网卡
ah f A 15432 database.internal:5432
ah f A '[::1]:8080' '[::1]:80'
ah f ls
ah f kill <ID>
ah f start <ID>
ah f restart <ID>
ah f rm <ID>
ah f rm -f <ID>
```

用法为 `ah forward NAME LOCAL TARGET`，可简写为 `ah f NAME LOCAL TARGET`，两个地址分别传参，不采用 ssh `-L 8080:host:80` 的合并格式。

- `NAME`：已保存的 SSH 连接名。
- 第一个端口 `LOCAL`：**本地监听端口**，例如 `8080` 或 `0.0.0.0:8080`。
- 第二个参数 `TARGET`：**SSH 服务器侧的目标服务端口**，例如 `80` 表示 SSH 服务器上的 `127.0.0.1:80`；也可指定由该服务器访问的 `HOST:PORT`。

例如 `ah f A 8080 80 -d` 表示「本地 8080 → SSH 服务器 A 上的 80」。
这里的 `80` 是目标服务端口，SSH 登录端口仍使用连接配置中的 `port`。

两端只写端口时均默认 `127.0.0.1`；显式地址使用 `HOST:PORT`，IPv6 地址需加方括号。
目标从 SSH 服务器访问和解析。端口范围为 1–65535，每次命令创建一个 TCP 转发，可同时服务多个连接。

新建转发时自动生成 ID。加 `-d` / `--daemon` 后脱离终端运行，SSH 和本地监听准备就绪后才返回 ID；
不加则在前台运行，Ctrl+C 关闭监听、活动连接及 SSH 连接。后台模式支持 Linux/macOS，使用已保存密码、可用私钥或 SSH agent，不能交互输入密码或私钥口令。
`ah f ls` 显示当前用户的所有转发记录，包括 ID、类型、连接名、监听/目标地址、PID、状态和错误；
`ah f kill ID` 通过私有控制通道停止对应转发，并等待关闭完成。前台转发也可按 ID 停止。
`ah f start ID` 按原 ID 在后台启动已停止或失败的转发；对运行中的转发报错。
`ah f restart ID` 等待旧转发停止后按原 ID 后台启动，已停止或失败时直接启动。
启动会恢复记录的配置/密钥/known_hosts 路径、工作目录和超时，使用当前连接配置；显式全局选项可覆盖路径和超时。
旧版记录未保存这些信息时使用当前默认值或显式选项。首次主机信任授权不会保存供重启复用。
`ah f rm ID` 删除已停止或失败的记录及日志；`ah f rm -f ID` 先停止运行中或启动中的转发再删除。
控制通道不可达时不会强删或按 PID 杀进程；无法确认停止时保留记录并报错。生命周期操作按 ID 加锁，删除后保留锁文件。
停止和失败记录保留；异常中断留下的 running 记录在控制通道不可达时显示 stale，不根据旧 PID 杀进程。
状态与后台日志位于用户配置目录的 `ah/forwards/<ID>.json` 和 `<ID>.log`，独立于 `--config` 指定的连接文件。
后台进程不自动重连，也不在系统重启后自动恢复。
复用连接配置中的 SSH 认证、SOCKS5 代理链及主机密钥校验，支持连接名 Tab 补全。
转发由 SSH 服务处理，不执行 shell 或 sudo；服务端须允许 TCP 转发。
监听失败、SSH 断开、目标连接失败或数据传输错误会结束命令并返回非零退出码。
`--timeout` 同时限制 SSH 建连及每次目标连接建立时间，不限制已建立连接的传输时长。

### 动态 SOCKS5 与远程转发

```sh
ah f A 1080 -D -d
# 本地 127.0.0.1:1080 起 SOCKS5 代理，经 A 访问任意目标
curl --socks5-hostname 127.0.0.1:1080 http://web.internal/
ah f A 9000 3000 -R -d
# SSH 服务器 A 上的 127.0.0.1:9000 → 本机 127.0.0.1:3000
```

转发分三种，由参数选择，三者互斥：

- `-L` / `--local`（默认，可省略）：`NAME LOCAL TARGET`，本地监听，目标由 SSH 服务器访问。
- `-D` / `--dynamic`：`NAME LOCAL`，本地起 SOCKS5 代理（无认证，仅 CONNECT，支持 IPv4/IPv6/域名）。每个请求由 SSH 服务器连接目标，域名也在服务器侧解析；浏览器需开启“通过 SOCKS 代理 DNS”，curl 用 `--socks5-hostname` 或 `socks5h://`，否则内网域名会在本机解析失败。单个目标连不上只向该请求回复失败并记入 stderr（后台时即日志），不结束代理；SSH 断开才结束。
- `-R` / `--remote`：`NAME REMOTE LOCAL_TARGET`，SSH 服务器监听 REMOTE，连接转到运行 ah 的机器能访问的 LOCAL_TARGET。与 `-L` 一样，目标连接失败会结束转发。服务器侧监听非回环地址需要 sshd `GatewayPorts`，否则 sshd 仍只监听回环；远端监听地址请写 IP。

`-D` 显式写 `0.0.0.0:1080` 会让局域网里任何人都能借这台 SSH 服务器上网，只在确有需要时这样做。
三种转发共用 ID、`-d`、`ls`/`kill`/`start`/`restart`/`rm` 生命周期；`ls` 的 TYPE 列显示 `-L`/`-D`/`-R`，`-D` 的 TARGET 显示 `(socks5)`，LISTEN 对 `-R` 是服务器侧地址。旧版记录按 `-L` 处理。

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
ah new D --host d.example.com --user deploy --tag prod,shop --note '订单服务'
ah edit D --add-tag k8s --remove-tag shop
ah ls --tag prod                 # 按标签筛选，多个 --tag 取交集
ah ls shop 10.0.0                # 名称/主机/用户/备注/标签包含全部关键词
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

交互会话按 OpenSSH 的做法申请 PTY：把本地终端设置（erase 等控制字符、IUTF8 等标志、波特率）随请求发给远端。新增连接默认保存 `term = "xterm-256color"`，可用 `new NAME --term screen-256color` 覆盖，或 `--term ''` 使用本地 `$TERM`。如果远端 terminfo 数据库里没有本地终端类型（例如 Ghostty 的 `xterm-ghostty` 遇上较旧的发行版），readline 会退化成哑终端行为，最典型的症状是退格只让光标移动、字符不消失。已有连接可用 `edit NAME --term xterm-256color` 固定一个远端认识的名字，或临时用全局选项 `ah --term xterm-256color c NAME` 覆盖；优先级为 `--term` > 连接配置 `term` > 本地 `$TERM`。另一种做法是把本地 terminfo 装到远端：`infocmp -x $TERM | ah c NAME 'tic -x -'`。

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
- 未知主机在交互终端先按可信渠道核对指纹，回答 yes 才接受并记录首次见到的密钥；回答 no，或没有交互终端的调用（含补全），一律拒绝。已有主机密钥发生变化时始终拒绝，不提示、不覆盖。
- `--trust-new-host` 跳过提示，直接接受并保存首次见到的密钥。该选项不使补全操作写入信任记录。

```sh
# 首次连接提示核对指纹，回答 yes 后写入 known_hosts
ah connect A
ah --trust-new-host connect A
ah --known-hosts ./test-known-hosts --timeout 15s connect A
```

`--timeout` 默认 10 秒，约束认证、连接与握手阶段；等待回答主机密钥提示的时间不计入其中，也不会给整次文件传输设置总时长上限。Ctrl-C 可取消复制或密码输入；交互 SSH 中 Ctrl-C 发送给远端终端。

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
ah cp -r ./site nas:/var/www/     # 复制目录
ah cp --backup ./app.conf nas:/etc/app/  # 覆盖前备份旧文件
```

没有 `NAME:` 前缀的路径表示本地文件，相对路径按执行目录解析；文件名含冒号时可使用 `./name:part` 避免被识别为连接别名。本地 `~` 表示本机用户目录。远端到远端时，两端分别建立 SSH/SFTP 连接，文件流经运行 ah 的机器，无需 A 能直接连接 B。目标为已有目录时使用源文件名；目标父目录须已存在。远端 `~` 表示该连接的 SFTP 登录目录；不支持 `~otheruser`。

默认拒绝覆盖，采用同目录临时文件与 `hardlink@openssh.com` 扩展原子发布，防止并发创建时覆盖目标。`--force` 使用 `posix-rename@openssh.com` 原子替换。目标服务端不支持对应扩展时明确报错。本地目标使用对应的本地硬链接或原子重命名。目标最终权限取源文件的普通 Unix 权限位；不复制属主、时间戳或 ACL。

复制失败不报告成功；能连通时清理临时文件。断线或取消导致无法清理时，错误会指出可能残留的 `.ah-copy-*` 路径。传输完成但发布响应丢失时，目标可能已经存在，应检查目标后再重试。

支持普通文件和目录（`-r`），并支持命名 SSH 跳板机和 `--resume` 断点续传。

### 目录复制

```sh
ah cp -r ./site nas:/var/www/          # 得到 /var/www/site
ah cp -r ./site nas:/var/www/site-new  # site-new 不存在时即为 site 的副本
ah cp -r -f ./site nas:/var/www/       # 已有 /var/www/site 时合并
```

`-r` / `--recursive` 按 `cp -r` 规则复制目录：目标为已有目录时复制到“目标/源目录名”，目标不存在时新建为源目录的副本（父目录须已存在）。最终落点已存在时，不加 `-f` 会在写入任何文件前报错；加 `-f` 则合并进去，同名文件逐个原子替换，目标中多余的文件保留不删。同名位置一边是文件、一边是目录时报错。不能把目录复制进它自己里面。
符号链接和设备、管道等特殊文件跳过不复制，逐个在 stderr 打印 `Skipped`。目录按源权限位设置（先以可写权限建立，文件复制完后再恢复）；合并时已有目录的权限不变。
每个文件单独原子发布，但整棵树不是原子的：中途失败时已复制的文件保留，确认后加 `-f` 重跑即可合并补齐。进度条按全部文件总字节显示。

### 覆盖前备份

```sh
ah cp --backup ./config.toml nas:/etc/app/          # 保留最近 5 份
ah cp --backup --backup-keep 10 ./config.toml nas:/etc/app/
ah cp -r --backup ./site nas:/var/www/              # 目录合并时逐个文件备份
```

`--backup` 在替换已有目标文件前，先把旧文件硬链接为同目录下的 `bak.原文件名.bak-UTC时间戳`（如 `bak.config.toml.bak-20260930T123456.123456789Z`），再原子替换；目标不存在时不产生备份。`--backup` 隐含覆盖，不需要再加 `-f`，用于目录时也隐含合并。
备份前先按时间删除该文件最旧的备份，使备份数不超过 `--backup-keep`（默认 5，范围 1–1000）；只清理名称完全符合 `bak.原文件名.bak-时间戳` 格式的文件。还原用 `ah backup`（简写 `ah b`），见下文。远端备份要求 `hardlink@openssh.com`。

### 查看与还原备份

```sh
ah backup ls nas:/etc/app/config.toml               # 列出备份，最新在前，带序号
ah backup restore nas:/etc/app/config.toml          # 还原到最新一份
ah backup restore nas:/etc/app/config.toml 3        # 按 ls 中的序号
ah backup restore nas:/etc/app/config.toml 20260930T1234   # 按时间戳前缀
ah b restore ./app.conf                             # 本地文件同样适用
```

TARGET 写原文件路径，不是备份文件本身。`ls` 显示序号（1 为最新）、备份时间（UTC）、大小和备份文件名。`restore` 的 VERSION 省略时取最新；可写序号、至少含 8 位日期（YYYYMMDD）的时间戳前缀或完整备份文件名，前缀匹配到多份时报错。
还原以原子替换写回，权限取自备份。还原前先把当前文件也备份一份（输出里会给出名字），所以还原本身可以再用 `ah b restore` 撤回；备份数同样受 `--backup-keep`（默认 5）限制，但正被还原的那份不会被清理。当前内容已与所选备份相同时不做改动。原文件被删掉时也能还原。sudo 连接下经提权的 SFTP 读写，可还原 root 文件。还原不写入复制历史。

替换已有文件时（`-f` 或 `--backup`），若目标与源大小、权限位和内容都完全相同，就跳过该文件：不替换、不备份，输出中计为 unchanged（单个文件打印 `Unchanged (identical content)`）。比较需要把目标完整读一遍，远端大文件会多花一次读取时间；只有权限位不同时仍会替换。

## 复制历史

```sh
ah history                     # 最近 20 条
ah history README nas          # 多个关键词同时匹配，不区分大小写
ah history search report --limit 50
ah history show 12             # 输出可复制的 shell 命令
ah history run 12              # 重跑，生成新的历史记录
```

历史默认保存在 `os.UserConfigDir()/ah/history.db`，可用全局 `--history-file /path/history.db` 覆盖。SQLite 文件权限为 0600，记录源/目标、执行目录、配置路径、开始/结束时间、字节数、覆盖/递归/备份选项、状态与错误；不保存密码或密钥内容。查询支持路径、状态和错误的关键词子串匹配。

每次参数数量正确的 `cp` 在传输前记为 `running`，结束后更新为 `success`、`failed` 或 `canceled`；进程被强制终止时可能保留 `running`。无法打开或写入历史时不会开始复制。本地目标不能覆盖当前历史数据库及其 SQLite 辅助文件。

重跑沿用原来的工作目录、覆盖/递归/备份选项和配置/主密钥/known_hosts 路径，连接定义使用该配置文件中的当前值；可通过全局选项显式覆盖配置路径。`history run` 直接调用复制逻辑，不执行数据库中的 shell 文本。原记录未使用 `--force` 时，重跑也会拒绝覆盖已有目标。首次主机信任授权不会保存在历史中。

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
make e2e                   # Docker 中真实 OpenSSH 端到端测试
make clean                 # 清理构建产物
```

也可以直接使用 `go build -o bin/ah ./cmd/ah`、`go install ./cmd/ah`、`go test ./...` 等 Go 命令。

测试启动本地 SSH/SFTP 服务，使用临时密钥和配置；Bash/Zsh/Fish 测试在隔离的伪终端中实际按 Tab 并复制含特殊字符的文件。缺少对应 shell 时跳过该 shell 测试。

`make e2e` 需要 Docker：用 `e2e/sshd` 构建一个真实 OpenSSH（Alpine）镜像，另起一台只在 Docker 内网可达的 Web 服务，再用编译出的 `ah` 按顺序跑完整场景——首次连接指纹确认（拒绝/接受/密钥变化）、远程命令的输出流与退出码、交互 shell、保存密码登录、单文件与目录复制（含权限、中文路径、符号链接跳过、合并、`--backup` 保留份数、`backup ls/restore` 还原与撤回、内容相同跳过、远端互传）、历史查询与重跑、`-L`/`-D`/`-R` 转发（含 `GatewayPorts`、日志、kill/start/restart、`rm --all`）、免密与带密码的 sudo 及 root 复制。交互提示用伪终端驱动。每次运行使用独立的临时 HOME、配置、known_hosts、主密钥和历史，结束时删除容器与网络，只保留 `ah-e2e-sshd` 镜像作构建缓存。镜像里的密码和运行时注入的密钥都是一次性的测试数据。未安装 Docker 或守护进程不可用时测试跳过；普通 `go test ./...` 不会运行它（`e2e` 构建标签）。

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

### 查看单个连接详情

`ah inspect 108` 以 JSON 显示连接地址、认证配置状态、代理链、sudo 和 TERM 等信息。密码及代理认证信息隐藏，私钥仅显示路径；命令只读取本地配置，不连接服务器。

### 复制、重命名连接与结构化查询

```sh
ah clone dev staging       # 保留 dev，复制为 staging
ah rename staging prod     # 将 staging 改名为 prod
ah list --json
ah history --json --status failed --connection prod --limit 50
ah history --after 2026-01-01T00:00:00Z --before 2026-02-01T00:00:00Z
```

clone/rename 拒绝覆盖已有别名，SSH 和 sudo 密码会按新别名重新加密；主密钥缺失或解密失败时不修改配置。已有历史和转发记录保留原连接名，不随改名更新。

list 的 JSON 只包含 name、host、port、user、term、tags、note，`--tag` 和关键词筛选同样适用；history 的 JSON 为记录数组，空结果为 `[]`。历史筛选先于 limit 生效，连接名精确匹配源或目标的别名，时间按开始时间筛选，after 包含边界，before 不包含边界，均使用 RFC3339。

### 复制预览与排除

```sh
ah cp -r --backup --dry-run ./site nas:/var/www/
ah cp -r --exclude .git --exclude '*.log' --exclude 'build/' ./site nas:/var/www/
```

`--dry-run` 查询文件并比较内容，显示最终落点与 mkdir、merge、add、replace、unchanged、exclude、skip-special、backup、prune-backup 操作，不修改复制目标、不生成备份、不写复制历史。预览仍需正常 SSH 认证，首次主机信任按既有规则处理。它反映查询时的状态，正式复制时会重新检查；不能保证之后的写权限或空间足够。

`--exclude` 可重复，仅用于 `-r`。模式使用 `/` 分隔，按 Go path.Match 匹配；不含斜杠的模式匹配任意层级的文件名，含斜杠的模式相对源目录匹配，尾部 `/` 只匹配目录。命中目录时不再遍历其子项。`*` 不跨越斜杠，`**` 不表示递归通配；不支持绝对路径、`..` 路径段或反斜杠。排除项不计入进度和复制字节，规则会保存到历史并用于重跑。

### 连接诊断与保活

```sh
ah doctor nas
ah --keepalive 30s forward nas 15432 5432 --continue-on-error -d
```

`doctor` 按阶段报告配置、认证资料、TCP 或代理链、SSH 密钥交换、主机密钥校验、认证、sudo、SFTP 与复制所需扩展的检查结果，失败返回非零。sudo 未开启时跳过。它正常校验主机身份，首次连接仍需信任确认；SFTP 初始化也受超时限制。不创建测试文件，因此不验证具体目标目录的写权限或磁盘空间。

`--keepalive DURATION` 定期发送 SSH 保活请求，默认 0 关闭；等待回复超过 `--timeout` 时关闭失去响应的 SSH 连接。服务端返回“不支持此请求”也算有效回复。`forward --continue-on-error` 让 `-L/-R` 的单次目标连接或传输失败只断开该客户端，并将错误写入 stderr（后台运行时写日志），随后继续监听。默认保留遇错结束的行为。SSH 通道打开超时仍需关闭整个 SSH 连接才能解除阻塞。保活与容错选项保存在转发记录中，start/restart 沿用，可显式覆盖。

### 转发自动重连

```sh
ah --keepalive 20s forward nas 15432 5432 --continue-on-error --reconnect -d
```

`--reconnect` 在首次启动成功后，转发失败时按 1、2、4 秒递增等待，最多间隔 30 秒重试，直到恢复或被停止。首次启动失败仍立即返回错误。原 ID 和监听地址保持不变，重试使用当前连接配置，不弹认证或信任提示，每次都校验主机密钥。`ls` 保留 running 状态，ERROR 显示 reconnecting 及最近一次断线原因；后台细节见日志。重连期间监听可能不可用，旧 TCP 连接会断开，应用需重新连接。kill、restart 和 rm -f 仍可取消重试。start/restart 继承该选项，可用 `--reconnect=false` 关闭。

### SSH 跳板机

```sh
ah new bastion --host bastion.example.com --user alice
ah new private --host 10.0.0.9 --user bob --jump bastion
ah c private
ah cp ./config.toml private:~/config.toml
ah edit private --jump ''       # 恢复直连
```

`jump` 引用另一个命名连接，通过其 SSH direct-tcpip 通道访问当前目标。跳板连接还可以引用下一层跳板，最多 32 个连接；循环引用或缺失别名在联网前报错。每跳独立使用自己的用户、密钥、密码和主机密钥校验，连接超时逐跳生效。跳板的 sudo 不参与 TCP 通道转发，最终目标仍按自身 sudo 设置执行。当前连接不能同时设置 jump 和 SOCKS5 代理链，可在跳板连接上设置代理。connect、cp、补全、doctor、forward 和历史重跑共用该路径，失败不回退直连；关闭最终连接会关闭所拥有的跳板连接。rename 会同时更新配置内引用旧名称的 jump，历史和转发记录仍保留原名称。

### 断点续传

```sh
ah cp --resume ./large.tar nas:~/large.tar
ah cp --resume nas:~/large.tar ./large.tar
ah cp -r -f --resume ./dataset nas:~/dataset
```

`--resume` 失败时保留同目录 `.ah-resume-*` 普通文件，并在错误中给出路径。重跑同一源和目标时，先计算源文件 SHA-256，再寻找内容指纹、源路径、目标路径和权限匹配的片段；按长度选取，逐块核对前缀后继续剩余内容。损坏片段明确报错，源内容变化时不复用旧片段，传输期间源内容或时间/大小发生变化时拒绝发布。只有校验完成才按原有 no-clobber、force 或 backup 规则原子发布。目录复制逐文件续传，重跑已部分完成的目录需 `-f` 或 `--backup` 合并。历史 show/run 保留 resume 选项。

每次重试使用独立临时文件，旧片段只读，避免并发重试修改已发布文件。已校验前缀会复制到新临时文件；为了完整性校验，源文件会完整预读一次，远端片段也需要读取。因此续传会增加读取与临时空间成本，不保证节省网络流量。成功时清理本次临时文件，历史失败片段保留以免影响其他重试，确认不再需要后可手工删除错误里给出的片段。`--dry-run --resume` 只读校验片段，并以 resume-prefix 操作显示复用来源。

### 目录差异与同步

```sh
ah diff ./dist web:/srv/app
ah sync ./dist web:/srv/app                     # 新增、更新，保留目标多余文件
ah sync --delete --dry-run ./dist web:/srv/app   # 预览更新和删除
ah sync --delete --exclude '*.log' --exclude 'uploads/' ./dist web:/srv/app
```

两端必须是目录，支持本地与远端的四种方向。源目录内容直接对应目标目录内容，不追加源目录名；目标目录不存在时创建它，但父目录必须存在。`diff` 比较文件内容与权限，列出 `add`、`mkdir`、`modify`、`target-only`、`conflict`，相同文件不输出。目录权限沿用复制规则：新建目录使用源权限，已有目录保留原权限。

`sync` 默认更新有差异的普通文件，保留目标多余项目；只有显式 `--delete` 才在全部新增、更新成功后删除目标多余普通文件与空目录。排除规则与 `cp -r --exclude` 相同，并保护两端命中的项目及其子项。符号链接和特殊文件跳过并保留，其父目录也不会被删。文件与目录类型冲突在写入前报错，不自动替换类型。

`diff` 和 `sync --dry-run` 不写目标或历史；同步预览用 `delete` / `retain` 表示目标多余项的处理。预览不能保证后续写权限、空间或服务器发布扩展可用。正式同步逐文件原子替换，目录整体不原子；失败保留已完成更新，不继续删除。删除前再次检查源清单及待删目标的类型和文件元数据；不保证并发修改中的目录具有一致快照。根路径为符号链接时拒绝操作，同一本地文件系统或同一连接名下拒绝源目标目录重叠。

同步写入 SQLite 历史，`history show/run` 保留同步模式、排除规则及 `--delete`；重跑带删除选项的历史也会执行删除。旧历史仍按原来的 `cp` 方式重跑。

### 配置导出、备份与迁移

```sh
ah config export ./connections.toml                  # 不含凭据的 TOML
ah config import ./connections.toml --dry-run         # 预览新增和同名冲突
ah config import ./connections.toml                   # 合并，同名时报错
ah config import ./connections.toml --force           # 明确替换同名连接
ah config export ./backup.ah --with-secrets           # 输入并确认备份口令
ah config import ./backup.ah                         # 自动识别加密包并输入口令
```

导出包含全部连接的地址、标签、备注、代理与跳板配置。默认移除保存的 SSH/sudo 密码和代理 URL 的认证信息。`--with-secrets` 将配置及上述凭据一起加密，使用 scrypt 派生密钥及 AES-256-GCM；原主密钥不进入备份。迁移导入时，用目标机器现有主密钥重新加密保存的 SSH/sudo 密码，没有主密钥时自动创建。

导出文件权限为 0600，默认拒绝覆盖已有文件，`config export --force` 可替换旧备份；不能把备份写到当前配置、主密钥、known_hosts、历史数据库及其辅助文件或口令文件上。目标父目录须存在。私钥只保存路径，不打包文件；迁移后可能需要调整路径并重新确认主机指纹。复制历史和转发记录不导出。

导入在配置锁内校验并原子合并，同名连接默认整批报错；`--force` 替换该连接的完整定义，其他连接保留。使用不含凭据的文件覆盖时，原连接保存的密码也会清除。无效 TOML、口令错误、包损坏、跳板缺失或循环都不会改写配置。`--dry-run` 只输出 `add` / `replace`，不写配置或创建主密钥；同名预览也需显式 `--force`。导入大小上限为 16 MiB。

自动化可用 `--passphrase-file /path/to/private-file` 代替交互输入，两端使用同一口令。该文件须是普通文件，Unix 下不得有组或其他用户权限；读取时去掉末尾一组换行（LF 或 CRLF）。口令不接受命令行明文参数。普通 TOML 导入拒绝直接携带 `enc:v1:` 密码，因为它们绑定源机器主密钥；需要迁移密码时使用加密导出。

### 按标签批量执行命令

```sh
ah exec --tag prod --dry-run -- 'df -h'       # 先列出匹配连接，不联网
ah exec --tag prod --jobs 4 -- 'df -h'
ah exec --tag prod --tag web --jobs 2 --exec-timeout 30s -- 'systemctl status nginx'
```

必须指定至少一个标签，多个标签按交集筛选，忽略大小写，也可写成 `--tag prod,web`。没有匹配连接时报错，不会自动扩大到全部机器。`--` 是必需的分隔符，其后参数按 SSH 方式用空格连接并交给远程 shell；管道、重定向和引号需要像 `ah c` 一样整体引用。

`--jobs` 默认 4，范围 1–64。单台连接或命令失败后继续其他目标，不自动重试；`--exec-timeout` 默认 0 不限时，非零时限制每台从连接到命令完成的总时长，全局 `--timeout` 仍限制连接/握手。Ctrl+C 关闭活动 SSH 连接并停止启动后续机器，不保证远程操作回滚或远程子进程全部终止。

stdout、stderr 各自实时输出，每行带 `[连接名]` 前缀；无结尾换行的尾部内容在结束时补换行，超过 32 KiB 的单行分段输出，以限制内存。stderr 最后按连接名排序输出 NAME/STATUS/EXIT/ERROR 和成功、失败、取消、未启动数量。全部成功退出 0，存在失败或单台超时退出 1，整体取消退出 130；各台远程退出码在表中保留，未取得退出码时显示 `-`。

批量执行不申请 PTY、不读取本地 stdin，也不弹 SSH/sudo 密码、私钥口令或首次信任提示。可使用保存的密码、SSH agent、无需口令的私钥及保存的 sudo 密码/免密 sudo；首次使用前可逐台 `ah c NAME` 确认指纹。显式全局 `--trust-new-host` 可接受并保存首次密钥，已有密钥变化仍拒绝。复用代理、跳板和 sudo 配置，不记录复制历史。

### 交互选择连接

```sh
ah pick                     # 打开连接选择器
ah pick prod                # 预填搜索关键词
ah pick --tag prod --tag web # 仅在同时带这些标签的连接中选择
```

直接输入文字会实时筛选连接名、主机地址（含端口）、用户名、标签和备注。多个空格分隔的关键词取交集，忽略大小写。↑/↓、Tab 或 Ctrl+N/Ctrl+P 切换选中项，Enter 连接；Backspace 删除一个 Unicode 字符，Ctrl+U 清空搜索。无匹配项时可继续编辑，Enter 不执行连接。即使只有一项，也需 Enter 确认。

Esc 或 Ctrl+D 正常退出，Ctrl+C 取消并返回 130。选择阶段只读本地配置，不联网；确认后先退出选择界面、恢复终端，再进入与 `ah c NAME` 相同的 SSH 会话，沿用密码、sudo、代理、跳板、TERM、保活和主机密钥校验。

选择器仅支持 macOS/Linux，需要 stdin 和 stderr 都是交互终端；界面写到 stderr，使用临时独立屏幕，退出后恢复原屏幕。窗口过小时提示调整大小，暂停 Enter 选择。列表显示名称、用户、地址、标签和备注，长内容按窗口宽度截断，不显示密码、代理凭据或私钥内容。
