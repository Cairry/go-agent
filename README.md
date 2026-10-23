# Mini Agent Go

一个用 Go 语言实现的完整Mini AI Agent 框架，支持工具调用、自主任务执行、持久记忆和异步任务等能力。代码和教程全程由 Claude Code 协作实现。

## 功能特性

- **Agent 主循环** — 工具调用驱动的自主执行，内置 Token 智能摘要防止上下文溢出
- **丰富工具集** — 文件读写、Bash 执行、网页抓取、持久记忆笔记、待办管理
- **HTTP API + SSE 流式响应** — 通过 HTTP 请求 Agent，实时返回思考、文本增量和工具调用事件
- **异步任务 + 定时任务** — 子 Agent 并发执行，Cron 定时触发，任务完成推送通知
- **技能系统** — 从 `.agent/skills/` 加载 SKILL.md，渐进式披露，无需重新编译
- **外部工具** — `.agent/tools/bin/` 预置任意语言实现的工具，YAML 定义接口
- **双 Provider 支持** — Anthropic Claude 和 OpenAI 兼容接口（DeepSeek、MiniMax 等）
- **全平台兼容** — 使用 `mvdan.cc/sh/v3` 纯 Go Shell，Windows/macOS/Linux 均支持

使用截图：
 <img src="docs/1.png">  
 <img src="docs/2.png">  

## 快速开始

### 1. 构建

```bash
git clone https://github.com/chuanyi/go-agent.git
cd go-agent
go mod tidy
go build -o go-agent ./cmd
# Windows: go build -o go-agent.exe ./cmd
```

### 2. 配置

```bash
cp .agent/config-example.yaml .agent/config.yaml
```

编辑 `.agent/config.yaml`：

```yaml
llm:
  api_key: "sk-ant-..."          # 你的 API Key
  model: "claude-sonnet-4-6"     # 模型名称
  provider: "anthropic"          # anthropic 或 openai

agent:
  max_steps: 200
  token_limit: 80000
  workspace_dir: "./workspace"
  system_prompt_path: "system_prompt.md"  # 可选，自定义系统提示

server:
  listen_addr: "127.0.0.1:8080"           # HTTP API 监听地址
```

### 3. 运行 HTTP API 服务

```bash
# 使用配置文件中的 workspace 和 server.listen_addr
./go-agent

# 指定工作目录
./go-agent /path/to/workspace

# 覆盖监听地址
./go-agent -listen 127.0.0.1:8080
```

默认监听 `127.0.0.1:8080`。发送消息时使用 SSE（Server-Sent Events）流式响应：

```bash
curl -N -X POST http://127.0.0.1:8080/api/chat \\
  -H 'Content-Type: application/json' \\
  -d '{"message":"列出当前目录的文件","session_id":"default"}'
```

主要接口：

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/healthz` | 健康检查 |
| `POST` | `/api/chat` | Agent 对话，SSE 流式返回 `content`、`thinking`、`tool_call`、`tool_result`、`done` 等事件 |
| `POST` | `/api/clear` | 清空会话历史，body 可传 `session_id` |
| `GET` | `/api/stats?session_id=default` | 获取会话统计 |

`/api/chat` 请求体中的 `session_id` 用于隔离多轮会话；不传时使用 `default` 会话。

## 配置详解

配置文件路径（按优先级）：
1. 可执行文件同级的 `.agent/config.yaml`（推荐）
2. `~/.mini-agent/config/config.yaml`
3. `config/config.yaml`

完整配置示例：

```yaml
llm:
  api_key: "YOUR_API_KEY"
  api_base: ""                   # OpenAI 兼容接口时填写，如 https://api.deepseek.com/v1
  model: "claude-sonnet-4-6"
  provider: "anthropic"          # anthropic | openai
  max_tokens: 8192               # 最大输出 tokens（0 = 使用默认值）
  temperature: -1                # 采样温度（-1 = 使用默认值）
  retry:
    enabled: true
    max_retries: 3
    initial_delay: 1.0

agent:
  max_steps: 200                 # 最大执行步数
  token_limit: 80000             # 触发摘要压缩的 token 阈值
  workspace_dir: "./workspace"   # Agent 工作目录
  system_prompt_path: "system_prompt.md"

