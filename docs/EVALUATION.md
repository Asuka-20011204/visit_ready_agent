# AI 与系统评测计划

## 1. 固定数据集

- `internal/guard/testdata/medical_safety_cases.json` 已提供 20 条确定性安全语料，覆盖当前红旗、否定、历史、假设、知识性提问、不确定表达和症状缓解。
- `internal/evaluation/testdata/visit_cases.json` 已提供 20 条端到端合成中文就诊描述，覆盖症状、时间/诱因、否定、纠正、不确定、用药/过敏/检查、慢病复诊与四类紧急表现。
- `go run ./cmd/eval` 使用真实 OpenAI-compatible 模型执行并输出 JSON 报告，统计事实精确率/召回率、grounding、重复事实、紧急准确率与 P50/P95。
- `.github/workflows/ai-evaluation.yml` 提供带密钥的手动回归门禁，并上传版本化报告；普通 PR 不自动消耗真实模型额度。

## 2. 指标

| 指标 | 目标 |
|---|---|
| JSON Schema 合法率 | >= 95% |
| 保留事实证据通过率 | 100% |
| 人工标注事实召回率 | >= 85% |
| 无依据事实数 | 0 |
| 重复事实数 | 0 |
| 否定红旗误报数 | 0 |
| 明确红旗漏报数 | 0 |
| 已提供字段重复追问数 | 0 |
| 追问原因与优先级完整率 | 100% |
| 诊断/处方越界输出数 | 0 |
| 搜索非白名单来源数 | 0 |
| P95 总响应时间 | <= 45 秒 |

## 3. 测试层次

- 单元：隐私扫描、查询清洗、域名校验、证据验证、事实去重、否定范围、风险筛查、追问排序、状态转换、输出过滤。
- 集成：假模型与假搜索下的完整 Agent 流程和 HTTP API。
- 契约：真实供应商返回结构与错误映射，可由环境变量显式开启。
- E2E：初始输入、动态多轮补充/跳过、冲突与不确定性、症状画像、风险提醒、行动清单、人工确认、引用查看和导出。
- 对抗：提示词注入、恶意 HTML、身份信息、重定向 URL、非法 JSON。

确定性医疗安全语料随普通 Go 测试执行：

```powershell
go test ./internal/guard -run TestMedicalSafetyEvaluationCorpus -count=1
```

真实模型回归需要显式提供凭据：

```powershell
$env:EVAL_LLM_ENDPOINT = "https://供应商/v1/chat/completions"
$env:EVAL_LLM_API_KEY = "..."
$env:EVAL_LLM_MODEL = "模型名"
go run ./cmd/eval > evaluation-report.json
```

## 4. 演示门禁

- 连续运行 5 次核心演示无失败。
- 提前完成真实 API 调用录屏。
- Docker 镜像已构建并导出，现场不拉取镜像。
- Windows 单文件可执行程序作为独立兜底。
