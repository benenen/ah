# 经 SSH 访问内网服务

只监听 `127.0.0.1`、或在防火墙后只对内网开放的服务——MySQL/PostgreSQL/Redis 之类的数据库、Web 管理后台、HTTP API、消息队列、Elasticsearch——都可以用 `ah forward` 拉到本机端口直接用。**远端不需要脚本、不需要装对应客户端、不用改服务配置。**

## 原理

`ah forward` 走 SSH 的 `direct-tcpip` 通道：由**远端的 sshd 自己发起对目标的 TCP 连接**，数据再经 SSH 连接回到本机。由此：

- 目标地址是从 **SSH 服务器那一侧**解析和访问的，不是从你的机器。`db-internal:3306`、`web.internal:80` 都按服务器那边的解析结果走。
- 远端只需要一个能登录、且允许 TCP 转发的 sshd。没有脚本、没有 agent、没有额外常驻进程。
- 认证、SOCKS5 代理链、known_hosts 全部复用已保存的连接；转发不执行 shell 或 sudo，连接配置里的 `--sudo` 对转发没有影响。
- 一条连接可以同时开出多个隧道（数据库、缓存、后台各一个），互不影响。

## 用法

```sh
ah f NAME LOCAL_PORT TARGET
```

`LOCAL_PORT` 是本机监听地址（只写端口时默认 `127.0.0.1`），`TARGET` 是从 SSH 服务器侧访问的目标地址。按服务端口改一下即可：

| 服务 | 建隧道 | 客户端连接 |
| --- | --- | --- |
| MySQL / MariaDB | `ah f db 13306 3306 -d` | `mysql --protocol=TCP -h 127.0.0.1 -P 13306 -u app -p` |
| PostgreSQL | `ah f db 15432 5432 -d` | `psql -h 127.0.0.1 -p 15432 -U app dbname` |
| Redis | `ah f cache 16379 6379 -d` | `redis-cli -h 127.0.0.1 -p 16379` |
| Web 后台 / HTTP API | `ah f web 18080 80 -d` | 浏览器打开 `http://127.0.0.1:18080`，或 `curl http://127.0.0.1:18080/` |
| Elasticsearch / OpenSearch | `ah f es 19200 9200 -d` | `curl http://127.0.0.1:19200/_cluster/health` |
| 目标不在 SSH 主机上 | `ah f svc 19000 other-host:9000 -d` | 客户端指向 `127.0.0.1:19000` |

管理这些隧道：

```sh
ah f ls              # 查看 ID、监听地址、状态
ah f kill <ID>       # 停止；端口立刻不可用
ah f start <ID>      # 按原 ID 在后台重新启动
ah f restart <ID>    # 先停止再启动
ah f rm <ID>         # 删除停止状态的记录；运行中的用 f rm -f ID
```

`-d` 表示后台运行并输出 ID；不加则前台运行，Ctrl+C 关闭监听与 SSH 连接。停止是立即生效的——客户端会报连接被拒（MySQL `ERROR 2003 ... (111)`、Redis `Connection refused`），而不是静默挂起。

## 客户端一侧

| 事项 | 说明 |
| --- | --- |
| 本地端口选 1024 以上 | 例如 13306、16379、18080；1024 以下需要特权。 |
| **MySQL 必须写 `127.0.0.1`，不能写 `localhost`** | MySQL 客户端把 `localhost` 特判为「走 unix socket」，会绕过转发的端口。实测报错：`ERROR 2002 (HY000): Can't connect to local MySQL server through socket '/var/run/mysqld/mysqld.sock' (2)`。也可以用 `--protocol=TCP` 强制走 TCP。 |
| 其他客户端 | `psql`、`redis-cli` 只在显式指定 unix socket 时才走 socket，写 `127.0.0.1` 即走 TCP；浏览器天然只能访问本机端口。 |
| Web 场景的 Host 与证书 | 用 `127.0.0.1:18080` 访问时，请求的 `Host` 是 `127.0.0.1`。按域名分站的服务可能返回默认站点或 404，HTTPS 还会因证书域名不匹配报错。按域名访问：`curl http://127.0.0.1:18080/ -H 'Host: admin.internal'`，或 `curl --resolve admin.internal:18080:127.0.0.1 https://admin.internal:18080/`；浏览器则临时改 hosts。 |
| 数据库账号的 host 匹配 | 服务端看到的来源，是 **sshd 发起连接时使用的那个地址**。目标写 `127.0.0.1:3306`（服务与 sshd 同机）时来源就是 `127.0.0.1`，`'app'@'localhost'` 这类账号可用；目标写另一台主机时来源是 SSH 服务器自己的地址，需要 `'app'@'%'` 或对应网段的账号。 |
| 不能隧道 UDP | 通道只承载 TCP。DNS、WireGuard、部分游戏/语音协议这类 UDP 服务无法用转发。 |

