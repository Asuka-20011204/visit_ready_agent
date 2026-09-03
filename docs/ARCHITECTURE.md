# 系统架构与 Agent 工作流

## 1. 技术决策

- Go 单体服务，`net/http` 提供页面、API 和静态资源。
- CloudWeGo Eino `v0.9.19` 提供可编排 Graph；固定版本，答辩前不升级。
- 模型与搜索供应商通过小接口注入，测试使用本地假实现。
- 页面使用服务端 HTML 和原生 JavaScript，不需要 Node.js 构建环境。
- 默认仅使用带 TTL 的内存会话，不保存健康信息到磁盘。

## 2. 组件

```text
Browser
  -> HTTP Handler
  -> Session Service
  -> Agent Runner (Eino Graph)
       -> Input Guard (Go)
       -> Extraction Node (LLM)
       -> Evidence Guard (Go)
       -> Clarification Branch (LLM + human interrupt)
       -> Search Planner (Go)
       -> Trusted Search Tool (Tavily)
       -> Question Node (LLM)
       -> Output Guard (Go)
  -> Renderer / Exporter
```

## 3. 状态机

```text
NEW -> EXTRACTING -> VALIDATING
                    | missing
                    v
              WAITING_CLARIFICATION -> EXTRACTING
                    | complete
                    v
               SEARCHING -> GENERATING -> WAITING_REVIEW
                                              |
                                              v
                                          COMPLETED
```

只有 Go 编排器可以改变状态。模型只能返回受限的结构化建议，不能直接执行网络、文件或系统操作。

## 4. 联网搜索边界

- 搜索不是自由浏览器，只调用实现 `SearchClient` 的受控 API。
- 允许域名由服务器配置，默认包含 `who.int`、`nhc.gov.cn`、`gov.cn`、`medlineplus.gov`、`cdc.gov` 和 `fda.gov`。
- 查询由已提取的通用医学术语生成，不包含用户姓名、联系方式、地址、证件号、完整叙述或检查单编号。
- 搜索结果再次校验最终 URL 的主机名，重定向后的非允许域名不得进入模型上下文。
- 搜索内容只用于通俗解释和准备问题，不进入“患者事实”区域。

## 5. 关键接口

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
    DeleteExpired(now time.Time) int
}
```

## 6. 可观测性

- 每个请求生成随机 `request_id`。
- 记录节点名、状态、耗时、结果数量和错误分类。
- 不记录用户输入、补充回答、搜索查询、来源正文或模型响应。
- UI 展示动作事件，不展示模型隐藏推理过程。
