# ah

Go 编写的 SSH 连接管理 CLI，使用 TOML 保存命名连接。

支持交互选择连接、按标签批量执行命令、本地与远端文件互传、目录同步、端口转发和 SOCKS5 代理。文件操作支持备份、断点续传、路径 Tab 补全，以及可查询、可重跑的 SQLite 复制历史。

## 安装

从 [Releases](https://github.com/benenen/ah/releases) 下载对应平台的安装包，或在仓库根目录使用 Go 1.26+ 安装：

```sh
make install
```

需要将 Go 安装目录加入 PATH。平台支持和安装细节见 [安装文档](docs/installation.md)。

## 快速上手

创建一条连接，首次连接时核对并确认 SSH 主机指纹：

```sh
ah new dev --host dev.example.com --user alice --identity-file ~/.ssh/id_ed25519 --tag dev
ah c dev
```

常用操作：

```sh
ah ls                                      # 列出连接
ah pick                                    # 搜索并选择连接
ah c dev 'uname -a'                         # 执行远程命令
ah exec --tag dev --jobs 4 -- 'df -h'       # 按标签批量执行
ah cp ./report.csv dev:~/                   # 上传文件
ah sync --dry-run ./dist dev:/srv/app       # 预览目录同步
ah f dev 15432 5432 -d                      # 后台转发远端数据库端口
```

`cp` 默认拒绝覆盖已有文件；`sync` 默认保留目标多余文件，只有显式 `--delete` 才删除。更多用法见 [命令参考](docs/cli.md)。

## 文档

- [文档首页](docs/README.md)
- [安装](docs/installation.md)
- [完整命令与配置](docs/cli.md)
- [经 SSH 访问内网服务](docs/internal-services-over-ssh.md)
- [开发与验证](docs/development.md)

运行 `ah --help` 查看命令，`ah <command> --help` 查看参数。