## 故障判定：先看 `f ls`，不要先怀疑服务

目标不可达时，**客户端拿到的往往是个误导性的错误**（连接被重置、握手包读不到），真实原因只有 ah 知道。任何一次失败的连接之后，`f ls` 的 `error` 列会给出确切原因：

| `f ls` 里的 error | 含义 | 处理 |
| --- | --- | --- |
| `dial tcp 127.0.0.1:2222: connect: connection refused` | SSH 没连上（端口/主机不对，或 sshd 未运行） | 核对连接配置；`-d` 时这条会直接打印 |
| `ssh: rejected: administratively prohibited ("open failed")` | 远端 sshd 关闭了 TCP 转发 | 远端 SSH 配置里设 `AllowTcpForwarding yes` 后 `f start ID` |
| `ssh: rejected: connect failed ("Connection refused")` | 目标服务没在监听，或端口写错 | 在 SSH 服务器上确认服务与端口 |
| `ssh: rejected: connect failed ("Name does not resolve")` | 目标主机名在服务器侧解析不了 | 用服务器能解析的名字或 IP |

**注意顺序**：`AllowTcpForwarding no` 时 `f -d` 会成功返回 ID、`f ls` 立刻看还是 `running`——因为监听和 SSH 连接都正常，只有真正有客户端连进来才暴露。此时客户端报的是 MySQL `ERROR 2013 ... Lost connection to MySQL server at 'reading initial communication packet'` 这类"像服务本身有问题"的错误，很容易查错方向。**再跑一次 `f ls`**，状态会变成 `failed` 并带上原因。

## 远端需要满足的条件

只有两处，都不是「写脚本」：

1. **sshd 允许 TCP 转发**：`AllowTcpForwarding yes`。上游 OpenSSH 的编译默认是 yes，但发行版配置可能关掉——Alpine 的 `/etc/ssh/sshd_config` 就是 `AllowTcpForwarding no`。OpenSSH 对多数关键字取**第一个**出现的值，所以修改要替换原行，在文件末尾追加一条 `yes` 是无效的。
2. 该账号在服务器上能连到目标 `host:port`（防火墙规则、服务自身的 `bind-address`）。

另一个容易忽略的点：用 `adduser -D` 之类方式建出、**没有设置密码的锁定账号**会被 sshd 直接拒绝，公钥正确也不行，日志为 `User <name> not allowed because account is locked`。

**唯一需要远端配合的例外**：服务只监听 unix socket（MySQL 的 `skip-networking`、只绑 unix socket 的进程）时隧道无能为力——`direct-tcpip` 只连 TCP 端口。这种情况才需要在远端架桥接。

## 不用 ah 的等价写法

```sh
ssh -N -L 13306:127.0.0.1:3306 -L 16379:127.0.0.1:6379 alice@jump.example.com
```

写进 `~/.ssh/config` 更省事：

```
Host tunnels
  HostName jump.example.com
  User alice
  LocalForward 13306 127.0.0.1:3306
  LocalForward 16379 127.0.0.1:6379
  ExitOnForwardFailure yes
  ServerAliveInterval 30
```

用 ah 的差别：复用已保存连接的认证/代理链/known_hosts，能后台运行并管理生命周期；但**不自动重连，机器重启后也不会恢复**，需要常驻时用 autossh 或 launchd/systemd 拉起。

## 复现这个环境

下面这套在 macOS + Docker 上验证过：三个目标服务都不发布端口，只挂在 docker 内网，只能经隧道到达。实测结果：MySQL 查询、Redis 读写、HTTP 取到页面均为正常响应。