task:
  max_workers: 3                 # 子 Agent 最大并发数

server:
  listen_addr: "127.0.0.1:8080" # HTTP API 监听地址

tools:
  skills_dir: ".agent/skills"       # Skill 目录
  mcp_config_path: "mcp.json"

```

## 工具列表

| 工具 | 说明 |
|------|------|
| `ls` | 列目录内容 |
| `read` | 读取文件（含行号） |
| `write` | 写入文件 |
| `edit` | 精确文本替换 |
| `multiedit` | 批量多处编辑 |
| `grep` | 内容搜索（ripgrep） |
| `find` | 文件名搜索（fd） |
| `bash` | 执行 Shell 命令（持久化 Session） |
| `record_note` | 写入持久记忆（`.agent/memory.json`） |
| `recall_note` | 检索持久记忆 |
| `web_fetch` | 抓取网页转 Markdown |
| `web_download` | 下载文件 |
| `todo` | 待办事项管理 |
| `task` | 创建/管理异步任务 |
| `cron` | 创建/管理定时任务 |
| `get_skill` | 按需加载技能内容 |

### API 流式事件

`POST /api/chat` 返回 `text/event-stream`。Agent 会先发送 `intent_checking`，完成意图识别和 Skill 路由后，才进入普通执行步骤。

常用事件：

- `intent_checking`：意图识别和 Skill 路由状态
- `step`：Agent 当前执行步数
- `thinking`：模型思考内容增量
- `content`：模型回复文本增量
- `tool_call` / `tool_result`：工具调用及结果
- `done`：本次请求完成
- `error`：执行失败

`intent_checking` 事件字段：

| 字段 | 说明 |
|------|------|
| `status` | `started`、`matched`、`understood` 或 `failed` |
| `skill` | 匹配到的 Skill 名称，没有匹配时为空 |
| `intent` | Agent 对用户意图的简要理解 |
| `content` | 面向用户的意图说明 |

同一个 `session_id` 会保留多轮对话历史，不同会话互相隔离；同一会话的请求会按顺序执行。

## 意图识别与技能路由

Agent 不会在收到请求后立即调用工具。只要项目加载了 Skill，Agent 会先执行一个**不提供任何工具的意图识别阶段**：

```text
用户请求
   │
   ▼
intent_checking: started
   │
   ├── 识别用户想做什么
   ├── 对比可用 Skill 的名称和描述
   └── 生成面向用户的理解说明
   │
   ├── matched    → 加载匹配的完整 SKILL.md
   ├── understood → 按普通流程处理
   └── failed     → 路由结果无效，停止本次请求
   │
   ▼
step → LLM → tool_call/tool_result → content
```

路由阶段不会提供 `write`、`bash` 等工具，因此 Skill 规则加载前不会发生文件写入或其他工具副作用。

### 匹配到 Skill 时

例如用户请求：

```text
创建一个 99乘法表.txt 文件
```

Agent 可能返回：

```text
event: intent_checking
data: {"type":"intent_checking","status":"matched","skill":"create-file","intent":"创建一个九九乘法表文件","content":"我理解你的请求是创建一个九九乘法表文件，将使用 create-file Skill。"}
```

随后 Agent 加载 `.agent/skills/create-file/SKILL.md`，按照该 Skill 的流程继续处理。对于要求二次确认的 Skill，首次请求只能询问确认，不能直接调用 `write`。

### 没有匹配 Skill 时

Agent 不会向用户提示“没有找到 Skill”，而是直接说明对请求的理解并继续执行：

```text
event: intent_checking
data: {"type":"intent_checking","status":"understood","skill":"","intent":"生成九九乘法表","content":"我理解你的请求是：生成九九乘法表，现在开始处理。"}
```

此时不会应用其他 Skill 的专属约束。

### 路由失败时

如果路由模型返回了无效 JSON，或者选择了不存在的 Skill，Agent 会发送 `status: failed` 并停止本次请求，不执行工具操作。这样可以避免在无法确认执行规则时产生副作用。

## 技能系统

在 `.agent/skills/` 目录创建 `SKILL.md` 文件，无需重新编译即可扩展 Agent 能力。启动时会扫描 Skill 元数据；每次请求先通过 `intent_checking` 进行意图识别，匹配后自动加载完整 Skill：

```
.agent/skills/
└── my-skill/
    ├── SKILL.md       # 技能定义（YAML Frontmatter + Markdown 内容）
    └── scripts/       # 辅助脚本（路径自动转为绝对路径）
