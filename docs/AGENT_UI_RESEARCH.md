# VisitReady Agent 交互调研

更新日期：2026-09-05

## 调研问题

VisitReady 不是通用聊天机器人。它把一段经过隐私检查的健康描述，转换为可核对的诊前沟通资料。因此界面必须清楚呈现四件事：对话线程、当前 Agent 运行、带依据的结构化产物，以及需要用户承担确认责任的节点。

本次调研以官方文档和项目仓库为主，研究的是交互契约，不照搬品牌视觉。

## 来源与可借鉴模式

| 系统 | 一手来源 | 成熟模式 | VisitReady 决策 |
| --- | --- | --- | --- |
| OpenAI Codex | [Codex 产品介绍](https://openai.com/codex/)与桌面产品 | 任务是可恢复的线程；工作过程从属于结果；Composer 是稳定的行动入口 | 使用 thread-first 框架、收拢的运行记录、固定 Composer 与可恢复历史 |
| AG-UI | [事件](https://docs.ag-ui.com/concepts/events)、[中断](https://docs.ag-ui.com/concepts/interrupts)、[工具](https://docs.ag-ui.com/concepts/tools) | 明确的 Run 生命周期、类型化工具活动、状态同步，以及带关联 ID 的 interrupt/resume | 将现有 `AgentEvent` 映射为生命周期记录；把补充信息和事实确认视为明确的人机中断；保留服务端校验 |
| assistant-ui | [官方仓库](https://github.com/assistant-ui/assistant-ui) | 可组合的 `Thread`、`Message`、`Composer`、`ThreadList`、`ActionBar`；重试、无障碍、附件和生成式 UI 属于 runtime 能力 | 在原生 HTML/CSS 中沿用同一概念模型，不为外观单独引入 React |
| LangGraph Agent Chat UI | [官方仓库](https://github.com/langchain-ai/agent-chat-ui) | 与线程绑定的运行、interrupt/resume、消息可见性控制，以及独立 artifact 表面 | 每个服务端会话对应一个 thread；内部细节默认收拢；诊前清单以 artifact 呈现，不伪装成聊天气泡 |
| CopilotKit | [官方仓库](https://github.com/CopilotKit/CopilotKit)与 [AG-UI 集成](https://docs.copilotkit.ai/) | 生成式 UI 与人工审批是类型化应用状态，而不是自然语言约定 | 从结构化事实渲染确认控件；最终动作由服务端检查的摘要版本约束 |
| Vercel AI SDK UI | [AI SDK UI 概览](https://ai-sdk.dev/docs/ai-sdk-ui/overview) | 消息、加载、错误、流式文本、流式对象和工具是独立状态通道 | 保持输入、会话、恢复三个错误区域与有界请求超时；未来可加入流式端点而不推翻视觉模型 |
| Open WebUI | [官方仓库](https://github.com/open-webui/open-webui) | 可搜索历史、可见工作流进度、排队交互、持久化 artifact、RBAC 与明确离线运行 | 借鉴紧凑历史和流程可见性；浏览器仍只存 session ID；没有鉴权前不暗示多用户持久化 |
| Perplexity | [产品](https://www.perplexity.ai/) | 来源紧邻结论，同时与生成内容保持视觉区分 | 可信资料与用户事实分区；只允许经过校验的 HTTPS 来源链接 |

## 医疗交互与安全分流调研

通用 Agent 框架解决“如何对话和恢复”，但不能替代医疗场景本身的安全设计。本轮额外参考以下一手资料：

| 来源 | 可借鉴模式 | VisitReady 决策 |
| --- | --- | --- |
| [NHS 111](https://www.nhs.uk/nhs-services/urgent-and-emergency-care-services/when-to-use-111/) | 用户通过症状问题逐步补充信息；更严重的症状优先处理；等待过程中若加重需要重新升级处理 | 追问按安全性和信息价值排序；明确红旗信号置顶；提示“正在发生或加重”时联系当地急救服务 |
| [MedlinePlus: Talking With Your Doctor](https://medlineplus.gov/ency/patientinstructions/000860.htm) | 就诊前整理症状、开始时间、变化、用药和要问的问题 | 输出症状画像、时间线、既有用药/过敏/检查资料、问题理由与行动清单 |
| [FDA Clinical Decision Support Software Guidance](https://www.fda.gov/regulatory-information/search-fda-guidance-documents/clinical-decision-support-software) | 高风险临床决策支持需要明确其使用边界与可复核依据 | 不输出诊断、概率或处方；风险提示只能来自用户原文证据，并由确定性规则二次筛查 |

这里没有把开源 symptom-checker 直接作为基础。GitHub 调研结果大多是小型演示项目，普遍把模型结论直接映射为疾病或分诊结果，缺少可核对证据、否定词处理和人工确认，不符合本项目边界。

## 能力模型：自适应诊前访谈

VisitReady 的产品定义从“单次摘要生成”升级为“自适应诊前访谈 + 安全提醒 + 就诊准备”。每轮都必须产生或更新以下结构：

1. 症状画像：症状名、起始时间、持续时长、频率、程度、规律、诱因、缓解因素与伴随表现。
2. 就诊时间线：只记录用户明确给出的时间与事件，并保留原文依据。
3. 安全提醒：仅对用户明确报告且通过规则筛查的红旗信号提示就医优先级，不输出病名或概率。
4. 自适应追问：已有信息不再问；按“安全 > 病程 > 频率/时长/影响 > 诱因 > 背景”排序，每问说明原因。
5. 行动清单：只建议记录数据、整理既有资料和准备沟通，不建议自行检查、治疗、停药或改剂量。

```text
用户脱敏描述
  -> LLM 结构化抽取
  -> 原文证据校验 + 事实去重
  -> 确定性红旗筛查
  -> 高价值信息缺口排序
  -> 人工补充中断
  -> 可信资料检索（可选、与个人事实分离）
  -> 个性化问题 + 行动清单
  -> 输出安全检查
  -> 逐项事实确认与导出
```

### 必须保持的语义边界

- “是否伴随胸痛或晕厥？”是待确认问题，不能显示为用户存在胸痛或晕厥。
- 风险提示必须附原文；含“没有、无、否认、未出现、不伴”等否定表达时不得触发。
- LLM 生成的风险 guidance 不直接展示，最终措辞由服务端固定模板决定。
- 检索资料只影响沟通问题和准备动作，永远不能写入患者事实。
- Demo 模式使用透明的确定性规则并显示“未调用 AI”，不能把词典规则冒充模型判断。

## 收敛后的 Agent 模型

这些系统实现不同，但共同收敛到以下模型：

```text
Thread
  -> Run（started / interrupted / completed / failed）
     -> Message parts
     -> Tool 或 workflow activity
     -> Structured artifact
     -> Human decision
  -> Next run 或 final export
```

VisitReady 已有的对应关系：

| UI 概念 | 当前实现 |
| --- | --- |
| Thread | `domain.Session`，以及 `sessionStorage` 中有数量上限的 session ID 列表 |
| Run | Eino 工作流生成的 `domain.AgentEvent` |
| Interrupt | `needs_clarification` 与 `awaiting_confirmation` 会话状态 |
| Artifact | 事实、就诊目标、问题、可信来源和 Markdown 导出 |
| Human decision | 逐项事实勾选与 `facts_digest`；Go API 校验被确认的事实版本，但不能证明用户确实阅读过 |
| Recovery | 在服务端 24 小时过期前，通过 `GET /api/v1/sessions/{id}` 恢复；MySQL 模式支持跨进程重启 |

## 本轮采纳

1. 对话是主导航模型；历史是安静的辅助栏，不做 dashboard。
2. Agent 活动以收拢的运行记录呈现，包含状态、数量和时间顺序。
3. 只有需要阅读或行动的结构化结果使用明显 artifact 表面。
4. 上下文与隐私状态靠近 Composer，和消费这些信息的动作放在一起。
5. 明确区分加载、追问、核对、完成、过期、超时和恢复状态。
6. 浏览器只持久化不透明 session ID；内容与过期时间以服务端为准。
7. 人工确认是新的显式动作。服务端摘要防止确认陈旧事实版本，但不是“用户已逐条阅读”的证明。
8. Demo 与 Live 模式必须不可混淆。

## 明确不采纳

- 不展示思维链。运行记录只报告动作和结果，不暴露隐藏推理。
- 不伪造 token streaming。当前 API 在完整工作流结束后返回，动画不能暗示不存在的流式输出。
- 不做跨会话全局健康记忆。健康文本应保持短生命周期。
- 不增加公开 thread 列表接口。没有身份认证时，全量索引会削弱 session capability 边界。
- 不迁移前端框架。assistant-ui 与 AI SDK 用于验证交互模型；React 重写不会提升当前 Go 内嵌页面的核心价值。
- 小屏不强制右侧 artifact 面板。内联 artifact 能保持阅读顺序，也不会压缩医疗文本。
- 不使用装饰性 AI 渐变、发光球或卡片阵列。它们会干扰临床任务层级。

## 后续架构升级

下一项真正重要的升级不是继续换皮，而是补充身份认证、持久化 thread 所有权，以及带 `run_id`、开始/结束/错误事件、断线游标和幂等 interrupt 响应的事件流端点。在此之前，VisitReady 应被描述为受保护的本地或受控演示，不是可直接公开的多用户 Agent 服务。

## 2026-09-05 能力升级：从追问次数转向信息状态

本轮不再把“第二轮够不够”理解为增加问题数量。参考 Codex、Claude、assistant-ui、AG-UI 与医疗访谈产品的共同模式，Agent 的核心价值被定义为：持续维护可恢复的任务状态，明确区分已知、未知和冲突，并根据下一条信息可能带来的变化选择问题。

新增的持久化产品契约：

1. `ConversationSummary`：每轮更新本次沟通意图、已明确细节和开放问题，用于恢复与 30 秒医生口述。
2. `Uncertainties`：用户未提供、跳过或明确表示不确定的信息，不再被模型压缩成确定事实。
3. `Contradictions`：同一症状同一字段出现不同说法时同时保留两段证据，并在生成最终摘要前优先澄清。
4. `InterviewState`：记录轮次、最大访谈预算、已明确和待处理数量，以及停止原因。
5. 最多四轮、每轮一至三问；回答完整时提前结束，明确跳过剩余问题时保留未知并结束，避免无休止追问。

视觉上用一条连续的访谈进度脊线承载这些状态，不为每项创建白色卡片。已明确内容是无框列表；未知使用低饱和暖纸色和左侧细线；冲突使用并排原话和单一澄清动作。状态不能只靠颜色表达，必须同时提供标题、证据和原因。
