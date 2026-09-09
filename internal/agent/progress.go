package agent

import (
	"context"
	"time"

	"visitready/internal/domain"
)

type progressContextKey struct{}

type ProgressReporter func(domain.AgentProgress)

func WithProgress(ctx context.Context, reporter ProgressReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressContextKey{}, reporter)
}

func ReportProgress(ctx context.Context, event domain.AgentProgress) {
	reporter, _ := ctx.Value(progressContextKey{}).(ProgressReporter)
	if reporter != nil {
		reporter(event)
	}
}

func (r *Runner) instrumentNode(name string, fn func(context.Context, workflowState) (workflowState, error)) func(context.Context, workflowState) (workflowState, error) {
	return func(ctx context.Context, state workflowState) (workflowState, error) {
		ReportProgress(ctx, domain.AgentProgress{Node: name, Status: "running", Message: nodeProgressMessage(name, false)})
		started := time.Now()
		next, err := fn(ctx, state)
		duration := time.Since(started).Milliseconds()
		if err != nil {
			ReportProgress(ctx, domain.AgentProgress{Node: name, Status: "failed", Message: nodeProgressMessage(name, true), DurationMS: duration})
			return next, err
		}
		next.Session.NodeTimings = append(next.Session.NodeTimings, domain.NodeTiming{Node: name, DurationMS: duration, Status: "completed", CompletedAt: r.now()})
		ReportProgress(ctx, domain.AgentProgress{Node: name, Status: "completed", Message: nodeProgressMessage(name, true), DurationMS: duration})
		return next, nil
	}
}

func nodeProgressMessage(name string, completed bool) string {
	labels := map[string][2]string{
		nodeExtract:           {"正在提取明确事实", "事实提取完成"},
		nodeValidateExtract:   {"正在校验模型提取", "提取边界校验完成"},
		nodeEvidence:          {"正在核对原文证据", "原文证据核对完成"},
		nodeEmergency:         {"正在执行紧急风险检查", "紧急风险检查完成"},
		nodeSearch:            {"正在检索可信资料", "可信资料检索完成"},
		nodeQuestions:         {"正在生成就诊沟通建议", "就诊沟通建议生成完成"},
		nodeValidateQuestions: {"正在验证问题边界", "问题边界验证完成"},
		nodeGuard:             {"正在执行最终安全检查", "最终安全检查完成"},
	}
	pair := labels[name]
	if completed {
		return pair[1]
	}
	return pair[0]
}
