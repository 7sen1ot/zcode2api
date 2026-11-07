# OpenAI 协议转换与限制

Chat Completions 支持 `tools[].function` 和 `tools[].custom`；Responses 支持平铺的 `function`、`custom` 以及 `namespace` 内的这两类工具。工具调用仍由客户端执行，网关负责声明、调用历史、工具选择、返回结果和 SSE 的双向转换。`namespace` 不是 Chat Completions 的标准工具类型。

Chat custom 的原始文本位于 `tool_calls[].custom.input`；Responses 位于 `custom_tool_call.input`。网关在上游使用对象中的 `input` 字符串承载文本，返回时解包，不把整个 JSON 对象当作原始输入。

## Grammar 实际校验

使用 [LLGuidance](https://github.com/guidance-ai/llguidance) 校验 OpenAI 使用的 Lark 方言和 Rust regex 语法，不使用 Python Lark 或其他正则方言替代。Python 运行时是 grammar 工具的可选依赖；普通文本 custom 和 function 工具不需要它。

在项目或发行包目录安装：

```powershell
python -m venv grammar_python/.venv
grammar_python/.venv/Scripts/python.exe -m pip install --only-binary=:all: -r grammar_python/requirements.txt
```

Linux：

```sh
python3 -m venv grammar_python/.venv
grammar_python/.venv/bin/python -m pip install --only-binary=:all: -r grammar_python/requirements.txt
```

需要 Python 3.10 或更高版本。默认优先查找 `--root` 目录下的 `grammar_python/.venv`，然后查找 PATH 中的 `python3`、`python`。也可通过 `ZCODE2API_GRAMMAR_PYTHON` 指定解释器路径。Docker 构建参数 `GRAMMAR_RUNTIME=1` 会安装依赖。

- 请求进入时编译 grammar；无效、不支持或无法完整解释的语法返回 `400 invalid_grammar`。
- 上游生成完整 custom 输入后，校验所有 UTF-8 字节及最终接受状态；不匹配时非流式返回 `502 grammar_mismatch`，不把无效输入返回给客户端。已经开始的 SSE 保持 HTTP 200，通过 Chat 的 `error` 或 Responses 的 `response.failed` 报告该错误并结束。
- 缺少运行时返回 `503 grammar_runtime_unavailable`，校验超时返回 `504 grammar_validation_timeout`。不会自动降级为只靠提示。
- grammar 定义最多 64 KiB，校验输入最多 1 MiB；单次编译或校验限时 5 秒，同时最多运行 4 个校验进程，Linux 校验进程地址空间限制为 512 MiB。
- 流式响应只提前发送空输入的调用元信息；custom 的实际输入在完成验证后才发送。不匹配时终止该调用，返回协议错误，不能把已收到的空元信息当作可执行调用。

这是**生成后的语法校验**，并非上游逐 token 约束生成。模型仍可能生成不合规输入并导致失败；网关不会为这类失败自动重放已开始的请求。语法合法也不代表操作安全，客户端仍负责工具权限和执行检查。旧工具调用历史只作对话记录，不用本轮可能已更换的 grammar 重新判定旧调用。

Chat 格式将语法放在 `custom.format.grammar.{syntax,definition}`；Responses 放在 `format.{syntax,definition}`。使用 grammar 的响应附带 `X-Zcode-Grammar-Validation: llguidance-post-generation`。

## System / developer 指令

网关不注入 ZCode 固定模板。请求开头存在明确的 ZCode 身份前缀时，按已识别的 ZCode 3.17.2 完整段落指纹清理默认模板；遇到未知或自定义内容就停止这部分前缀清理。另对 ZCode 身份、`You are an agent working with the user toward their goals…` 身份段落，以及 `IMPORTANT: Assist with authorized security testing…` 完整默认段落清理所有重复项，即使它们出现在未知内容之后；兼容身份末尾的句号和段落内换行，保留 Markdown 代码围栏中的示例。只处理顶层 `system`、开头的 `system/developer` 消息和 `instructions`，不扫描用户消息、工具结果或 `tools` 定义。未知、自定义或改写过的内容未命中指纹时原样保留。

Anthropic 上游只有顶层 `system`，没有独立的 `developer` 角色，也不能原生表达对话中途的 `system` 更新。不能保证与 OpenAI 的角色优先级、冲突处理和历史作用域完全等价。

默认 `instruction_mode=scoped`：原始 developer 指令和中途 system 指令放入顶层 system，显式标注来源角色及生效边界；对话中只保留随机生成的位置标记，不把高优先级指令正文降为用户消息。Responses 的 `instructions` 按 developer 指令处理。

此模式要求模型理解位置标记与角色优先级，仍属于近似转换；不同模型可能忽略、误解或过度扩大指令范围。响应明确附带：

```http
X-Zcode-Instruction-Mode: scoped-system
X-Zcode-Compatibility: instruction-roles-and-scope-approximated
```

需要拒绝这种降级时，发送请求头 `X-Zcode-Instruction-Mode: strict`，或在 JSON 请求体中设置 `"instruction_mode": "strict"`；请求头优先。严格模式遇到 developer、Responses instructions 或对话中途的 system，直接返回 `400 unsupported_instruction_semantics`，不会发送给上游。对话开头的普通 system 可以直接转换。严格模式避免静默降级，不代表网关或模型能保证所有 OpenAI 行为完全一致。

原生 Anthropic Messages 请求不走上述角色桥接。使用 Claude Code 且不需要 OpenAI 协议特性时，优先选择原生 Anthropic 模式，可减少转换差异。
