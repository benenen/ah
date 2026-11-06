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
| `ah inspect NAME` | 以 JSON 展示单个连接配置；凭据仅显示是否配置，代理认证信息隐藏，支持连接名补全 |
| `ah new <name>` | 新建连接，重复名称报错 |
| `ah edit <name>` | 修改已有连接，校验成功后保存 |
| `ah rm <name>` | 删除配置中的连接记录，名称不存在时报错 |
| `ah connect <name> [COMMAND [ARG...]]` / `ah c <name> [COMMAND [ARG...]]` | 无命令时交互登录；带命令时执行远程命令并退出，两者等价并支持连接名补全 |
| `ah cp A:/source/file B:/target/file` | 从 A 经 SFTP 读取，并经 SFTP 写入 B |
| `ah backup ls/restore TARGET` | 列出/还原 `cp --backup` 留下的备份 |
| `ah completion <shell>` | 输出 shell 补全脚本，首批覆盖 Bash、Zsh、Fish |
| `ah version` / `ah v` | 输出版本、commit、构建时间与运行平台；版本由构建时注入，源码构建显示 dev |

`connect`、`completion`、`version` 是配套命令约定。SSH 命名跳板机已扩展支持，见下文；断点续传按后续扩展契约执行。遇到已有目标文件时默认报错；显式覆盖选项为 `--force` / `-f`。

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
- `cp -r`：按 cp -r 解析目标（已有目录则落到 目标/源名，否则新建）；落点已存在时无 -f 在写入前报错，-f 合并（同名文件逐个原子替换，多余文件保留）；文件/目录类型冲突报错；同一文件系统禁止复制进自身。符号链接与特殊文件跳过并在 stderr 报告。目录先以 0700 建立，结束后自深向浅设源权限位，合并时不改已有目录权限。树整体非原子，失败保留已复制文件。本地目标逐个文件检查历史库保护。
- `--backup [--backup-keep N]`（默认 5，1–1000）：替换已有普通文件前，将其硬链接为同目录 `bak.NAME.bak-<UTC 纳秒时间戳>`，此前按时间删除最旧的同名备份使总数不超过 N；只识别严格匹配该格式的文件。隐含覆盖（用于目录时隐含合并）；远端需 hardlink@openssh.com。
- `backup ls TARGET` 按时间倒序列出 TARGET 的备份（序号、UTC 时间、大小、文件名），无备份时正常退出并说明。`backup restore TARGET [VERSION] [--backup-keep N]`：VERSION 省略取最新，可为序号（<8 位数字）、≥8 位的时间戳前缀或完整备份名，匹配多份报错；先按 --backup 规则备份当前文件（被还原的那份不参与清理，可能因此多留一份），再原子写回，内容相同则不动；目标缺失也可还原；不写复制历史。
- 替换模式（-f/--backup）下，目标为普通文件且大小、权限位、内容均与源相同时跳过：不替换、不备份，计为 unchanged 并在输出中报告；进度按已完成计。
- `ah cp <Tab>` 可补全连接别名；`ah cp A:/dir/<Tab>` 查询 A 的目录；第二个参数按 B 的连接独立补全。
- 目录候选追加 `/`，正确处理空格、中文及 shell 特殊字符；使用 SFTP 列目录，不拼接远程 shell 命令。
- 远程路径按 POSIX 语义处理；`~` 使用 SFTP Getwd 返回的登录目录显式展开；拒绝 `~user`。
- 补全只查询，设置短超时；失败不阻塞终端、不启动密码输入或主机信任提示，不向候选标准输出混入日志。
- Shell 命令行补全是初始交互方式；如果新增程序内路径输入框，其 Tab 也复用同一目录查询逻辑。

## 主机密钥契约

默认校验 `~/.ssh/known_hosts`，`--known-hosts PATH` 可覆盖。未知主机在交互终端打印目标地址与 SSH 指纹，并等待 yes/no：只有明确回答 yes 才追加密钥并继续；回答 no 或没有交互终端（含补全）都拒绝。`--trust-new-host` 跳过提示，直接信任并保存首次见到的密钥。
已有密钥发生变化时始终拒绝，不提示也不覆盖记录。等待回答的时间不计入 `--timeout`，认证、连接与握手其余阶段仍受其约束。非 macOS/Linux 平台不提供交互确认。

## 复制历史契约

