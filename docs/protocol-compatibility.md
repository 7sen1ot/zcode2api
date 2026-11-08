# OpenAI 协议转换与限制

[返回 README](../README.md) · [配置与重试](configuration.md) · [代理与安全验证](network.md)

网关转换工具声明、调用历史、工具选择、结果和 SSE，工具执行仍由客户端负责。

| 协议 | 工具声明 | custom 原始输入 |
| --- | --- | --- |
| Chat Completions | `tools[].function`、`tools[].custom` | `tool_calls[].custom.input` |
| Responses | 平铺的 `function`、`custom`，以及 `namespace` 内的这两类工具 | `custom_tool_call.input` |

`namespace` 仅用于 Responses。custom 文本在上游通过对象的 `input` 字符串传递，返回时解包为原始文本。

## Grammar 实际校验

使用 [LLGuidance](https://github.com/guidance-ai/llguidance) 校验 OpenAI 的 Lark 方言及 Rust regex 语法。仅带 grammar 的 custom 工具需要 Python 3.10+ 与 llguidance，普通 custom 和 function 不需要。

在项目或发行包目录安装：

```powershell
python -m venv grammar_python/.venv
grammar_python/.venv/Scripts/python.exe -m pip install --only-binary=:all: -r grammar_python/requirements.txt
```

Linux / macOS：

```sh
python3 -m venv grammar_python/.venv
grammar_python/.venv/bin/python -m pip install --only-binary=:all: -r grammar_python/requirements.txt
```

可用 `ZCODE2API_GRAMMAR_PYTHON` 指定解释器，否则依次查找 `--root` 下的 `grammar_python/.venv`、PATH 中的 `python3` 和 `python`。Docker 构建时加入 `GRAMMAR_RUNTIME=1` 安装运行时。

Chat 的语法位于 `custom.format.grammar.{syntax,definition}`，Responses 位于 `format.{syntax,definition}`。响应附带 `X-Zcode-Grammar-Validation: llguidance-post-generation`。

| 阶段 | 结果 |
| --- | --- |
| 请求进入时编译语法 | 无效、不支持或无法完整解释时返回 `400 invalid_grammar` |
| 校验生成结果的全部 UTF-8 字节及最终接受状态 | 不匹配时非流式返回 `502 grammar_mismatch`；已开始的 SSE 保持 HTTP 200，通过 Chat `error` 或 Responses `response.failed` 结束 |
| 运行时缺失 / 校验超时 | 分别返回 `503 grammar_runtime_unavailable` / `504 grammar_validation_timeout`，不降级为只靠提示 |

grammar 定义最多 64 KiB，输入最多 1 MiB；单次编译或校验限时 5 秒，最多同时运行 4 个校验进程，Linux 进程地址空间限制为 512 MiB。

流式响应可提前发送空输入的调用元信息，实际 custom 输入通过完整校验后才发送；失败时终止调用，不能把空元信息当作可执行输入。

这是**生成后的校验**，不约束上游逐 token 生成，失败后不自动重放已开始的请求。历史工具调用不按本轮 grammar 重新校验；客户端仍负责工具权限和执行安全。

## System / developer 指令

### 模板与去重

网关注入 ZCode 3.17.2 所需的三段模板，将调用方文本指令合并到第三段，保持恰好三个系统块；缺少模板或改变结构可能导致上游返回 3012。合并保留一致的 `cache_control`，拒绝非文本块、额外字段或冲突的缓存策略。

去重按已知完整内容匹配，兼容默认段落内的空白差异和身份末尾句号：

- 请求开头的明确 ZCode 身份前缀按已识别的旧版默认段落清理，遇到未知内容停止此前缀清理；与当前注入模板一致的段落、条目和旧身份仍可在自定义内容之后清理。
- 扩展区块保留额外文字，移除重复默认条目及清理后为空的标题。剩余客户端独有规则以 `Client` 标题区分，例如 `# Client Session-specific guidance`。
- 清理已识别的 Claude Code / Agent SDK 固定身份声明及 Fable 5 默认模型介绍；未知、自定义、改写文本和 Markdown 围栏示例保留。

| 位置 | 处理范围 |
| --- | --- |
| Anthropic 顶层 `system`、开头的 `system/developer`、Responses `instructions` | 清理已知默认内容，保留调用方指令 |
| Chat/Responses 中途 `system/developer` | scoped 模式清理重复默认内容，保留自定义指令和生效边界；空更新不再注入指令块。strict 模式先拒绝中途指令 |
| 用户消息、工具结果和 `tools` 定义 | 不扫描、不清理 |

### 角色与作用域

Anthropic 上游只有顶层 `system`，无法原生表达独立的 developer 角色或中途 system 更新，因此不能保证等价保留 OpenAI 的角色优先级、冲突处理和历史作用域。

通过请求体 `instruction_mode` 或请求头 `X-Zcode-Instruction-Mode` 选择模式，请求头优先：

| 模式 | 行为 |
| --- | --- |
| `scoped`（默认） | developer 和中途 system 指令保留在顶层 system，标注来源角色和生效边界，对话中仅放位置标记。标记按首条转换指令的角色与内容稳定生成，避免相同请求因随机标记破坏缓存前缀。Responses `instructions` 按 developer 处理 |
| `strict` | 拒绝需要角色或作用域近似转换的指令，返回 `400 unsupported_instruction_semantics`，不发送上游；开头的普通 system 可直接转换 |

scoped 依赖模型理解角色和位置标记，可能误解或扩大指令范围；发生近似转换时，响应附带：

```http
X-Zcode-Instruction-Mode: scoped-system
X-Zcode-Compatibility: instruction-roles-and-scope-approximated
```

原生 Anthropic Messages 不走角色桥接。Claude Code 不需要 OpenAI 协议特性时，可优先使用原生 Anthropic 模式。
