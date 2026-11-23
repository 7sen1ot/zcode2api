# 推理协议转换与限制

[返回 README](../README.md) · [配置与重试](configuration.md) · [代理与安全验证](network.md)

网关接受 `POST /v1/chat/completions`、`POST /v1/responses` 和 `POST /v1/messages`。前两者转换为 Anthropic Messages 请求。客户端声明的 function 和 custom 工具由客户端执行，Responses 内置的网页搜索交由上游执行。

| 协议 | 工具声明 | custom 原始输入 |
| --- | --- | --- |
| Chat Completions | `tools[].function`、`tools[].custom` | `tool_calls[].custom.input` |
| Responses | 顶层 `function`、`custom`、`web_search`，以及 `namespace` 内的 function 和 custom 工具 | `custom_tool_call.input` |

`namespace` 仅用于 Responses。custom 文本在上游通过对象的 `input` 字符串传递，返回时解包为原始文本。

网关按上游工具名排列未标记缓存断点的工具定义。每个显式 `cache_control` 断点保留在原工具上，排序仅在断点之间进行，以保留各段包含的工具并减少顺序变化对缓存前缀的影响。

## Responses 网页搜索

在顶层 `tools` 中声明 `{"type":"web_search"}` 即可请求实时文本搜索，也接受 `web_search_2025_08_26`。每次请求默认最多搜索 8 次，可用正整数 `max_tool_calls` 调低上限。仅查缓存、仅查索引或指定非文本搜索会返回请求错误。域名过滤可选 `filters.allowed_domains` 或 `filters.blocked_domains`，两者互斥。`user_location.type` 省略时默认为 `approximate`。[搜索参数转换](../internal/server/protocol/web_search.go)

响应中的 `web_search_call` 记录搜索状态和查询。需要来源列表时，发送 `"include":["web_search_call.action.sources"]`。正文引用独立放在对应 `output_text.annotations` 中。引用范围覆盖上游标注的整个文本块，索引从该块的 0 开始，按 Unicode 码点计数。流式和非流式响应使用相同的输出项结构。[输出项与引用](../internal/server/protocol/response_output.go)

## Anthropic 输入 Token 估算

`POST /v1/messages/count_tokens` 对经过网关模板和工具转换的请求做本地估算，在 JSON 整数字段 `input_tokens` 中返回结果，并带有响应头 `X-Zcode-Token-Count: estimated`。估算包括系统文本、消息、工具定义及工具结果，图片和文档按可获取的信息折算。当前工具调用轮次的可见思考计入估算，已结束轮次的思考及加密思考块跳过。此接口不发起上游推理，实际用量以推理响应的 `usage` 为准。[计数入口](../internal/server/gateway.go) · [估算规则](../internal/server/protocol/token_count.go)

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

可用 `ZCODE2API_GRAMMAR_PYTHON` 指定解释器，否则依次查找 `--root` 下的 `grammar_python/.venv`、PATH 中的 `python3` 和 `python`。Docker 构建时加入 `--build-arg GRAMMAR_RUNTIME=1` 安装运行时。

Chat 的语法位于 `custom.format.grammar.{syntax,definition}`，Responses 位于 `format.{syntax,definition}`。请求通过工具声明校验并进入推理时，网关设置响应头 `X-Zcode-Grammar-Validation: llguidance-post-generation`。

| 阶段 | 结果 |
| --- | --- |
| 检查声明 | 不支持的格式、空定义或超过 64 KiB 的定义返回 `400 unsupported_request` |
| 编译语法 | LLGuidance 拒绝定义或返回警告时，返回 `400 invalid_grammar` |
| 校验生成结果的全部 UTF-8 字节及最终接受状态 | 不匹配时非流式返回 `502 grammar_mismatch`；已开始的 SSE 保持 HTTP 200，通过 Chat `error` 或 Responses `response.failed` 结束 |
| 运行时缺失 / 校验超时 | 分别返回 `503 grammar_runtime_unavailable` / `504 grammar_validation_timeout`，不降级为只靠提示 |