默认 os.UserConfigDir()/ah/history.db，可用 --history-file 覆盖；SQLite 文件 0600。
传输前持久化 running，完成后更新 success/failed/canceled，保存路径、cwd、配置路径、时间、字节数、force、recursive、backup_keep 和错误（旧库打开时自动补列，旧记录视为非递归、无备份），不记录密码或密钥内容。不能持久化时不开始传输；崩溃可能保留 running。本地目标不得覆盖当前历史库及 sidecar。
`history [关键词...]` / `history search` 大小写不敏感、多关键词 AND 子串搜索，--limit 默认 20。
`history show ID` 输出安全引用且保留原工作目录的命令；`history run ID` 使用结构化参数重跑并另记历史，不执行 shell 文本。保留原 cwd/force/recursive/backup_keep/配置路径，使用当前连接定义，显式全局配置选项可覆盖记录路径。

## 远程命令契约

连接名后的参数按 SSH 方式用空格连接，交给远程 shell 解析；ah 自身选项必须位于连接名前，之后的选项属于远程命令。命令模式不申请 PTY、不将本地终端改为 raw，转发 stdin/stdout/stderr，保留远程退出码；完成或取消时停止输入转发并关闭连接，保留调用方 stdin。无命令时维持既有交互终端行为。

交互模式申请 PTY 时，按 OpenSSH 的做法在切换 raw 前读取本地终端设置，把控制字符、输入/输出/本地标志和真实波特率随 pty-req 发给远端；erase 键与 IUTF8 必须生效，否则 backspace 在 canonical 输入下会按字节删除多字节字符。CS7/CS8 共用 CSIZE 位且服务端按任意顺序应用，只发送当前实际字宽；sudo 认证期间仍强制关闭回显。

pty-req 的 TERM 优先级为全局 `--term` > 连接 TOML 的 `term` > 本地 `$TERM` > `xterm-256color`；`term` 字段校验不含空白与控制字符。new 默认保存 `term = "xterm-256color"`，可用 `--term` 覆盖或 `--term ''` 使用本地 `$TERM`；edit 用 `--term` 设置、`--clear-term` 清除。该字段用于远端 terminfo 缺少本地终端类型的场景，否则远端行编辑会退化。

## SOCKS5 代理契约

连接 TOML 的 proxies 字符串数组按跳序保存代理。new/edit 支持重复 --proxy，edit 替换整条链，--clear-proxy 恢复直连；未指定时保留已有配置。地址支持 HOST:PORT 或 socks5://HOST:PORT（IPv6 必须加方括号），同时支持 socks5h URL 和代理认证；代理凭据原样存储，错误不得回显凭据。
SSH 统一拨号路径用于 connect/c/cp/远程补全；第一跳本机解析，后续跳和目标由前一跳解析。使用同一握手 context 约束所有跳，超时/取消关闭链路，不回退直连。主机密钥始终按最终目标校验，不把 TCP 代理身份当作目标身份。

## sudo 提权契约

每个连接支持 sudo 布尔开关、独立加密的 sudo_password 和可选绝对路径 sftp_server。new/edit --sudo 设置开关，--sudo=false 关闭；--sudo-password 隐藏输入并加密保存，--clear-sudo-password 删除，仅保存密码不自动开启。sudo 密码密文使用 name/sudo AAD，不能与 SSH 密码互换，不得保存明文。
统一 SSH 层用于 connect/c/远程命令/cp/补全。sudo 请求密码时，优先用保存密码，否则交互终端手动输入；非交互不提示，免密sudo不发密码。使用 sudo -S 自定义随机提示，仅在收到提示时向 stdin 发密码；root ready 标记之后才转发业务输入或SFTP包。PTY模式处理合并流，认证期间关闭回显，成功后恢复回显；认证后 stderr 实时转发。
管理员 shell 使用 root 的 /bin/sh -l；复制启动root独立sftp-server（常见路径探测，可覆盖），不改变本地文件操作身份。要求服务器账户有相应sudo权限，不改sudoers；仅internal-sftp不足以完成此模式。sudo认证/初始化需限时、支持取消，失败不回退普通用户。

兼容旧 proxy 字符串（与 proxies 互斥）、socks5h URL、代理 URL 认证和 --no-sudo。代理 URL 认证信息原样存储，不属于加密密码。旧 sudo 密文按旧连接名绑定解密，新密码使用独立 sudo 绑定；不再隐式复用 SSH 密码。

命令统一支持 connect/c、copy/cp、edit/e、history/h、list/ls、new/n、remove/rm、version/v；顶层 help 显示简写，子命令帮助显示别名。-h 仍为帮助标志。

history run ID 支持 --force/-f 和 --force=false 显式覆盖原 force；未指定则继承原值，新历史记录保存最终生效值。

