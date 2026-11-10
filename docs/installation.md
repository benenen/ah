# 安装

[文档首页](README.md) · [命令参考](cli.md) · [开发与验证](development.md)

## 下载发布版

从 [Releases](https://github.com/benenen/ah/releases) 下载对应系统和架构的安装包。Linux/macOS 为 `.tar.gz`，Windows 为 `.zip`，支持 amd64 和 arm64。

例如，解压 Linux amd64 的 v0.3.0 安装包：

```sh
tar -xzf ah_0.3.0_linux_amd64.tar.gz
```

将解压后的 `ah`（Windows 为 `ah.exe`）放入 PATH 中的目录。安装包包含 `LICENSE`，同一发布页的 `checksums.txt` 提供全部安装包的 SHA-256。下载对应文件后可核对：

```sh
# Linux
sha256sum -c checksums.txt --ignore-missing
# macOS：计算已下载文件的摘要，与 checksums.txt 中对应行比较
shasum -a 256 ah_0.3.0_darwin_arm64.tar.gz
```

## 从源码安装

需要 Go 1.26 或更新版本。从仓库根目录执行：

```sh
make install
# 等价于：
go install ./cmd/ah
```

安装目录优先使用 `go env GOBIN`；未设置时使用 `$(go env GOPATH)/bin`，请将实际目录加入 PATH。仅构建到仓库内可用：

```sh
make build
./bin/ah --help
```

## 检查安装

```sh
ah version
ah --help
```

发布版的版本信息包含版本号、commit、构建时间和运行平台；普通源码构建显示 dev。报告问题时可附上 `ah version` 输出。

## 平台支持

交互 SSH、远程命令、`pick` 和 `exec` 支持 macOS/Linux。Windows 产物可执行连接配置管理、`cp`、`history`、`version` 等操作；交互登录与远程命令、交互密码输入、主机信任确认和后台转发不受支持。

安装后按 [快速上手](../README.md#快速上手) 创建连接；补全脚本的加载与持久化配置见 [Tab 补全](cli.md#tab-补全)。
