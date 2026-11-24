# ah 命令行参考

[文档首页](README.md) · [安装](installation.md) · [开发与验证](development.md)

本文对应本仓库当前实现。示例地址和连接名为虚构值，请替换为自己的配置。

## 目录

- [准备与命令总览](#准备与命令总览)
- [命令简写](#命令简写)
- [全局选项](#全局选项)
- [连接管理](#连接管理)
- [查看连接详情](#查看连接详情)
- [复制、重命名连接与结构化查询](#复制重命名连接与结构化查询)
- [交互选择连接](#交互选择连接)
- [交互登录与远程命令](#交互登录与远程命令)
- [按标签批量执行](#按标签批量执行)
- [SSH 跳板机](#ssh-跳板机)
- [SOCKS5 代理链](#socks5-代理链)
- [ProxyCommand](#proxycommand)
- [sudo 默认提权](#sudo-默认提权)
- [连接诊断与保活](#连接诊断与保活)
- [端口转发](#端口转发)
- [文件复制](#文件复制)
- [目录差异与同步](#目录差异与同步)
- [历史查询与重跑](#历史查询与重跑)
- [Tab 补全](#tab-补全)
- [配置与凭据](#配置与凭据)
- [配置导出与迁移](#配置导出与迁移)
- [常见问题与退出码](#常见问题与退出码)
- [Agent 使用](#agent-使用)

## 准备与命令总览

安装方式、PATH 设置和平台限制见 [安装文档](installation.md)。安装后可用 `ah --help` 或 `ah <command> --help` 查看当前版本支持的参数；仓库内也可使用 `./bin/ah`。

| 命令 | 用途 |
| --- | --- |
| `ah list` | 列出连接名、主机、端口、登录用户、标签和备注；支持 `--tag` 与关键词筛选 |
| `ah config export FILE` / `ah config import FILE` | 导出、备份和迁移整份连接配置 |
| `ah pick [QUERY...] [--tag TAG...]` | 搜索连接并按 Enter 进入 SSH |
| `ah inspect NAME` | 以 JSON 输出单个连接配置；凭据只显示是否已配置 |
| `ah new NAME --host HOST --user USER` | 创建连接；不会主动连接服务器 |
| `ah edit NAME [flags]` | 只更新明确传入的字段 |
| `ah rm NAME` | 删除保存的连接记录，不删除远端文件 |
| `ah connect NAME [COMMAND [ARG...]]` | 交互登录或执行远程命令 |
| `ah exec --tag TAG -- COMMAND [ARG...]` | 按标签并发执行，输出主机前缀及退出码汇总 |
| `ah c NAME [COMMAND [ARG...]]` | `connect` 的简写 |
| `ah forward NAME LOCAL TARGET` | 通过 SSH 转发 TCP 端口；`-D` 起 SOCKS5 代理，`-R` 远程转发 |
| `ah cp SOURCE DESTINATION` | 单个普通文件的本地/远端复制 |
| `ah diff SOURCE DESTINATION` | 查看目录新增、修改及目标独有项目 |
| `ah sync SOURCE DESTINATION` | 单向同步目录内容，`--delete` 显式删除目标多余项目 |
| `ah doctor NAME` | 分阶段诊断连接、认证、sudo、SFTP 与发布扩展 |
| `ah clone OLD NEW` / `ah rename OLD NEW` | 复制或重命名连接 |
| `ah backup ls/restore TARGET` | 查看或还原复制备份 |
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

## 全局选项

| 选项 | 默认值/行为 |
| --- | --- |
| `--config PATH` | `os.UserConfigDir()/ah/connections.toml` |
| `--key-file PATH` | `os.UserConfigDir()/ah/master.key`；用于密码加解密 |
| `--history-file PATH` | `os.UserConfigDir()/ah/history.db` |
| `--known-hosts PATH` | `~/.ssh/known_hosts` |
| `--term TERM` | 覆盖本次交互会话发送的 TERM，优先于连接配置 |
| `--shell SHELL` | 覆盖本次交互会话启动的 shell，优先于连接配置；`default` 使用服务器的登录 shell |
| `--keepalive DURATION` | 默认 0 关闭；定期发送 SSH 保活请求，回复等待受 --timeout 限制 |
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
| `--jump ALIAS` | 经命名 SSH 跳板连接；空字符串清除，与当前连接的代理链互斥 |
| `--term TERM` | 保存交互终端类型；new 默认 xterm-256color，空字符串使用本地 TERM |
| `--shell SHELL` | 保存交互 shell，如 `pwsh`、`zsh`；空字符串按平台默认，`default` 使用服务器的登录 shell |
| `--sudo-shell SHELL` | sudo 交互登录 shell，默认 bash，不可用时回退 /bin/sh |
| `--proxy ADDRESS` | 保存 SOCKS5 代理；重复传入表示依次多跳 |
| `--proxy-command CMD` | 像 OpenSSH ProxyCommand 一样经本地命令的 stdin/stdout 连接；空字符串清除 |
| `--host-key-alias NAME` | known_hosts 中用于校验的名字；配置了 `--proxy-command` 时默认取连接名，空字符串清除 |
| `--sudo` / `--sudo=false` | 启用/关闭该连接的默认 root 提权 |
| `--sudo-password` | 隐藏输入独立的 sudo 密码并加密保存；本身不启用 sudo |
| `--sftp-server PATH` | sudo 模式使用的独立 SFTP 服务绝对路径；空字符串恢复自动探测 |
| `--tag TAG` | 标签，可重复或逗号分隔；`edit` 时整体替换 |
| `--note TEXT` | 单行备注，最多 500 字符；空字符串清除 |

`edit` 还支持 `--no-sudo`（等价 `--sudo=false`）、`--clear-password`、`--clear-sudo-password`、`--clear-proxy`、`--clear-term`、`--clear-sudo-shell`，以及 `--add-tag`、`--remove-tag`、`--clear-tags`（与 `--tag` 互斥）。同一项的设置和清除选项互斥。修改其他字段会保留原密码、代理和 sudo 设置。

### 标签、备注与筛选

```sh
ah new web1 --host web1.example.com --user deploy --tag prod,shop --note '订单服务主节点'
ah edit web1 --add-tag k8s --remove-tag shop
ah ls --tag prod              # 带 prod 标签的连接
ah ls --tag prod --tag shop   # 同时带两个标签
ah ls web 10.0.0              # 在名称/主机/用户/备注/标签中同时匹配两个关键词
ah ls --tag prod --json
```

标签以字母或数字开头，可含字母（含中文）、数字和 `_ . : / -`，最长 64 字节；匹配和去重都忽略大小写。删除连接上不存在的标签会报错。`ls` 表格增加 TAGS、NOTE 列，`--tag` 后按 Tab 补全已有标签。

**密码选项是开关，不接收明文参数。**

```sh
ah edit nas --password          # 出现 SSH password: 后输入并回车
ah edit nas --sudo-password     # 出现 sudo password: 后输入并回车
ah edit nas --clear-password
```

保存的 SSH 密码优先于密钥认证。未保存时支持显式私钥、agent、默认私钥及交互密码认证。临时登录时输入的密码不会自动保存。

## 查看连接详情

```sh
ah inspect 108
ah --config ./connections.toml inspect nas
```

输出缩进 JSON，包含 name、config_file、host、port、user、identity_file、proxies、sudo、sudo_shell、sftp_server、term，以及 tags、note、password_configured、sudo_password_configured。
`term` 为保存值，`effective_term` 按全局 `--term`、连接 term、本地 `$TERM`、`xterm-256color` 的顺序计算。`shell` 为全局 `--shell` 或连接保存值，空表示按平台默认。
未设置的字符串保留为空，代理链为空时输出 `[]`；旧版单代理配置也统一展示为 proxies 数组。
每跳包含 address 和 authentication_configured，address 隐藏代理用户名及密码。命令不连接远端或解密凭据；密码密文、明文及私钥内容均不输出。
名称不存在、配置损坏或参数数量错误时返回非零退出码。连接名支持 Tab 补全。

## 复制、重命名连接与结构化查询

```sh
ah clone dev staging       # 保留 dev，复制为 staging
ah rename staging prod     # 将 staging 改名为 prod
ah list --json
ah history --json --status failed --connection prod --limit 50
ah history --after 2026-01-01T00:00:00Z --before 2026-02-01T00:00:00Z
```

clone/rename 拒绝覆盖已有别名，SSH 和 sudo 密码会按新别名重新加密；主密钥缺失或解密失败时不修改配置。已有历史和转发记录保留原连接名，不随改名更新。

list 的 JSON 只包含 name、host、port、user、term、tags、note，`--tag` 和关键词筛选同样适用；history 的 JSON 为记录数组，空结果为 `[]`。历史筛选先于 limit 生效，连接名精确匹配源或目标的别名，时间按开始时间筛选，after 包含边界，before 不包含边界，均使用 RFC3339。

## 交互选择连接

```sh
ah pick
ah pick prod
ah pick --tag prod,web
```

直接输入关键词搜索名称、主机/端口、用户名、标签和备注，忽略大小写，空格分隔的多个关键词取交集；`--tag` 限定候选范围，也可重复传入。

| 按键 | 操作 |
| --- | --- |
| ↑/↓、Tab、Ctrl+N/P | 切换选中连接 |
| Enter | 确认并连接；无匹配时继续等待 |
| Backspace | 删除一个 Unicode 字符 |
| Ctrl+U | 清空搜索 |
| Esc、Ctrl+D | 正常退出，不连接 |
| Ctrl+C | 取消，返回 130 |

仅支持 macOS/Linux，stdin、stderr 必须是交互终端。界面使用 stderr 的临时独立屏幕，长内容截断，窗口过小时提示调整并暂停 Enter；退出或取消后恢复终端。列表仅显示名称、用户、地址、标签和备注，不显示凭据。选择阶段不联网或修改配置；确认后恢复终端并执行与 `ah c NAME` 相同的 SSH 连接，包括既有认证、首次主机信任提示和 sudo 行为。

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

### 终端兼容

交互会话按 OpenSSH 的做法申请 PTY：把本地终端设置（erase 等控制字符、IUTF8 等标志、波特率）随请求发给远端。新增连接默认保存 `term = "xterm-256color"`，可用 `new NAME --term screen-256color` 覆盖，或 `--term ''` 使用本地 `$TERM`。如果远端 terminfo 数据库里没有本地终端类型（例如 Ghostty 的 `xterm-ghostty` 遇上较旧的发行版），readline 会退化成哑终端行为，最典型的症状是退格只让光标移动、字符不消失。已有连接可用 `edit NAME --term xterm-256color` 固定一个远端认识的名字，或临时用全局选项 `ah --term xterm-256color c NAME` 覆盖；优先级为 `--term` > 连接配置 `term` > 本地 `$TERM`。另一种做法是把本地 terminfo 装到远端：`infocmp -x $TERM | ah c NAME 'tic -x -'`。

交互会话启动的 shell 按 `--shell` > 连接配置 `shell` > 平台默认的顺序决定。平台默认为：Windows 用 `cmd.exe`，macOS 用 `zsh`，其他系统用 `bash`。

```sh
ah edit winbox --shell pwsh        # 这台 Windows 默认进 PowerShell 7
ah --shell powershell c winbox     # 临时改用 Windows PowerShell
ah edit nas --shell default        # 交给服务器决定，等同于旧行为
```

Windows 主机按 SSH 版本号里的 `OpenSSH_for_Windows` 识别，选中的 shell 交给服务器的 DefaultShell 去启动；其他主机在远端用 `uname -s` 区分 macOS 和其他系统，再以 `-l` 登录方式启动。指定的 shell 在远端不存在时，会提示一行并退回用户自己的登录 shell，不会直接断开。除 `default` 外，ah 都通过 exec 请求启动 shell，所以 sshd 不再打印 motd 和 “Last login”，需要的话设 `--shell default`。sudo 连接不受这个设置影响。

带命令执行（`ah c NAME CMD`、`ah exec`）一般仍由服务器的默认 shell 解释，例外是 Windows 连接的 shell 设成了 `pwsh` 或 `powershell`（也可以写完整路径）：这时命令交给 PowerShell 执行，`ah c winbox ls`、`ah c winbox 'Get-Process | Select -First 3'` 都能直接用。ah 把命令放进 base64 后由 `-Command` 解码执行，所以 cmd 不会解释其中的 `&`、`|`、`%VAR%` 和引号。输出统一为 UTF-8、不带颜色，最后一条命令失败时返回它的原生退出码，原生命令可以读 stdin（PowerShell 的 `$input` 读不到）。没有用 `-EncodedCommand`，是因为不少安全软件会拦截它。经过 cmd 之后命令行总长不能超过约 8 KB，所以命令文本大约要控制在 5 KB 以内。`shell` 只接受名字或路径（字母、数字和 `. _ + - / \ :`），不能带参数。

## 按标签批量执行

```sh
ah exec --tag prod --dry-run -- 'df -h'
ah exec --tag prod --jobs 4 -- 'df -h'
ah exec --tag prod,web --jobs 2 --exec-timeout 30s -- 'systemctl status nginx'
```

至少一个标签和 `--` 分隔符是必需的。多个标签取交集，忽略大小写；无匹配时报错。`--dry-run` 只列出选中的连接名，不联网。`--` 后参数按 SSH 规则用空格拼接，远程管道、重定向和需要保留的引号请整体引用。

并发默认 4 台，`--jobs` 范围 1–64。单台失败继续其他目标，不自动重试。`--exec-timeout` 限制每台从连接到命令完成的总时间，默认 0 不限时，负数拒绝；全局 `--timeout` 仍为连接/握手超时。

stdout/stderr 分别实时输出，每行带 `[连接名]`；结尾没有换行时补换行，单行超 32 KiB 则分段补换行。stderr 末尾按名称列出状态、远程退出码和错误，并汇总 success/failed/canceled/not-run。未取得远程退出码显示 `-`。全部成功退出 0，任何单台失败/超时退出 1，整体取消退出 130。

不申请 PTY、不读取本地 stdin、不弹认证或主机信任提示。使用保存的 SSH/sudo 密码、agent、无需交互的私钥或免密 sudo；先用普通连接完成主机信任。显式全局 `--trust-new-host` 可接受首次密钥，变化密钥仍拒绝。代理、跳板、sudo 和保活沿用连接配置；Ctrl+C 关闭活动连接，后续排队机器不再启动，不保证远程操作回滚。输出写入失败也会取消整批；不写复制历史。

## SSH 跳板机

```sh
ah new bastion --host bastion.example.com --user alice
ah new private --host 10.0.0.9 --user bob --jump bastion
ah c private
ah cp ./config.toml private:~/config.toml
ah edit private --jump ''       # 恢复直连
```

`jump` 引用另一个命名连接，通过其 SSH direct-tcpip 通道访问当前目标。跳板连接还可以引用下一层跳板，最多 32 个连接；循环引用或缺失别名在联网前报错。每跳独立使用自己的用户、密钥、密码和主机密钥校验，连接超时逐跳生效。跳板的 sudo 不参与 TCP 通道转发，最终目标仍按自身 sudo 设置执行。当前连接不能同时设置 jump 和 SOCKS5 代理链，可在跳板连接上设置代理。connect、cp、补全、doctor、forward 和历史重跑共用该路径，失败不回退直连；关闭最终连接会关闭所拥有的跳板连接。rename 会同时更新配置内引用旧名称的 jump，历史和转发记录仍保留原名称。

## SOCKS5 代理链

```sh
ah edit nas --proxy socks5://127.0.0.1:1080
ah edit nas --proxy socks5://127.0.0.1:1080 --proxy socks5://proxy2.internal:1080
ah edit nas --clear-proxy
```

第二条命令保存“本机 → 代理 1 → 代理 2 → nas”的整条链；`edit --proxy` 替换旧链，不是追加。`new` 同样支持重复选项。

地址支持 `HOST:PORT` 或 `socks5://HOST:PORT`，IPv6 使用 `[::1]:1080`。也接受 `socks5h://HOST:PORT`。保留代理 URL 认证支持，但 URL 中的用户名/密码原样存入 TOML，不受 SSH/sudo 密码加密保护；示例使用无认证代理，避免把代理凭据写入命令记录或共享文件。SSH 跳板使用独立的 `--jump` 选项，见 [SSH 跳板机](#ssh-跳板机)。

只有第一跳在本机解析；后续代理和目标域名交给上一跳解析。链路失败不回退直连。规则随连接保存，供登录、命令、复制、历史重跑和远程补全共用。旧 `proxy = "socks5://..."` 单代理配置仍可读取，设置新的 `--proxy` 后改存 `proxies` 数组；两种字段不能同时设置，`--clear-proxy` 清除两者。

## ProxyCommand

对应 `~/.ssh/config` 里的 `ProxyCommand`，例如：

```sh
ah new shiben --host 127.0.0.1 --port 22 --user shiben \
  --proxy-command 'uucli proxy aeawswhnguarjyo6 %h %p'
ah c shiben
```

ah 用 `/bin/sh -c`（Windows 为 `cmd.exe /C`）启动命令，SSH 流量走它的 stdin/stdout。`%h`、`%p`、`%r` 替换为 host、port、user，其中 host 和 user 会做 shell 引用；`%%` 是字面 `%`，其他 `%` 写法在保存时就会报错。命令的 stderr 只保留在内存里，连接失败时附在错误信息后面。超时、取消或断开连接时会杀掉整个进程组。

这类命令连到的 host 往往都是 `127.0.0.1`，没法区分不同的服务器，所以 known_hosts 默认按连接名校验（和 OpenSSH 的 `Host` 别名一样，不带端口），用 `ssh shiben` 存下的记录可以直接用。`--host-key-alias` 可以指定别的名字。`clone`/`rename` 会把原连接名固定为新连接的 `host_key_alias`，所以已有的 known_hosts 记录照样能匹配。

`proxy_command` 与 `--jump`、`--proxy` 互斥，但跳板连接本身可以配置它。`config import` 遇到带 `proxy_command` 的连接时，`--dry-run` 会列出 `proxy-command NAME CMD`；真正导入必须加 `--allow-proxy-command`，免得别人给的配置文件在本机执行命令。

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

启用后，交互连接进入 root 的登录 shell，默认 bash，不可用时回退 `/bin/sh`，可用 `--sudo-shell` 指定；远程命令和 SFTP 操作也以 root 执行。sudo 请求密码时，优先使用保存的 sudo 密码，否则在终端隐藏输入。免密 sudo 不要求密码。旧版本保存的 sudo 密码仍能解密。SSH 密码与 sudo 密码分别保存，不再自动复用 SSH 密码；只保存 sudo 密码不会开启提权。

自动化或补全需要免密 sudo 或已保存的 sudo 密码；补全不会弹提示。登录账户须已有相应 sudo 权限，ah 不修改 sudoers。本地复制端仍使用本机当前用户；远端新文件由 root 创建。

提权复制需要独立 `sftp-server`，仅启用 `internal-sftp` 不够。常见安装路径会自动探测，也可设置：

```sh
ah edit nas --sftp-server /usr/lib/openssh/sftp-server
ah edit nas --sftp-server ''
```

要求强制 TTY 的 sudo 策略不适用于当前无 PTY 的复制/远程命令模式。sudo 认证和 SFTP 初始化支持超时、取消；失败不会降级成普通用户操作。

## 连接诊断与保活

```sh
ah doctor nas
ah --keepalive 30s forward nas 15432 5432 --continue-on-error -d
```

`doctor` 按阶段报告配置、认证资料、TCP 或代理链、SSH 密钥交换、主机密钥校验、认证、sudo、SFTP 与复制所需扩展的检查结果，失败返回非零。sudo 未开启时跳过。它正常校验主机身份，首次连接仍需信任确认；SFTP 初始化也受超时限制。不创建测试文件，因此不验证具体目标目录的写权限或磁盘空间。

`--keepalive DURATION` 定期发送 SSH 保活请求，全局默认 0 关闭（可在 `new/edit --keepalive` 写入连接配置作为默认值，`edit --clear-keepalive` 清除，全局 `--keepalive` 可覆盖）；等待回复超过 `--timeout` 时关闭失去响应的 SSH 连接。服务端返回“不支持此请求”也算有效回复。`forward` 在未显式指定 `--keepalive` 且连接未配置时默认使用 `30s` 保活，可用 `--keepalive 0` 关闭；`forward --continue-on-error` 默认 `true`，让 `-L/-R` 的单次目标连接或传输失败只断开该客户端，并将错误写入 stderr（后台运行时写日志），随后继续监听（显式 `--continue-on-error=false` 可恢复遇错即停）。SSH 通道打开超时仍需关闭整个 SSH 连接才能解除阻塞。保活与容错选项保存在转发记录中，start/restart 沿用，可显式覆盖。

## 端口转发

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
ah forward rm --all
ah forward rm --all -f
```

用法为 `ah forward NAME LOCAL TARGET`，可简写为 `ah f NAME LOCAL TARGET`，两个地址分别传参，不采用 ssh `-L 8080:host:80` 的合并格式。

- `NAME`：已保存的 SSH 连接名。
- 第一个端口 `LOCAL`：**本地监听端口**，例如 `8080` 或 `0.0.0.0:8080`。
- 第二个参数 `TARGET`：**SSH 服务器侧的目标服务端口**，例如 `80` 表示 SSH 服务器上的 `127.0.0.1:80`；也可指定由该服务器访问的 `HOST:PORT`。

例如 `ah forward A 8080 80 -d` 表示「本地 8080 → SSH 服务器 A 上的 80」。
这里的 `80` 是目标服务端口，SSH 登录端口仍使用连接配置中的 `port`。

两端只写端口时均默认 `127.0.0.1`；显式地址使用 `HOST:PORT`，IPv6 地址需加方括号。
目标从 SSH 服务器访问和解析。端口范围为 1–65535，每次命令创建一个 TCP 转发，可同时服务多个连接。

新建转发时自动生成 ID。加 `-d` / `--daemon` 后脱离终端运行，SSH 和本地监听准备就绪后才返回 ID；
不加则在前台运行，Ctrl+C 关闭监听、活动连接及 SSH 连接。后台模式支持 Linux/macOS，使用已保存密码、可用私钥或 SSH agent，不能交互输入密码或私钥口令。
`ah forward ls` 显示当前用户的所有转发记录，包括 ID、类型、连接名、监听/目标地址、PID、状态和错误，支持 `--status` / `--running` 筛选；
`ah forward ls --json` 以稳定字段输出同一份列表（`id`、`type`、`name`、`listen`、`target`、`pid`、`status`、`started`、`error`；`type` 为 `local`/`dynamic`/`remote`），便于脚本消费，内部字段不进输出。
`ah forward kill ID`、`start ID`、`restart ID`、`rm ID` 均支持按对应状态过滤的 ID Tab 补全。
`ah forward kill ID` 通过私有控制通道停止对应转发，并等待关闭完成。前台转发也可按 ID 停止。
`ah forward start ID` 按原 ID 在后台启动已停止或失败的转发；对运行中的转发报错。
`ah forward restart ID` 等待旧转发停止后按原 ID 后台启动，已停止或失败时直接启动。
启动会恢复记录的配置/密钥/known_hosts 路径、工作目录和超时，使用当前连接配置；显式全局选项可覆盖路径和超时。
旧版记录未保存这些信息时使用当前默认值或显式选项。首次主机信任授权不会保存供重启复用。
`ah forward rm ID` 删除已停止或失败的记录及日志；`ah forward rm -f ID` 先停止运行中或启动中的转发再删除（控制通道不可达的 `stale` 记录会在警告后删除，不按 PID 杀进程）；`ah forward rm --stale` 批量清理所有 `stale` 记录。
`ah forward rm` 也可写作 `ah forward remove`，与顶层 `ah remove`/`ah rm` 对应。
`ah forward rm --all` 删除所有已停止或失败的记录，其余（运行中、启动中、stale、不可读）在 stderr 提示跳过；`ah forward rm --all -f` 逐条按 `-f` 语义全部删除。`--all`、`--stale` 不能与 ID 同时使用。
记录不可读（JSON 损坏）时普通 `ah forward rm ID` 报错并提示 `--force`，只有 `ah forward rm -f ID` 会删除，且会在 stderr 警告该记录的控制通道不可知、其工作进程可能仍在监听；文件名不是合法 ID 的游离文件不属记录，需手工清理。
控制通道不可达时不会强删或按 PID 杀进程；无法确认停止时保留记录并报错。生命周期操作按 ID 加锁，删除后保留锁文件。
停止和失败记录保留；异常中断留下的 running 记录在控制通道不可达时显示 stale，不根据旧 PID 杀进程。
单个记录文件损坏或文件名不是合法 ID 时，该条显示 corrupt 并附带原因，不影响其余记录的展示；corrupt 记录的控制通道不可知，不能核验也无法停止其工作进程。
状态与后台日志位于用户配置目录的 `ah/forwards/<ID>.json` 和 `<ID>.log`，独立于 `--config` 指定的连接文件。
后台进程默认不自动重连；可用 `--reconnect` 显式启用，见下文。系统重启后不会自动恢复。
复用连接配置中的 SSH 认证、SOCKS5 代理链及主机密钥校验，支持连接名 Tab 补全。
转发由 SSH 服务处理，不执行 shell 或 sudo；服务端须允许 TCP 转发（`AllowTcpForwarding yes`）。把只在内网可达的数据库、Redis、Web 后台等 TCP 服务拉到本机端口使用，完整示例见 [经 SSH 访问内网服务](internal-services-over-ssh.md)。
监听失败、SSH 断开、目标连接失败或数据传输错误会结束命令并返回非零退出码。
`--timeout` 同时限制 SSH 建连及每次目标连接建立时间，不限制已建立连接的传输时长。

### 动态 SOCKS5 与远程转发

```sh
ah forward A 1080 -D -d
# 本地 127.0.0.1:1080 起 SOCKS5 代理，经 A 访问任意目标
curl --socks5-hostname 127.0.0.1:1080 http://web.internal/
ah forward A 9000 3000 -R -d
# SSH 服务器 A 上的 127.0.0.1:9000 → 本机 127.0.0.1:3000
```

转发分三种，由参数选择，三者互斥：

- `-L` / `--local`（默认，可省略）：`NAME LOCAL TARGET`，本地监听，目标由 SSH 服务器访问。
- `-D` / `--dynamic`：`NAME LOCAL`，本地起 SOCKS5 代理（无认证，仅 CONNECT，支持 IPv4/IPv6/域名）。每个请求由 SSH 服务器连接目标，域名也在服务器侧解析；浏览器需开启“通过 SOCKS 代理 DNS”，curl 用 `--socks5-hostname` 或 `socks5h://`，否则内网域名会在本机解析失败。单个目标连不上只向该请求回复失败并记入 stderr（后台时即日志），不结束代理；SSH 断开才结束。
- `-R` / `--remote`：`NAME REMOTE LOCAL_TARGET`，SSH 服务器监听 REMOTE，连接转到运行 ah 的机器能访问的 LOCAL_TARGET。与 `-L` 一样，目标连接失败会结束转发。服务器侧监听非回环地址需要 sshd `GatewayPorts`，否则 sshd 仍只监听回环；远端监听地址请写 IP。

`-D` 显式写 `0.0.0.0:1080` 会让局域网里任何人都能借这台 SSH 服务器上网，只在确有需要时这样做。
三种转发共用 ID、`-d`、`ls`/`kill`/`start`/`restart`/`rm` 生命周期；`ls` 的 TYPE 列显示 `-L`/`-D`/`-R`，`-D` 的 TARGET 显示 `(socks5)`，LISTEN 对 `-R` 是服务器侧地址。旧版记录按 `-L` 处理。

### 转发自动重连

```sh
ah --keepalive 20s forward nas 15432 5432 --continue-on-error --reconnect -d
```

`--reconnect` 在首次启动成功后，转发失败时按 1、2、4 秒递增等待，最多间隔 30 秒重试，直到恢复或被停止。首次启动失败仍立即返回错误。原 ID 和监听地址保持不变，重试使用当前连接配置，不弹认证或信任提示，每次都校验主机密钥。`ls` 保留 running 状态，ERROR 显示 reconnecting 及最近一次断线原因；后台细节见日志。重连期间监听可能不可用，旧 TCP 连接会断开，应用需重新连接。kill、restart 和 rm -f 仍可取消重试。start/restart 继承该选项，可用 `--reconnect=false` 关闭。

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

目标为已有目录时采用源文件名；父目录须已存在。复制目录需加 `-r`；断点续传使用 `--resume`，见下文。不支持远端通配符展开。

默认拒绝覆盖；确认需要替换时使用：

```sh
ah cp --force ./report.csv nas:/home/alice/report.csv
# -f 等价于 --force
```

复制先写同目录临时文件再原子发布。远端默认要求 `hardlink@openssh.com`，覆盖要求 `posix-rename@openssh.com`；缺少扩展会报错。本地使用对应的硬链接或原子重命名。保留普通权限位，不复制属主、时间戳或 ACL。

失败时尽力清理临时文件；断线可能留下错误中指出的 `.ah-copy-*` 文件。发布响应丢失时，目标可能已存在，重试前先检查目标。

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

### 复制预览与排除

```sh
ah cp -r --backup --dry-run ./site nas:/var/www/
ah cp -r --exclude .git --exclude '*.log' --exclude 'build/' ./site nas:/var/www/
```

`--dry-run` 查询文件并比较内容，显示最终落点与 mkdir、merge、add、replace、unchanged、exclude、skip-special、backup、prune-backup 操作，不修改复制目标、不生成备份、不写复制历史。预览仍需正常 SSH 认证，首次主机信任按既有规则处理。它反映查询时的状态，正式复制时会重新检查；不能保证之后的写权限或空间足够。

`--exclude` 可重复，仅用于 `-r`。模式使用 `/` 分隔，按 Go path.Match 匹配；不含斜杠的模式匹配任意层级的文件名，含斜杠的模式相对源目录匹配，尾部 `/` 只匹配目录。命中目录时不再遍历其子项。`*` 不跨越斜杠，`**` 不表示递归通配；不支持绝对路径、`..` 路径段或反斜杠。排除项不计入进度和复制字节，规则会保存到历史并用于重跑。

### 断点续传

```sh
ah cp --resume ./large.tar nas:~/large.tar
ah cp --resume nas:~/large.tar ./large.tar
ah cp -r -f --resume ./dataset nas:~/dataset
```

`--resume` 失败时保留同目录 `.ah-resume-*` 普通文件，并在错误中给出路径。重跑同一源和目标时，先计算源文件 SHA-256，再寻找内容指纹、源路径、目标路径和权限匹配的片段；按长度选取，逐块核对前缀后继续剩余内容。损坏片段明确报错，源内容变化时不复用旧片段，传输期间源内容或时间/大小发生变化时拒绝发布。只有校验完成才按原有 no-clobber、force 或 backup 规则原子发布。目录复制逐文件续传，重跑已部分完成的目录需 `-f` 或 `--backup` 合并。历史 show/run 保留 resume 选项。

每次重试使用独立临时文件，旧片段只读，避免并发重试修改已发布文件。已校验前缀会复制到新临时文件；为了完整性校验，源文件会完整预读一次，远端片段也需要读取。因此续传会增加读取与临时空间成本，不保证节省网络流量。成功时清理本次临时文件，历史失败片段保留以免影响其他重试，确认不再需要后可手工删除错误里给出的片段。`--dry-run --resume` 只读校验片段，并以 resume-prefix 操作显示复用来源。

## 目录差异与同步

```sh
ah diff ./dist web:/srv/app
ah sync ./dist web:/srv/app
ah sync --delete --dry-run ./dist web:/srv/app
ah sync --delete --exclude '*.log' --exclude 'uploads/' ./dist web:/srv/app
```

支持本地与远端四种方向，源、目标均为目录。内容直接同步到目标根，不追加源目录名；目标缺失时创建，父目录须存在。文件按内容和权限比较，相同文件保留；新建目录继承源权限，已有目录保留权限。

| 输出 | 含义 |
| --- | --- |
| `add` / `mkdir` | 新增文件 / 目录 |
| `modify` | 文件内容或权限不同 |
| `target-only` | diff 中仅目标存在的项目 |
| `delete` / `retain` | 同步预览将删除 / 保留的目标项目 |
| `conflict` | 文件与目录类型不一致，实际同步及同步预览报错 |
| `exclude` / `skip-special` | 排除项 / 符号链接或特殊文件，保留不动 |

默认保留目标多余项目；`--delete` 在新增、更新全部成功后才逐项删除多余普通文件和空目录。`--exclude` 可重复，语义同复制，并保护命中的目标项目。包含排除项或特殊文件的多余目录也会保留。根路径不能为符号链接；同一本地文件系统或同一连接名下，源、目标目录不能重叠。

`diff` 和 `--dry-run` 不写目标或历史，但仍需 SSH 认证及首次信任。预览不保证后续写权限、空间、发布扩展可用。同步逐文件原子替换，树整体非原子，失败保留已完成更新并停止后续删除；并发修改不保证一致快照。删除前复查源清单和待删目标元数据，检测到变化时报错。

同步保留历史，`history show/run` 保留 `--delete` 和排除项；重跑带删除的同步记录也会删除多余文件。旧记录仍按复制重跑。

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

重跑保留原工作目录、源/目标和 force，默认使用原 config/key-file/known-hosts 路径；通过全局选项可显式覆盖这些路径。连接定义使用配置文件的当前值，包括代理和 sudo。历史不会保存首次主机信任授权，也不会还原过去的文件内容（还原备份用 `ah backup restore`）。

先用 `show` 确认目标及覆盖选项，再用 `run ID`；无需对数据库内容执行 `eval`。本地目标不能覆盖当前历史库及 SQLite 辅助文件。

历史重跑默认继承原覆盖设置。目标已存在且确定需要替换时，使用 `ah h run 1 --force`（简写 `-f`）；使用 `--force=false` 可显式禁止继承覆盖。每次重跑都会生成新的历史 ID，所以错误中的 ID 可能不同于重跑的原 ID。

`ah history clean`（`ah h clean`）删除 success/failed/canceled 历史并显示删除条数，保留 running 记录以免干扰进行中的复制；不重置历史 ID。仅清理 `--history-file` 指定的历史库，不删除连接配置或复制文件。此操作不能撤销，agent 仅在用户要求清理历史时执行。

历史清理支持筛选：

```sh
ah h clean --failed                # 仅删除失败记录
ah h clean --keep-days 7           # 保留最近 7 天，删除更早的已结束记录
ah h clean --failed --keep-days 7  # 仅删除 7 天前的失败记录
```

天数取值 1–36500，按开始时间计算，每天为 24 小时；条件组合取交集，截止时间及之后的记录保留。running 始终保留；不带筛选时仍清理全部已结束记录。

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
tags = ["prod", "storage"]
note = "家里的 NAS"
# password / sudo_password 由命令生成 enc:v1: 密文，不在这里填写明文。
```

相对 `identity_file` 以执行目录解析，建议使用绝对路径或 `~/`。TOML 未知字段、无效配置和明文密码会报错；连接名参与密码认证绑定，手工改名后需要重新设置密码。

密钥认证支持显式私钥、`SSH_AUTH_SOCK` agent，以及默认的 `~/.ssh/id_ed25519`、`~/.ssh/id_rsa`。显式加密私钥可使用 agent 中已解锁的同一密钥，否则在交互终端输入口令。临时输入的密码和私钥口令不会自动保存。

密码采用 AES-256-GCM，主密钥独立存放。配置、主密钥和历史文件权限为 0600，新建私有目录为 0700。备份和迁移需妥善保管配置及对应密钥；只备份 TOML 不能恢复密码，拿到两者的人可以解密。解密时找不到主密钥会报错，不会重新生成替代密钥。

## 配置导出与迁移

```sh
ah config export ./connections.toml
ah config import ./connections.toml --dry-run
ah config import ./connections.toml
ah config import ./connections.toml --force
ah config export ./backup.ah --with-secrets
ah config import ./backup.ah
```

普通导出为 TOML，移除 SSH/sudo 密码和代理认证信息；加密导出会隐藏输入并确认独立备份口令，包含这些凭据，但不包含源主密钥。加密包使用 scrypt 与 AES-256-GCM，导入时用目标本地主密钥重新加密 SSH/sudo 密码。支持旧版 sudo 密码绑定。

导入默认合并，同名时报错，`--force/-f` 替换同名连接的完整定义（包括清除不含凭据导出中省略的密码）。其他连接保留。`--dry-run` 输出 add/replace，不创建配置、锁、目录或主密钥，同名预览也需 `--force`。无效配置、口令错误、包损坏、跳板缺失或循环时不改写配置。

导出文件为 0600，父目录须存在；已有文件需 `config export --force` 才能替换，不能覆盖当前配置、密钥、known_hosts、历史库及辅助文件或当前口令文件。普通 TOML 不接受直接携带绑定源主密钥的 enc:v1 密码，迁移密码需用加密包。文件大小最多 16 MiB。

加密导入、导出可传 `--passphrase-file /path/to/private-file`，代替交互输入；文件须为普通文件，Unix 下仅所有者可访问，末尾 LF/CRLF 不计入口令。口令不可用明文参数传入。私钥文件、主机信任、复制历史和转发记录不迁移；私钥路径原样保留，目标机器上按需修改。

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