`ah history clean`（`ah h clean`）删除 success/failed/canceled 历史并显示删除条数，保留 running 记录以免干扰进行中的复制；不重置历史 ID。仅清理 `--history-file` 指定的历史库，不删除连接配置或复制文件。此操作不能撤销，agent 仅在用户要求清理历史时执行。

历史清理支持筛选：

```sh
ah h clean --failed                # 仅删除失败记录
ah h clean --keep-days 7           # 保留最近 7 天，删除更早的已结束记录
ah h clean --failed --keep-days 7  # 仅删除 7 天前的失败记录
```

天数取值 1–36500，按开始时间计算，每天为 24 小时；条件组合取交集，截止时间及之后的记录保留。running 始终保留；不带筛选时仍清理全部已结束记录。

## TCP 转发契约

`ah forward NAME LOCAL TARGET [-d]`：LOCAL 和 TARGET 分别为端口或 HOST:PORT，只写端口均默认 127.0.0.1；
IPv6 使用方括号。明确填写 0.0.0.0 才监听所有 IPv4 网卡，不采用 ssh -L 的合并参数格式。
`-L`（默认）、`-D`、`-R` 互斥选择类型，记录保存类型，缺省视为 -L；ls 以 TYPE 列显示参数名，JSON 增加 type（local/dynamic/remote）。
`-D NAME LOCAL`：本地 SOCKS5（无认证、仅 CONNECT、IPv4/IPv6/域名），目标由 SSH 服务器拨号和解析；单个请求的握手/拨号/传输失败只结束该请求并写 stderr，不结束代理，拨号超时向客户端回复失败。
`-R NAME REMOTE LOCAL_TARGET`：tcpip-forward 在服务器监听，连接由本机拨 LOCAL_TARGET；远端监听请求受 --timeout 限制，目标失败规则同 -L。
复用命名连接的 SSH 认证、代理链和主机密钥校验；目标由远端访问。转发不执行 shell/sudo。

每次新建生成独立 ID，-d 后台运行（Linux/macOS），无 -d 保留前台和 Ctrl+C。
后台只在 SSH 与本地监听准备完成后报告成功，不能交互索取密码；失败向调用方返回非零状态。
`forward ls` 展示当前用户的前后台记录和运行状态，`--json` 输出稳定的 id/name/listen/target/pid/status/started/error 字段；`forward kill ID` 通过带 ID 校验的私有控制通道取消并等待停止，
避免旧 PID 被复用时误杀其他进程。停止/失败记录保留，无法联系的 running 记录显示 stale。
单条记录文件损坏（或文件名不是合法 ID）时只将该条标为 corrupt 并附原因，不得因此让整份列表失败；corrupt 记录无法核验或停止其工作进程，也不根据残留 PID 杀进程。
状态和日志保存在用户配置目录 ah/forwards（目录 0700，文件 0600），不随 --config 分组。
取消、SSH 断开和转发错误必须释放监听、SSH、活动 TCP 和 goroutine；支持双向半关闭。
目标拨号受 --timeout 限制，已建立连接不受其时长限制；目标连接或数据传输失败结束转发，不自动重连/开机恢复。

`forward start ID` 按原 ID 后台启动 stopped/failed 转发，拒绝重复启动活动转发；
`forward restart ID` 先等待活动转发停止，再按原 ID 后台启动，stopped/failed 则直接启动。
保存启动配置/密钥/known_hosts 路径、工作目录和超时，不保存凭据或首次信任授权；重启读取当前连接配置，显式全局选项可覆盖。
旧记录缺少启动信息时使用当前默认值或显式选项。
`forward rm ID` 删除 stopped/failed 记录和日志；`forward rm -f ID` 先取消 running/starting 转发，确认停止后删除。
`forward rm --all` 只删 stopped/failed 并在 stderr 提示跳过其余；`--all -f` 对每条按 `rm -f` 处理，单条失败不中断其余，最终非零退出；`--all` 与 ID 互斥。
记录不可读时只有 `-f` 可删（普通 rm 报错并提示），删除前必须向 stderr 警告其控制通道不可知、可能残留监听；不可读记录不得绕过状态检查。
生命周期操作按 ID 加锁（锁文件保留）；控制通道不可达时保留记录并报错，不通过旧 PID 杀进程。

## 连接复制与查询扩展

`clone OLD NEW` 保留原连接，`rename OLD NEW` 删除原别名。新别名须合法且不存在；配置更新锁内完成凭据按新名重新加密，兼容旧 sudo 绑定。失败不提交配置。历史和转发不改名。

