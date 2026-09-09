# 系统架构与 Agent 工作流

## 1. 技术决策

- Go 单体服务，`net/http` 提供页面、API 和静态资源。
- CloudWeGo Eino `v0.9.19` 提供可编排 Graph；固定版本，答辩前不升级。
- 模型与搜索供应商通过小接口注入，测试使用本地假实现。
- 页面使用服务端 HTML 和原生 JavaScript，不需要 Node.js 构建环境。
- demo 使用带 TTL 的内存会话；live 使用 MySQL Store 保存账户绑定的加密会话与独立账户/令牌表。会话使用私有 DTO 保存待完成工作流，最终确认或紧急终止后清除原始输入与完整补充回答。

## 2. 组件

```text
Browser
  -> HTTP Handler
  -> Account Auth (email + Argon2id + HttpOnly cookie, live only)
  -> Session Service
  -> Agent Runner (Eino Graph)
       -> Input Guard (Go)
       -> Preflight Emergency Screen (Go, may terminate before LLM)
       -> Structured Extraction Node (LLM)
       -> Extraction Validator (Go)
       -> Evidence + Deduplication Guard (Go)
       -> Deterministic Risk Screen (Go)
       -> Emergency Escalation Node (Go, terminal)
       -> Prioritized Clarification Branch (LLM + human interrupt)
       -> Search Planner (Go)
       -> Trusted Search Tool (Bocha Web Search API)
       -> Visit Question + Action Plan Node (LLM)
       -> Generated Output Validator (Go)
       -> Output Guard (Go)
  -> Renderer / Exporter
```

## 3. 状态机

```text
NEW -------------------------------> EMERGENCY
 |                                      ^
 +-----------------> FAILED -----> EXTRACTING
                      | retry limit
                      +-----------> FAILED (terminal)
 v                                      |
EXTRACTING -> EXTRACTION_VALIDATOR -> VALIDATING
                    | high-value gap and rounds < 4
                    v
              WAITING_CLARIFICATION -> EXTRACTING
                    | newly reported current red flag
                    +-----------------------------> EMERGENCY
                    | complete, skipped, or round limit
                    v
               SEARCHING -> GENERATING -> OUTPUT_VALIDATOR -> OUTPUT_GUARD -> WAITING_REVIEW
                                                               |
                                                               v
                                                           COMPLETED
```

只有 Go 编排器可以改变状态。模型只能返回受限的结构化建议，不能直接执行网络、文件或系统操作。

提取或工作流发生短暂故障时，Runner 返回带相同随机 ID 的 `failed` 快照，而不是丢弃上下文。首次创建使用 `Create` 保存可重试快照，补充和重试使用 `ReplaceIfStatus` 的 status + revision CAS 原子写回；主动取消会在任何 Store 提交前停止，首次永久失败也不保存健康原文。恢复端点只接受可重试的 `failed` 状态。恢复会重新执行确定性校验和完整工作流，不跳过入口/出口 guard，最多三次处理尝试；永久失败与三次耗尽使用不同错误码。公开 `RunFailure` 只描述恢复能力，底层错误仍仅进入脱敏结构化日志。

## 4. 结构化智能与证据边界

