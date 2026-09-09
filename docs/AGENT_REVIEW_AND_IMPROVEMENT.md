# 医疗提前准备病症 Agent 审查与改进规格文档

**版本**：v2026-09-06  
**目标**：让 Codex 直接修改代码，符合医疗安全边界、可靠性与企业级要求。

## 一、当前项目核心状态（已验证）
- 使用 Go 单体 + CloudWeGo Eino Graph 固定编排。
- 核心流程（提取事实 → 证据校验 → 澄清追问 → 受限搜索 → 问题+行动清单 → 人工核对 → 导出）能正确运行。
- 有原文依据绑定、PII 扫描、ContainsMedicalOverreach 校验。
- 状态管理包含 SymptomProfile、Timeline、RiskSignal、Uncertainties、Contradictions。
- 追问最多 4 轮，优先紧急+高价值信息。
- 风险信号必须带原文依据 + 否定词二次筛查。

**存在的主要问题**（直接影响医疗安全与可靠性）：
1. 缺少硬性 Emergency Escalation 节点（高风险输入仍继续追问）。
2. 追问轮数、澄清优先级、风险筛查高度依赖 LLM 结构化输出，缺少硬编码确定性决策层。
3. ClarificationTurns 序列化不完整，Resume 后易出现状态漂移。
4. 无独立 Validator 节点，LLM 输出合法性校验仅靠 decodeStrictJSON。

## 二、必须优先实现的改进（P0/P1）

### P0 - Emergency Escalation 节点（最高优先级）
**新增节点**：`emergencyEscalationNode`（固定文案 + 终止流程 + 导出）。

**修改位置**：
- `internal/agent/workflow.go`（新增节点 + 边）
- `internal/agent/runner.go`（Runner 结构体中添加节点引用）
- `internal/guard/source.go`（新增 `EmergencyEscalation` 方法）

**实现要求**：
- 当任何 RiskSignal 的 Priority 为 `urgent` 时，立即跳到 emergency 节点。
- 固定输出文案：`“这些表现正在发生或明显加重，请立即联系当地急救服务或前往急诊。”`
- 终止整个工作流，进入 `StatusFailed` 或直接导出。
- 无论任何状态，都必须显示固定安全提醒。

### P1 - 独立 Validator 节点
**新增文件**：`internal/agent/validator.go`

**功能**：
- 在任何 LLM 输出后运行（Extract 后 + GenerateQuestions 后）。
- 严格检查：
  - 所有字段必须有原文依据（groundedQuote）。
  - 风险信号必须通过 `ContainsMedicalOverreach` + `positiveRedFlagTerms`。
  - 澄清问题必须是安全问句。
  - 行动清单不得包含治疗建议。
- 发现问题时返回 `ErrMedicalBoundaryViolation`，HTTP 层统一返回 400。

**修改位置**：
- `internal/agent/runner.go`（在 questionNode / outputGuardNode 后调用 Validator）。
- `internal/llm/openai.go`（在 decodeStrictJSON 后调用 Validator）。

### P2 - 补充确定性决策层
- 在 `evidenceNode` 中硬编码：
  - 最大追问轮数（4 轮）。
  - 澄清问题优先级排序（urgent > high > normal）。
  - 当出现 urgent RiskSignal 时，强制进入澄清问题。
- 在 `groundedRiskSignals` 中硬编码否定词筛查逻辑（即使 LLM 给出风险信号，也必须通过二次校验）。

## 三、推荐架构图（最终形态）

```text
User
   ↓
HTTP Input Guard + PII + Length + Privacy
   ↓
Emergency Risk Check (Go)
   ↓
State Extractor (LLM)
   ↓
Evidence Validator + Grounded Filter (Go)
   ↓
State Machine Decision (Go)
   ↓
Clarification Branch (LLM + Go priority)
   ↓
Search Planner (Go)
   ↓
Trusted Search Tool (Bocha)
   ↓
Output Generator (LLM)
   ↓
Safety Validator + Reflection (Go)
   ↓
Emergency Escalation (Go)
   ↓
Final Session
   ↓
Export / Confirm
```

## 四、后续步骤（直接执行）

1. 先实现 **Emergency Escalation** 节点（P0）。
2. 再实现 **Validator**（P1）。
3. 最后补充确定性决策层（P2）。

所有修改后，请运行 `go test ./... -race` 并确认所有测试通过。

---

**Codex 执行命令**：
```
cd d:\Reze my files\shixi\visit_ready_agent
go run ./cmd/server
```

需要我现在就把 **Emergency Escalation 节点 + Validator 的完整代码**（包括 runner.go + 新文件）直接给出吗？直接说“给代码”即可。