```sh
# 1. SSH 服务器：仅 stock openssh，加一行转发配置
mkdir -p /tmp/ah-lab && cd /tmp/ah-lab
ssh-keygen -t ed25519 -f ./id_ed25519 -N '' -q

cat > Dockerfile <<'EOF'
FROM alpine:3.20
RUN apk add --no-cache openssh
RUN adduser -D tester && echo 'tester:labpass' | chpasswd
COPY id_ed25519.pub /home/tester/.ssh/authorized_keys
RUN chown -R tester:tester /home/tester/.ssh \
 && chmod 700 /home/tester/.ssh && chmod 600 /home/tester/.ssh/authorized_keys \
 && ssh-keygen -A
RUN sed -i 's/^AllowTcpForwarding no/AllowTcpForwarding yes/' /etc/ssh/sshd_config \
 && grep -q '^AllowTcpForwarding yes' /etc/ssh/sshd_config
CMD ["/usr/sbin/sshd", "-D", "-e"]
EOF

# 2. 三个目标服务 + SSH 服务器，同一个网络，服务不发布端口
docker network create ah-lab
docker run -d --name ah-lab-mysql --network ah-lab \
  -e MYSQL_ROOT_PASSWORD=rootpass -e MYSQL_DATABASE=labdb \
  -e MYSQL_USER=app -e MYSQL_PASSWORD=apppass mysql:8.0
docker run -d --name ah-lab-redis --network ah-lab valkey/valkey:8.1
docker run -d --name ah-lab-web --network ah-lab alpine:3.20 sh -c \
  'apk add --no-cache busybox-extras >/dev/null && mkdir -p /srv \
   && echo "<h1>internal admin console</h1>" > /srv/index.html \
   && exec httpd -f -p 8080 -h /srv'
docker build -t ah-lab-sshd . && docker run -d --name ah-lab-sshd \
  --network ah-lab -p 2222:22 ah-lab-sshd
docker port ah-lab-mysql ah-lab-redis ah-lab-web   # 均为空：无法从本机直连

# 3. 一条连接，三个隧道（--config / --known-hosts 指向临时文件，不动真实配置）
#    写绝对路径：后台转发的记录会保存这些路径，换目录 restart 时才不会失效
ssh-keyscan -p 2222 127.0.0.1 > "$PWD/known_hosts" 2>/dev/null
AH="ah --config $PWD/connections.toml --known-hosts $PWD/known_hosts"
$AH new lab --host 127.0.0.1 --port 2222 --user tester --identity-file "$PWD/id_ed25519"
$AH f lab 13306 ah-lab-mysql:3306 -d
$AH f lab 16379 ah-lab-redis:6379 -d
$AH f lab 18080 ah-lab-web:8080 -d
$AH f ls

# 4. 各服务经隧道访问
mysql --protocol=TCP -h 127.0.0.1 -P 13306 -u app -p -D labdb -e 'SELECT VERSION()'
redis-cli -h 127.0.0.1 -p 16379 ping
curl -s http://127.0.0.1:18080/
```

本机没有这些客户端时，可以用容器里的客户端连宿主回环（在 OrbStack 上验证过 `host.docker.internal` 能到宿主的 `127.0.0.1`）：

```sh
docker run --rm -i -e MYSQL_PWD=apppass mysql:8.0 \
  mysql --protocol=TCP -h host.docker.internal -P 13306 -u app -D labdb -e 'SELECT VERSION()'
docker run --rm -i valkey/valkey:8.1 redis-cli -h host.docker.internal -p 16379 get greeted
```

确认流量确实走了隧道：让服务端告诉你它看到的连接来源。返回的是 **SSH 服务器的地址**，不是运行 ah 的那台机器。

```sql
SELECT SUBSTRING_INDEX(host,':',1) AS seen_from
  FROM information_schema.processlist WHERE id = CONNECTION_ID();
```

清理：`ah f kill <ID> && ah f rm <ID>`（逐个），再 `docker rm -f ah-lab-sshd ah-lab-mysql ah-lab-redis ah-lab-web && docker network rm ah-lab`。
