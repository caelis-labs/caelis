# Caelis

**让 AI Agent 协同工作的工作空间。**

[![最新版本](https://img.shields.io/github/v/release/caelis-labs/caelis)](https://github.com/caelis-labs/caelis/releases/latest)
[![质量检查](https://github.com/caelis-labs/caelis/actions/workflows/quality.yml/badge.svg)](https://github.com/caelis-labs/caelis/actions/workflows/quality.yml)
[![npm](https://img.shields.io/npm/v/@caelis/caelis)](https://www.npmjs.com/package/@caelis/caelis)

[English](README.md) · [简体中文](README.zh-CN.md)

任何兼容 [ACP（Agent Client Protocol）](https://agentclientprotocol.com/) 的 Agent，
都可以加入同一个 Caelis 协作网络。内置 Runtime、原生协作 Agent 和外部 ACP Agent
以 **Participant（参与者）** 的身份，通过统一的 mailbox 和消息语义协同工作，
而不只是各自执行一次子任务、向主代理汇报结果。

你可以用 Caelis 理解仓库、实现功能、运行测试和审查代码，也可以让多个 Agent 分工协作。
参与者能够发现彼此、互发消息、保留各自的对话，并在收到新输入后继续工作。
你可以在终端工作空间中查看进展、直接发送输入，也可以通过单次命令或 ACP 客户端使用 Caelis。

当前协作网络限定在本地 Caelis 的一个 Session（会话）内，不是托管服务，也不跨会话路由消息。
外部 Agent 通过 ACP stdio 接入；可用的协作能力取决于它对 MCP 工具注入、运行中输入
（steering）和历史加载的支持。

[官网](https://caelis.dev) · [版本下载](https://github.com/caelis-labs/caelis/releases) · [文档](#文档)

## 快速开始

在 macOS 或 Linux 上安装，然后进入你的项目：

```bash
curl -fsSL https://caelis.dev/install.sh | sh
cd /path/to/your/project
caelis
```

首次启动后：

1. 输入 `/connect`，通过 ChatGPT Codex 登录、配置 API 或本地模型服务，或连接 ACP Agent。
2. 输入 `/model`，选择主 Agent 使用的模型和推理强度。
3. 描述一个具体目标，例如：`梳理这个仓库的结构，告诉我如何运行测试。`

启动目录就是工作空间。会话保存在本地，可通过 `/resume` 继续之前的工作。

### 其他安装方式

| 方式 | 命令 |
| --- | --- |
| Windows PowerShell | `irm https://caelis.dev/install.ps1 \| iex` |
| npm | `npm install -g @caelis/caelis` |
| 免全局安装运行 | `npx @caelis/caelis --help` |
| 从源码构建 | `git clone https://github.com/caelis-labs/caelis.git && cd caelis && make install` |

发布的二进制支持 macOS、Linux 和 Windows 的 x64、ARM64 平台。
从源码构建需要 [`go.mod`](go.mod) 中声明的 Go 版本。

## 与 Participant 协作

可以先使用当前模型启动原生参与者，也可以搭配其他模型和 ACP Agent：

1. 通过 `/connect` 添加所需的模型服务或 ACP Agent。外部 Agent 的可执行程序需要单独安装，
   并位于 Host 进程的 `PATH` 中；其他 ACP stdio 命令可通过 **Custom** 接入。
2. 输入 `/team`，在团队面板中配置 `breeze`、`orbit`、`zenith` 等参与者角色，或创建自定义角色。
3. 请主 Agent 组织协作，例如：

   ```text
   请让两个参与者一起审查这次改动：一个检查正确性，一个检查测试覆盖。
   让它们交流相关发现，最后汇总问题和修复建议。暂时不要修改文件。
   ```

主控 Agent 通过 `StartThread` 创建参与者对话，用 `ReadThread` 和 `WaitThread`
观察公开结果。所有参与者通过 `ListThreads` 发现协作者，通过 `SendMessage` 发送消息，
共用 Control 管理的 mailbox 服务。消息入队不等于已经送达；协作能力也不意味着参与者
可以自行编排其他 Agent 或转移控制权。

点击对话中的参与者链接或底栏的运行中／已完成数量，即可打开对应工作区。
你可以切换参与者、选择浮层或分屏，并直接发送文字或图片，不必替换主对话。
**F6** 切换输入框焦点，**F7** 显示或隐藏面板，隐藏不会停止任务。

详细配置与操作见 [Participant 使用指南](docs/participants.md)；
能力要求和消息投递保证见 [外部 ACP Agent](docs/external-acp-agents.md)。

## 更多能力

- **仓库工具：** 查看和编辑文件、搜索代码、执行命令，工具请求和审批过程可见。
- **模型选择：** 在同一个模型选择器中使用 ChatGPT Codex 登录、API 服务、本地模型或 ACP Agent。
- **工作空间扩展：** 支持 MCP Server、Skill 和插件；项目级 MCP 配置需要先获得工作空间信任。
- **持久会话与记忆：** 恢复历史对话，使用内置的 `Remember` 和 `Recall`。
  记忆功能无需单独安装；除非在 `/team` 中显式绑定 Memory Steward，否则不会为记忆调用模型。
- **交互与自动化：** 提供 TUI、文本、版本化 JSON、流式 JSONL 和 ACP Server，
  使用相同的 Session 与 Control 服务。

## 常用命令

| 目标 | 命令 |
| --- | --- |
| 启动 TUI | `caelis` |
| 打开独立 Bot TUI | `caelis bot` |
| 执行一次提示 | `caelis -p "概述这个仓库。"` |
| 返回结构化结果 | `caelis -p "审查这些改动。" -format json` |
| 流式输出 ACP Envelope | `caelis -p "运行测试。" -format jsonl` |
| 从标准输入读取提示 | `printf '%s\n' "解释这段代码。" \| caelis -format text` |
| 作为 ACP Server 提供服务 | `caelis acp` |
| 修复已知兼容性数据并检查健康状态 | `caelis doctor` |
| 查看托管本地 Host 的状态 | `caelis service status` |
| 查看全部选项 | `caelis -h` |

使用 `-session` 指定持久会话，`-store-dir` 指定数据目录，`-control-url` 连接指定 Host，
或用 `-embedded` 显式启用单进程运行。

## 安全与本地数据

Caelis 默认使用 `auto-review` 模式，由 Guardian 审查工具请求；无法得出有效决策时拒绝执行。
如果希望逐项自行审批，使用 `/mode manual`。外部 Agent 保留自身的执行能力，
Caelis 处理它们通过 ACP 暴露的权限请求。

ChatGPT 订阅登录使用社区兼容的 Codex OAuth 流程，而非 OpenAI 文档承诺的第三方集成。
浏览器或设备登录后的刷新凭证保存在所选 Store 中，文件权限仅向当前用户开放。

正式版将会话和凭证保存在 `~/.caelis`，开发版默认使用 `~/.caelis-dev/default`。
凭证文件仅向当前用户开放。`-store-dir` 修改的是数据目录，不是工作空间。
模型请求会发送给你选择的服务商或外部 Agent；本地存储不代表离线推理。

托管本地 Host 启动失败时，会给出稳定的 `CAELIS_STARTUP_*` 错误码。
其中 `CAELIS_STARTUP_WORKSPACE_IDENTITY_CONFLICT` 可通过 `caelis doctor` 修复；
正常启动不会改写持久会话数据。

## 文档

以下详细文档目前以英文维护：

- [Participants](docs/participants.md)：配置协作者并使用参与者工作区。
- [外部 ACP Agent](docs/external-acp-agents.md)：连接、能力和消息契约。
- [Agent SDK](agent-sdk/README.md)：嵌入或扩展可复用的 Go Runtime。
- [架构](docs/architecture.md)：代码归属与依赖边界。
- [测试](docs/testing.md)：默认检查与按影响范围选择的验证。
- [发布](docs/release.md)：官方产物的发布与验收流程。

## 参与开发

```bash
make install
make commit-check
```

`make commit-check` 检查 Go 格式和 diff 空白。修改代码时运行相关测试，
完整门禁由 PR CI 执行；需要本地全量验证时运行 `make quality`。
按影响范围选择的检查及平台验证见 [测试文档](docs/testing.md)。

## 许可证

[Apache-2.0](LICENSE)。
