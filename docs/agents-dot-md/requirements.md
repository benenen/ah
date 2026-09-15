# 功能契约

> 实现命令、TOML 配置、SFTP 复制或路径补全时读取；区分用户需求与初始约定。

## 已确定需求

- 使用 Go 封装 SSH client，主要交付 CLI。
- 管理多个命名连接，包含 list、new、rm、edit 命令。
- 使用 TOML 文件保存连接。
- cp 支持本地/远端四种方向；无 NAME: 前缀的路径为本地。
- 每次复制记录 SQLite 历史，支持关键词模糊查询、显示命令与按 ID 重跑。
- new/edit 可隐藏输入密码，以 AES-256-GCM 加密保存到 TOML；主密钥独立保存在本地受限文件。
- 输入路径时支持 Tab 补全，覆盖复制源端与目标端的本地和远程路径。

## 命令契约

| 命令 | 目标行为 |
| --- | --- |
| `ah list` | 列出连接别名及非敏感连接信息 |
| `ah new <name>` | 新建连接，重复名称报错 |
| `ah edit <name>` | 修改已有连接，校验成功后保存 |
| `ah rm <name>` | 删除配置中的连接记录，名称不存在时报错 |
| `ah connect <name> [COMMAND [ARG...]]` / `ah c <name> [COMMAND [ARG...]]` | 无命令时交互登录；带命令时执行远程命令并退出，两者等价并支持连接名补全 |
| `ah cp A:/source/file B:/target/file` | 从 A 经 SFTP 读取，并经 SFTP 写入 B |
| `ah completion <shell>` | 输出 shell 补全脚本，首批覆盖 Bash、Zsh、Fish |

`connect`、`completion` 是配套命令约定。目录递归复制、SSH 跳板机、断点续传暂不列入已确定范围。遇到已有目标文件时默认报错；显式覆盖选项为 `--force` / `-f`。

## 配置

默认使用 `os.UserConfigDir()` 下的 `ah/connections.toml`，允许 `--config <path>` 覆盖。连接以唯一别名索引，字段至少含 host、port、user，可指定 identity_file；端口默认 22。`new/edit --password` 隐藏输入并加密保存，`edit --clear-password` 删除密码。TOML 不接受明文密码。`--key-file` 指定主密钥，默认同用户配置目录下的 ah/master.key，0600；缺失或损坏时解密报错。保存的密码可用于 connect/c/cp 与非交互补全。

示意结构（虚构地址）：

```toml
[connections.A]
host = "a.example.com"
port = 22
user = "alice"
identity_file = "~/.ssh/id_ed25519"

[connections.B]
host = "b.example.com"
port = 22
user = "bob"
```

配置不存在时 list 返回空列表，new 可创建；损坏配置必须报错而非覆盖为空。写入采用同目录临时文件和原子替换，Unix 文件权限为 0600、配置目录为 0700；防止并发修改静默丢失。

## 复制与补全验收

- A 到 B 的内容经运行 ah 的本机流式中转，使用两个 SSH/SFTP 会话；无需 A 能直连 B，不把整个文件读入内存。
- 处理连接失败、权限不足、路径不存在、中途断线、取消、写入及关闭错误；失败返回非零退出码，不把部分文件报告为成功。
- 以同目录临时文件暂存；远端默认使用 hardlink@openssh.com 原子发布，--force 使用 posix-rename@openssh.com；本地使用 os.Link/os.Rename。服务端不支持对应扩展时失败；覆盖前保护原目标。失败尽力清理，连接中断时在错误中报告可能残留的临时文件。
- `ah cp <Tab>` 可补全连接别名；`ah cp A:/dir/<Tab>` 查询 A 的目录；第二个参数按 B 的连接独立补全。
- 目录候选追加 `/`，正确处理空格、中文及 shell 特殊字符；使用 SFTP 列目录，不拼接远程 shell 命令。
- 远程路径按 POSIX 语义处理；`~` 使用 SFTP Getwd 返回的登录目录显式展开；拒绝 `~user`。
- 补全只查询，设置短超时；失败不阻塞终端、不启动密码输入或主机信任提示，不向候选标准输出混入日志。
- Shell 命令行补全是初始交互方式；如果新增程序内路径输入框，其 Tab 也复用同一目录查询逻辑。

## 复制历史契约

默认 os.UserConfigDir()/ah/history.db，可用 --history-file 覆盖；SQLite 文件 0600。
传输前持久化 running，完成后更新 success/failed/canceled，保存路径、cwd、配置路径、时间、字节数、force 和错误，不记录密码或密钥内容。不能持久化时不开始传输；崩溃可能保留 running。本地目标不得覆盖当前历史库及 sidecar。
`history [关键词...]` / `history search` 大小写不敏感、多关键词 AND 子串搜索，--limit 默认 20。
`history show ID` 输出安全引用且保留原工作目录的命令；`history run ID` 使用结构化参数重跑并另记历史，不执行 shell 文本。保留原 cwd/force/配置路径，使用当前连接定义，显式全局配置选项可覆盖记录路径。

