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
| `ah completion <shell>` | 输出 shell 补全脚本，首批覆盖 Bash、Zsh |

`connect`、`completion` 是配套命令约定。目录递归复制、跳板机、断点续传暂不列入已确定范围。遇到已有目标文件时默认报错；显式覆盖选项为 `--force` / `-f`。

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