单次编译或校验的 5 秒时限包括等待空闲进程名额的时间。最多同时运行 4 个校验进程，Linux 进程地址空间上限为 512 MiB。生成的工具输入超过 1 MiB 时返回 `grammar_input_too_large`。

流式响应可提前发送空输入的调用元信息，实际 custom 输入通过完整校验后才发送；失败时终止调用，不能把空元信息当作可执行输入。

这是**生成后的校验**，不约束上游逐 token 生成，失败后不自动重放已开始的请求。历史工具调用不按本轮 grammar 重新校验；客户端仍负责工具权限和执行安全。

## System / developer 指令

### 模板与去重

「设置 → 请求模板」默认使用 `main`。两种模板接收调用方指令的位置不同。

| 模板 | 上游 system 块 | 调用方文本的位置 |
| --- | --- | --- |
| `main` | 三个内置文本块 | 合并到第三块末尾 |
| `subagent` | 固定身份和可选正文，共两个文本块 | 合并到第二块末尾。四项正文默认关闭，此时只保留调用方提供的正文。 |

合并上游 system 文本块时可保留一致的 `cache_control`。传到合并步骤的非文本块、额外字段或相互冲突的缓存策略会返回请求错误。OpenAI developer 和中途指令经过角色转换，原块的缓存标记不会随指令文本转入新块。请求头 `X-Zcode-Template-Mode` 可覆盖全局模板，同时提供的 `X-Zcode-Session-Type` 须与所选模板一致。[模板实现](../internal/server/protocol/zcode_system.go) · [角色转换](../internal/server/protocol/instruction_context.go)

去重按已知完整内容匹配，兼容默认段落内的空白差异和身份末尾句号：

- 请求开头的明确 ZCode 身份前缀按已识别的旧版默认段落清理，遇到未知内容停止此前缀清理；已识别的内置默认段落、条目和旧身份仍可在自定义内容之后清理。
- 扩展区块保留额外文字，移除重复默认条目及清理后为空的标题。剩余客户端独有规则以 `Client` 标题区分，例如 `# Client Session-specific guidance`。
- 清理已识别的 Claude Code / Agent SDK 固定身份声明及 Fable 5 默认模型介绍；未知、自定义、改写文本和 Markdown 围栏示例保留。

| 位置 | 处理范围 |
| --- | --- |
| Anthropic 顶层 `system`、开头的 `system/developer`、Responses `instructions` | 清理已知默认内容，保留调用方指令 |
| Chat/Responses 中途 `system/developer` | scoped/history 模式清理重复默认内容，保留自定义指令和生效边界；空更新不再注入指令块。strict 模式先拒绝中途指令 |
| 用户消息、工具结果和 `tools` 定义 | 不扫描、不清理 |

### 角色与作用域

Anthropic 上游只有顶层 `system`，无法原生表达独立的 developer 角色或中途 system 更新，因此不能保证等价保留 OpenAI 的角色优先级、冲突处理和历史作用域。

Chat Completions 和 Responses 按以下顺序选择模式。优先读取请求头 `X-Zcode-Instruction-Mode`，其次是请求体 `instruction_mode`，再使用 Responses 父记录中的 `history` 模式、API 密钥的「OpenAI 指令模式」和默认的 `scoped`。

| 模式 | 行为 |
| --- | --- |
| `scoped`（默认） | developer 和中途 system 指令保留在顶层 system，标注来源角色和生效边界，对话中仅放位置标记。标记按首条转换指令的角色与内容稳定生成，避免相同请求因随机标记破坏缓存前缀。Responses `instructions` 按 developer 处理 |
| `history`（可选） | 开头的 system/developer 和 Responses 顶层 `instructions` 仍放入 system；中途 system/developer 在原位置转换为带来源角色和生效边界的 user 内容块。每次请求预置相同的系统桥接说明，使用固定标记和按源消息顺序生成的编号，避免首次或后续中途更新改写系统前缀 |
| `strict` | 拒绝需要角色或作用域近似转换的指令，返回 `400 unsupported_instruction_semantics`，不发送上游；开头的普通 system 可直接转换 |