`list --json` 输出按名称排序的安全字段数组（name/host/port/user/term/tags/note，tags 无值时为 []）。`history [search] --json` 输出记录数组，空数组为 []；`--status` 精确匹配合法状态，`--connection` 精确匹配源/目标连接名；`--after/--before` 为 RFC3339 开始时间范围，左闭右开；所有条件与关键词取交集，先筛选再应用 limit。

## 复制预览与排除

`cp --dry-run` 复用端点解析、认证与发布扩展校验，以只读查询列出最终落点及目录创建/合并、文件新增/替换/相同跳过、特殊文件跳过、排除、备份和旧备份清理。不写复制历史或目标，源目标变化及实际写权限/空间不作保证；首次 SSH 信任维持原行为。

`cp -r --exclude PATTERN` 可重复，Go path.Match 的 POSIX 相对 glob；无斜杠匹配任意层级 basename，有斜杠匹配源根相对路径，尾斜杠限定目录，命中目录剪枝。不支持递归 ** 语义、绝对路径、.. 段和反斜杠。非法模式在传输前拒绝，排除项不计进度；SQLite 自动补 exclude_patterns JSON 列，旧记录无排除，show/run 保留模式。

## 诊断与保活扩展

`doctor NAME` 检查配置、认证资料加载、TCP/代理链、SSH 密钥交换、host key、认证、sudo（启用时）、SFTP 及 hardlink/posix-rename 扩展，各阶段输出 OK/FAIL；失败非零。不绕过主机身份校验，不写测试文件。SFTP 启动受 --timeout 约束。

全局 `--keepalive` 默认 0 关闭，负值拒绝；非零周期发送 want-reply SSH 请求，任意回复视为存活，超时按 --timeout 关闭连接，取消与关闭回收请求 goroutine。`forward --continue-on-error` 默认 false，启用时单次 -L/-R 目标拨号/传输错误写 stderr 并继续监听；SSH channel-open 超时仍结束传输。后台与 start/restart 保留选项，显式标志覆盖保存值。

## 重连与 SSH 跳板扩展

`forward --reconnect` 默认 false，首次启动成功后才自动重连，等待 1s 指数增长到 30s，使用原 ID/最终监听地址和当前连接定义。重连不提示认证或首次信任，始终校验主机密钥；失败无回退。重连期间状态 running，ERROR 标明 reconnecting，日志记录重试错误，控制通道继续响应停止。旧 TCP 不恢复、监听可暂时中断。start/restart/后台保留布尔选项，显式标志可覆盖。

连接 `jump` 字段由 `new/edit --jump ALIAS` 设置，空字符串清除；与当前连接 SOCKS5 代理互斥，但跳板本身可配置代理。逐跳使用 SSH direct-tcpip、独立身份认证/主机密钥和超时，最多 32 个连接；使用前解析全链拒绝循环与缺失引用。跳板 sudo 不用于转发，关闭最终连接释放整链，失败不直连回退。统一入口用于所有 SSH 调用与补全（补全的总 context 限时仍生效）。rename 原子更新入站 jump 引用。

## 断点续传扩展

`cp --resume` 支持四种方向及递归逐文件复制，历史自动补 resume INTEGER 列（旧记录 false），show/run 保留。失败保留随机同目录 `.ah-resume-<fingerprint>-<random>`，fingerprint 绑定源/目标路径、源 SHA-256 和权限；只读取普通片段、优先最大且不超过源长度。逐块比较前缀并复制到独立 staging，再复制剩余内容，校验实际源流 SHA-256、长度与源句柄 stat 大小/时间后才按原原子发布/备份规则发布。损坏片段失败，源变化不复用旧指纹；每次 staging 独立避免共享写入。取消关闭 SFTP，错误报告保留片段位置。成功只清理本次 staging，旧失败片段不自动删除。该策略完整预读源且克隆前缀，额外读取和空间成本不承诺网络流量降低。预览只读校验并显示 resume-prefix。

## 标签、备注与筛选

连接 TOML 可选 `tags` 字符串数组和 `note` 单行备注，仅用于查找，不影响连接行为。标签以字母或数字开头，只含字母、数字和 `_ . : / -`，最长 64 字节，同一连接内忽略大小写不得重复；备注最多 500 字符，不含控制字符。
`new/edit --tag` 可重复或逗号分隔，new 设置、edit 整体替换；edit 另有 `--add-tag`、`--remove-tag`（删除不存在的标签报错）、`--clear-tags`，`--tag` 与三者互斥，`--clear-tags` 与增删互斥；同时增删时先删后加。`--note` 设置，空字符串清除。CLI 输入按忽略大小写去重并保留首次写法。
`list [KEYWORD...] [--tag TAG...]`：`--tag` 要求同时带有全部标签；关键词忽略大小写，在名称、主机、用户、备注和标签中做子串匹配，多个关键词取交集；与 `--json` 共用筛选。表格增加 TAGS、NOTE 列，空值显示 `-`。inspect 输出 tags/note。`--tag`/`--add-tag` 补全已有标签，`--remove-tag` 补全该连接的标签。