```

SKILL.md 格式：

```markdown
---
name: my-skill
description: 简短描述，Agent 看到这个决定是否调用
allowed-tools:
  - bash
  - read
  - write
---

# 技能内容

详细的操作流程、最佳实践、检查项……
```

Skill 完整内容会在匹配后自动加载；`get_skill` 仍可用于显式读取某个 Skill 的完整说明。

## 外部工具

在 `.agent/tools/bin/` 放置可执行文件，配合 YAML 定义接口：

```
.agent/tools/bin/
├── gen_image.exe          # 任意语言实现的工具
└── gen_image.yaml         # 工具接口定义
```

YAML 定义示例（`.agent/tools/*.yaml`）：

```yaml
name: gen_image
description: 使用 AI 生成图片
binary: gen_image
parameters:
  type: object
  properties:
    prompt:
      type: string
      description: 图片描述
  required: [prompt]
```

工具二进制通过 stdin/stdout 以 JSON 格式交换数据。

## 项目结构

```
go-agent/
├── cmd/
│   └── main.go                  # 程序入口
├── internal/
│   ├── agent/                   # Agent 核心（主循环、摘要、回调）
│   ├── llm/                     # LLM 客户端（Anthropic SDK、OpenAI SDK）
│   ├── tools/                   # 工具系统（接口、注册表、各工具实现）
│   ├── task/                    # 异步任务与 Cron 调度
│   ├── api/                     # HTTP API 与 SSE 流式服务
│   ├── shell/                   # 持久化 Shell 会话管理
│   ├── schema/                  # 统一数据结构定义
│   ├── config/                  # 配置加载
│   ├── storage/                 # Todo 存储
│   └── utils/                   # 日志、Token 估算等工具函数
├── docs/
│   └── tutorial/                # 教程系列（9 章）
├── logs/                        # Agent 和 LLM 执行日志
└── .agent/                      # 运行时数据目录（放在可执行文件同级）
    ├── config.yaml              # 配置文件
    ├── config-example.yaml      # 配置示例
    ├── system_prompt.md         # 自定义系统提示（可选）
    ├── memory.json              # 持久记忆
    ├── skills/                  # 技能插件
    ├── tasks/                   # 任务持久化数据
    └── tools/
        └── bin/                 # 外部工具二进制
```

## 架构要点

### Token 智能摘要

当对话历史接近 token 上限时，自动触发压缩：从末尾保留最近 20K tokens，将更早的历史压缩成结构化摘要（Goal / Progress / Key Decisions / Current State / Next Steps）。摘要支持增量更新，长任务也不会因上下文溢出而中断。

### 子 Agent 隔离

异步任务由独立的 Agent 实例执行，有独立的对话历史和工具集。子 Agent 不注册 `task`/`cron` 工具，防止递归创建任务。

## 开发

```bash
# 编译验证
go build ./cmd

# 运行测试
go test ./...

# 运行特定包的测试
go test ./internal/tools -v
go test ./internal/agent -v
```

## 依赖

| 包 | 用途 |
|----|------|
| `anthropics/anthropic-sdk-go` | Anthropic 官方 Go SDK |
| `openai/openai-go` | OpenAI 官方 Go SDK |
| `mvdan.cc/sh/v3` | 纯 Go POSIX Shell（跨平台 bash 工具） |
| `aymanbagabas/go-udiff` | Unified diff（edit 工具） |
| `easychen/serverchan-sdk-golang` | Server酱通知推送 |
| `robfig/cron/v3` | Cron 表达式解析 |
| `JohannesKaufmann/html-to-markdown` | 网页转 Markdown |

## 教程

详见 [`docs/`](docs/README.md)——《用 Claude Code 打造你的智能体数字员工》系列教程，从零讲解如何一步步构建go-agent框架的每个模块。

<img src="docs/3.png">  

## License

MIT
