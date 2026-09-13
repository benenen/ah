# ah 项目指南

本文件适用于整个仓库；`CLAUDE.md` 软链到此。默认用简体中文交流。

## 项目目标与现状

ah 使用 Go 对 SSH client 做一层封装，提供 CLI 管理连接；连接配置以 TOML 文件持久化。核心命令包括 `list`、`new`、`rm`、`edit`，以及支持本地/远端互传的 `cp` 和 SQLite 复制历史查询与重跑。输入源路径和目标路径时支持 Tab 补全，包括远程目录。

当前已实现 Go CLI、连接配置、SSH/SFTP、复制与补全。命令用法见 README.md，依赖版本以 go.mod 为准。本项目未要求接入 LLM。

## 工作准则

- 先检查现状，说明影响实现的假设；常规可逆选择自行推进，关键需求歧义再询问。
- 简单优先：围绕上述 CLI 实现，除已授权的 SQLite 复制历史外，不提前添加服务端、其他数据库、Web UI 或 LLM。
- 精确修改：保留用户改动，避免无关重构；未经授权不执行破坏性操作。
- 目标驱动：确定可验证结果，完成与改动相关的构建、检查和测试，如实报告未验证部分。
- 写或审 Go 代码前读 [代码检查清单](docs/agents-dot-md/code-checklist.md)。
- 改命令、配置、复制或补全行为前读 [功能契约](docs/agents-dot-md/requirements.md)；调整包边界时读 [架构](docs/agents-dot-md/architecture.md)；引入依赖或执行构建时读 [技术栈](docs/agents-dot-md/tech-stack.md)。
- SSH 主机密钥必须校验；凭据、私钥、真实连接配置不得写入仓库或日志。

<!-- CODEGRAPH_START -->
## CodeGraph

如果仓库根目录存在 `.codegraph/`，理解或定位代码时必须先使用 `codegraph_explore` MCP 工具或 `codegraph explore "<符号或问题>"`，再考虑 grep/find 或读取代码。可在查询中指定文件或符号以获取当前带行号源码。若工具为延迟加载，先按名称搜索加载。没有 `.codegraph/` 时跳过，不自动建立索引。
<!-- CODEGRAPH_END -->

## 技能整理与记忆沉淀

- 项目技能使用 `skills/<name>/SKILL.md`；全局技能不复制进仓库。
- 新增、删除或改名技能/模块，或修改技能描述后，运行 `bash docs/agents-dot-md/reindex.sh`。Git 仓库的技能索引只收录已跟踪文件。
- 索引标记内的内容由脚本生成，不手工维护。
- 可复用的项目经验写进对应模块，避免重复记录能从代码直接读取的事实；敏感信息只保留在仓库外的本地配置中。

## 项目技能索引
<!-- SKILLS:START -->
- （仓库内暂无 SKILL.md）
<!-- SKILLS:END -->

## 模块文档索引
<!-- MODULES:START -->
- [系统架构](docs/agents-dot-md/architecture.md) — 调整包边界或 SSH/SFTP 调用链前读取；以下是当前职责划分。
- [Go 代码检查清单](docs/agents-dot-md/code-checklist.md) — 写或审 Go 代码时读取；按改动适用项验证。
- [开发环境](docs/agents-dot-md/environment.md) — 配置测试环境、凭据或发布流程时读取。
- [功能契约](docs/agents-dot-md/requirements.md) — 实现命令、TOML 配置、SFTP 复制或路径补全时读取；区分用户需求与初始约定。
- [技术栈与验证](docs/agents-dot-md/tech-stack.md) — 引入依赖、实现 Go 代码或执行构建时读取。
<!-- MODULES:END -->