## 目录差异与单向同步

- `diff SOURCE DESTINATION` 与 `sync SOURCE DESTINATION` 仅接受目录，支持四种本地/远端方向，直接映射目录内容，不追加源目录名；目标根可缺失，父目录必须存在。
- `diff` 只读比较普通文件的内容与权限，输出 add/mkdir/modify/target-only/conflict；相同文件不输出。类型冲突可展示，实际 sync 和同步预览报错。目录权限同递归复制：新目录继承源权限，已有目录保留权限。
- `sync` 默认新增和原子替换差异文件、保留目标多余项目；`--delete` 仅在新增/更新成功且源清单复查未变化后删除目标多余普通文件及空目录，逐项复查目标类型和文件大小/时间/权限，不递归强删。
- 两命令支持重复 `--exclude`，沿用 cp 排除语义，命中任一端的项目及子树均保留；符号链接及特殊文件跳过并保留，不删除包含这些项目的多余祖先目录。输出 exclude/skip-special/retain 说明保留原因。
- 拒绝非目录或符号链接根路径。同一本地文件系统或同一连接名下，在解析现有祖先真实路径后拒绝任意方向重叠。不同连接别名或本地与远端是否指向同一存储无法保证识别。
- `sync --dry-run` 不写目标或历史，用 delete/retain 展示多余项处理；与 diff 一样仅反映查询时状态，不保证真实写权限、空间和发布扩展可用。SSH 信任沿用既有规则。
- sync 复用 SSH/SFTP、取消回收、逐文件原子替换和本地历史库保护；写入或删除前预检查所有操作路径。树整体非原子，失败保留已完成更新，停止后续删除；并发修改不保证一致快照。
- 历史自动补 sync/delete_extra INTEGER 列（旧记录 false），show/run 保留同步、删除、排除选项；带删除的历史重跑继续删除。同步始终更新差异文件，history run --force=false 拒绝用于同步记录。

## 配置导出与迁移

- `config export FILE` 导出全部连接为 TOML，移除 SSH/sudo 密码和代理 URL userinfo；保留地址、端口、用户、私钥路径、跳板、代理地址、sudo/TERM、标签和备注，不依赖源主密钥。
- `config export FILE --with-secrets` 需隐藏输入并确认独立口令；以固定版本头、随机 16 字节盐、scrypt（N=65536、r=8、p=1）和 AES-256-GCM 加密完整导出载荷。SSH/sudo 密码先用源主密钥解密，兼容旧 sudo 绑定，再放入加密包；原主密钥不打包。
- `config import FILE` 自动识别 TOML/加密包，后者需口令；严格校验内容和合并后的跳板引用、循环及 32 跳上限。在现有配置锁内合并并原子保存；同名默认整批失败，`--force/-f` 完整替换同名定义（包括凭据），保留其他连接。导入 SSH/sudo 密码按原别名绑定目标本地主密钥重新加密，不替换已有主密钥。
- 导入不含凭据的定义覆盖原连接时清除其保存密码；普通 TOML 不接受直接携带本机绑定的 enc:v1 密码，需改用加密导出。未知版本、错误口令、损坏文件或配置均报错，不改写连接配置。
- `config import --dry-run` 只校验和输出 add/replace，不创建目录、锁、配置或主密钥。同名预览也需 --force。导入/导出文件上限 16 MiB，仅接受普通导入文件；不提供 stdin/stdout 流式导入导出。
- 两端加密操作支持 `--passphrase-file PATH`；普通文件，Unix 下权限不得开放给组或其他用户，读取末尾 LF/CRLF 后口令为 1–4096 字节。交互输入只支持 macOS/Linux，不回显、不记录口令。不支持明文口令参数或环境变量。
- 导出通过同目录 0600 临时文件和原子发布保存，默认拒绝已有落点，--force 原子替换普通文件。父目录须存在；拒绝覆盖符号链接、当前配置及锁、主密钥、known_hosts、历史库及 sidecar、当前口令文件，校验真实路径和硬链接别名。
- 只迁移连接配置与可选保存凭据，不迁移主密钥、私钥文件、known_hosts、历史和转发记录；路径原样保留，按需在目标机器调整。