## 远程命令契约

连接名后的参数按 SSH 方式用空格连接，交给远程 shell 解析；ah 自身选项必须位于连接名前，之后的选项属于远程命令。命令模式不申请 PTY、不将本地终端改为 raw，转发 stdin/stdout/stderr，保留远程退出码；完成或取消时停止输入转发并关闭连接，保留调用方 stdin。无命令时维持既有交互终端行为。

交互模式申请 PTY 时，按 OpenSSH 的做法在切换 raw 前读取本地终端设置，把控制字符、输入/输出/本地标志和真实波特率随 pty-req 发给远端；erase 键与 IUTF8 必须生效，否则 backspace 在 canonical 输入下会按字节删除多字节字符。CS7/CS8 共用 CSIZE 位且服务端按任意顺序应用，只发送当前实际字宽；sudo 认证期间仍强制关闭回显。

pty-req 的 TERM 优先级为全局 `--term` > 连接 TOML 的 `term` > 本地 `$TERM` > `xterm-256color`；`term` 字段校验不含空白与控制字符，new/edit 用 `--term` 设置、`--clear-term` 清除。该字段用于远端 terminfo 缺少本地终端类型的场景，否则远端行编辑会退化。

## SOCKS5 代理契约

连接 TOML 的 proxies 字符串数组按跳序保存代理。new/edit 支持重复 --proxy，edit 替换整条链，--clear-proxy 恢复直连；未指定时保留已有配置。地址支持 HOST:PORT 或 socks5://HOST:PORT（IPv6 必须加方括号），同时支持 socks5h URL 和代理认证；代理凭据原样存储，错误不得回显凭据。
SSH 统一拨号路径用于 connect/c/cp/远程补全；第一跳本机解析，后续跳和目标由前一跳解析。使用同一握手 context 约束所有跳，超时/取消关闭链路，不回退直连。主机密钥始终按最终目标校验，不把 TCP 代理身份当作目标身份。

## sudo 提权契约

每个连接支持 sudo 布尔开关、独立加密的 sudo_password 和可选绝对路径 sftp_server。new/edit --sudo 设置开关，--sudo=false 关闭；--sudo-password 隐藏输入并加密保存，--clear-sudo-password 删除，仅保存密码不自动开启。sudo 密码密文使用 name/sudo AAD，不能与 SSH 密码互换，不得保存明文。
统一 SSH 层用于 connect/c/远程命令/cp/补全。sudo 请求密码时，优先用保存密码，否则交互终端手动输入；非交互不提示，免密sudo不发密码。使用 sudo -S 自定义随机提示，仅在收到提示时向 stdin 发密码；root ready 标记之后才转发业务输入或SFTP包。PTY模式处理合并流，认证期间关闭回显，成功后恢复回显；认证后 stderr 实时转发。
管理员 shell 使用 root 的 /bin/sh -l；复制启动root独立sftp-server（常见路径探测，可覆盖），不改变本地文件操作身份。要求服务器账户有相应sudo权限，不改sudoers；仅internal-sftp不足以完成此模式。sudo认证/初始化需限时、支持取消，失败不回退普通用户。

兼容旧 proxy 字符串（与 proxies 互斥）、socks5h URL、代理 URL 认证和 --no-sudo。代理 URL 认证信息原样存储，不属于加密密码。旧 sudo 密文按旧连接名绑定解密，新密码使用独立 sudo 绑定；不再隐式复用 SSH 密码。

命令统一支持 connect/c、copy/cp、edit/e、history/h、list/ls、new/n、remove/rm；顶层 help 显示简写，子命令帮助显示别名。-h 仍为帮助标志。

history run ID 支持 --force/-f 和 --force=false 显式覆盖原 force；未指定则继承原值，新历史记录保存最终生效值。

`ah history clean`（`ah h clean`）删除 success/failed/canceled 历史并显示删除条数，保留 running 记录以免干扰进行中的复制；不重置历史 ID。仅清理 `--history-file` 指定的历史库，不删除连接配置或复制文件。此操作不能撤销，agent 仅在用户要求清理历史时执行。

历史清理支持筛选：

```sh
ah h clean --failed                # 仅删除失败记录
ah h clean --keep-days 7           # 保留最近 7 天，删除更早的已结束记录
ah h clean --failed --keep-days 7  # 仅删除 7 天前的失败记录
```

天数取值 1–36500，按开始时间计算，每天为 24 小时；条件组合取交集，截止时间及之后的记录保留。running 始终保留；不带筛选时仍清理全部已结束记录。