scoped/history 均依赖模型理解角色和位置标记，可能误解或扩大指令范围，不能保证与原生角色优先级等价。scoped 发生近似转换时，响应附带：

```http
X-Zcode-Instruction-Mode: scoped-system
X-Zcode-Compatibility: instruction-roles-and-scope-approximated
```

history 模式返回 `X-Zcode-Instruction-Mode: history`，发生角色转换时同时返回上述 `X-Zcode-Compatibility` 提示头。

初始指令、工具定义和已有历史保持稳定时，history 模式追加中途指令不会改写顶层 system；初始指令或工具定义真实变化、切换指令模式仍可能影响缓存复用。该模式不保证具体缓存命中率。

原生 Anthropic Messages 不走角色桥接。Claude Code 不需要 OpenAI 协议特性时，可优先使用原生 Anthropic 模式。

## Responses 续聊与会话亲和

Responses 按以下优先级确定会话亲和标识：按既有规则识别的有效显式会话标识（`X-Session-Id` 优先，其次 `metadata.user_id`）→ `previous_response_id` 对应响应保存的标识 → 为首次请求生成的独立标识。首次标识结合当前密钥与网关生成的响应 ID，避免同一密钥的独立对话混用会话。

续聊及从同一父响应分出的分支默认继承父会话。显式会话标识可覆盖继承值，覆盖后的标识保存在子响应中，父响应保留原标识。`previous_response_id` 可恢复对话历史、会话身份及已保存的 `history` 模式。需要沿用顶层 `instructions` 时，在续聊请求中重新提交。

### 工具继承

在有工具定义且会保存响应的请求上发送 `X-Zcode-Reuse-Tools: true`，网关会把工具定义及当轮的 `tool_choice`、`parallel_tool_calls` 保存到响应记录。续聊省略 `tools` 时，网关默认继承父记录中已保存的工具定义，并填入本轮省略的工具选项。发生继承时，响应包含 `X-Zcode-Tools-Reused: true`。

续聊显式提交 `tools` 时以该字段为准，`tools: []` 可清空工具。发送 `X-Zcode-Reuse-Tools: false` 可跳过继承，并停止为本轮响应保存工具定义。

### 存储与调度

Responses 会话的账号粘性使用当前 `conversation_affinity_minutes`，设为 `0` 时关闭。绑定过期、账号不可用或并发满额时，调度器可以选择其他可用账号。成功后更新会话绑定。Chat Completions 和原生 Messages 在未识别到显式会话时使用密钥的 `sticky_minutes`。账号绑定全局最多保留 8,192 条，达到上限后优先淘汰最久未成功更新的绑定。

响应记录及其亲和标识仅保存在内存中，最多 100 条，按内容计量的总容量上限为 64 MiB。分支与续聊共享已有历史，继承的工具定义也共享存储。流式请求会在发送终态事件前处理保存。若单条响应连同历史和工具已超过 64 MiB，网关仍返回生成结果，但会跳过保存。`store: false` 也会跳过保存。[响应缓存](../internal/server/gateway.go)

同一密钥可用 `GET /v1/responses/{id}` 获取已保存的响应，用 `DELETE /v1/responses/{id}` 删除。未保存、已删除、被淘汰或服务重启前的 ID，以及属于其他密钥的 ID，均无法用于读取或续聊，此时返回 `404 response_not_found`。通过 `previous_response_id` 续聊可复用服务端保存的原生搜索和带签名的思考块。手动回传输出项时，搜索动作和可用链接转为历史文本，仅供展示的 `reasoning` 项会跳过。[历史转换](../internal/server/protocol/request.go)
