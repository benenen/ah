# 系统架构

> 调整包边界或 SSH/SFTP 调用链前读取；以下是当前职责划分。

现有目录：

- `cmd/ah/`：程序入口、退出码与信号处理。
- `internal/cli/`：命令参数、用户交互与输出。
- `internal/credentials/`：密码加解密、独立主密钥创建与读取。
- `internal/history/`：SQLite 历史持久化、状态更新与关键词查询。
- `internal/config/`：连接模型、TOML 编解码、校验与原子保存。
- `internal/sshclient/`：SSH 连接、认证、主机密钥校验、会话与 SFTP 生命周期。
- `internal/transfer/`：本地/远端源目标解析与流式复制。
- `internal/completion/`：连接名、本地与远程路径候选查询。

CLI 调用配置、SSH、复制和补全能力；底层包不依赖 CLI 框架。CLI 统一通过 sshclient 建立连接，再将 SFTP client 交给 transfer/completion，避免各自维护认证和主机校验逻辑。接口由消费方按测试或替换需求定义，不提前创建通用框架。

配置修改链路：加载 → 校验修改 → 安全保存 → 输出结果。
复制链路：持久化 running 历史 → 解析本地/远端路径 → 加载连接 → 分别连接 A/B → 打开源和目标 → 流式传输 → 检查写入及关闭结果 → 硬链接发布或 POSIX rename 替换目标 → 释放资源 → 独立限时 context 更新历史状态。
补全链路：解析当前参数 → 连接名候选或连接指定端点 → SFTP 读取父目录 → 前缀过滤 → shell 候选输出。