- `SymptomProfile` 保存症状的开始时间、持续时长、频率、程度、规律、诱因、缓解因素与伴随表现；空字段只表示未知。`source_quote` 记录首次依据，`evidence_quotes` 记录后续追问中支持新增字段的完整用户原文。
- `TimelineEvent` 和 `RiskSignal` 都必须附带能在用户输入中精确定位的 `source_quote`。
- 事实先通过原文校验，再按规范化后的类别与引用去重；相同事实不会因模型重复输出而重复展示。
- 就诊目标由通过原文校验的症状名称确定性生成，不直接采用模型给出的疾病或诊断性标题。
- 风险提示不能只依赖模型判断。服务端只接受明确红旗词且不在否定、假设、不确定或纠正语境中的原文证据，并用固定的非诊断文案覆盖模型 guidance。后续“已缓解”只有明确指向同一症状或无歧义地回指该症状时，才能解除此前的红旗；其他症状缓解不能相互抵消。
- 首次描述和每轮补充回答都会在 LLM 前执行确定性紧急筛查。补充阶段的紧急旁路先于 keyed lock，但仅允许从 `waiting_clarification` 原子转换为独立 `emergency` 终态；核对中、已完成或其他状态不可由补充端点改写。命中后停止追问、搜索和后续模型调用；正在完成的旧请求在写回前会重新读取状态，不能覆盖紧急终态。会话仍可恢复和导出紧急安全摘要，不会被记成系统失败。
- `ClarificationPrompts` 按紧急、高、常规排序，每轮最多三问、最多四轮；回答充分时提前结束。未知值和冲突分别进入 `Uncertainties` 与 `Contradictions`，不静默覆盖原事实。
- `Question` 和 `ActionItem` 都包含原因与优先级。行动项再次经过医疗越界检查，禁止自行治疗、停药或改剂量。
- 独立 Validator 在两次 LLM 输出之后运行。患者事实、症状画像、时间线和风险信号必须有原文依据；生成的问题与行动项不是患者事实，因此不伪造 `source_quote`，而是检查问句意图、类别、PII 和医疗越界后过滤或降级为服务端固定内容。
- `ClarificationTurns` 是仅供服务端恢复指代关系的私有状态，HTTP JSON 明确不序列化。内存 Store 通过深拷贝保留它；MySQL Store 使用独立持久化 DTO，避免复用公开 API DTO 导致上下文丢失或意外暴露。

## 5. 联网搜索边界

- 搜索不是自由浏览器，只调用实现 `SearchClient` 的受控 API。
- 允许域名由服务器配置，默认包含 `who.int`、`nhc.gov.cn`、`gov.cn`、`medlineplus.gov`、`cdc.gov` 和 `fda.gov`。
- 查询由已提取的通用医学术语生成，不包含用户姓名、联系方式、地址、证件号、完整叙述或检查单编号。
- 搜索结果再次校验最终 URL 的主机名，重定向后的非允许域名不得进入模型上下文。
- 搜索内容只用于通俗解释和准备问题，不进入“患者事实”区域。

## 6. 关键接口

```go
type LLMClient interface {
    Extract(ctx context.Context, input string) (domain.Extraction, error)
    GenerateQuestions(ctx context.Context, input domain.QuestionInput) (domain.QuestionSet, error)
}

type SearchClient interface {
    Search(ctx context.Context, query string, allowedDomains []string) ([]domain.Source, error)
}

type SessionStore interface {
    Create(session domain.Session) error
    Get(id string) (domain.Session, error)
    Replace(session domain.Session) error
	ReplaceIfStatus(session domain.Session, expected domain.AgentStatus) (domain.Session, error)
	Delete(id string) error
    DeleteExpired(now time.Time) int
}
```

内存与 MySQL 实现还提供 `CreateContext`、`ReplaceContext`、`ReplaceIfStatusContext` 和 `DeleteContext`。HTTP 层优先使用这些接口，把客户端取消和请求截止时间传递到真正的存储提交；旧的无上下文方法保留给后台清理和兼容调用，并委托到相同实现。

## 7. 可观测性

- 每个请求生成随机 `request_id`。
- 记录节点名、状态、耗时、结果数量和错误分类。
- 不记录用户输入、补充回答、搜索查询、来源正文或模型响应。
- 访问日志将会话路径统一模板化为 `/api/v1/sessions/:id/...`，未知后缀也不会原样写入；上游响应读取到 EOF、提前关闭或失败均有独立终态日志。
- 模型调用只重试明确的瞬态故障（超时、临时 DNS、连接中断、EOF，以及 429/500/502/503/504）；证书校验失败、永久 DNS 不存在和其他配置类网络错误不会重试。
- UI 展示动作事件，不展示模型隐藏推理过程。
