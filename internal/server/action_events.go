package server

import (
	"context"

	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
)

func (a *App) emitActionStatusEvent(ctx context.Context, source, sourceID, turnID, actionID string, status protocolv1.ActionStatus, reason string) {
	if source != "chat" || sourceID == "" {
		return
	}
	_, _ = a.appendThreadEvent(ctx, &protocolv1.ThreadEvent{
		ThreadId:     sourceID,
		TurnId:       turnID,
		Source:       protocolv1.EventSource_POLICY_ENGINE,
		ContentTrust: protocolv1.ContentTrust_TRUSTED_VALIDATED,
		Payload: &protocolv1.ThreadEvent_ProposedActionStatusChanged{ProposedActionStatusChanged: &protocolv1.ProposedActionStatusChanged{
			ActionId: actionID,
			Status:   status,
			Reason:   reason,
		}},
	})
}

func (a *App) emitToolExecutionStarted(ctx context.Context, threadID, turnID, executionID, actionID, toolName, displayCommand string) {
	if threadID == "" {
		return
	}
	_, _ = a.appendThreadEvent(ctx, &protocolv1.ThreadEvent{
		ThreadId:     threadID,
		TurnId:       turnID,
		Source:       protocolv1.EventSource_TOOL_EXECUTOR,
		ContentTrust: protocolv1.ContentTrust_TRUSTED_VALIDATED,
		Payload: &protocolv1.ThreadEvent_ToolExecutionStarted{ToolExecutionStarted: &protocolv1.ToolExecutionStarted{
			ExecutionId:    executionID,
			ToolCallId:     actionID,
			ToolName:       toolName,
			DisplayCommand: displayCommand,
		}},
	})
}

func (a *App) emitToolExecutionOutputDelta(ctx context.Context, threadID, turnID, executionID string, stream protocolv1.OutputStream, chunk []byte, offset uint64) {
	if threadID == "" || len(chunk) == 0 {
		return
	}
	_, _ = a.appendThreadEvent(ctx, &protocolv1.ThreadEvent{
		ThreadId:     threadID,
		TurnId:       turnID,
		Source:       protocolv1.EventSource_TOOL_EXECUTOR,
		ContentTrust: protocolv1.ContentTrust_TRUSTED_VALIDATED,
		Payload: &protocolv1.ThreadEvent_ToolExecutionOutputDelta{ToolExecutionOutputDelta: &protocolv1.ToolExecutionOutputDelta{
			ExecutionId: executionID,
			Stream:      stream,
			Chunk:       chunk,
			OffsetBytes: offset,
			Utf8:        true,
		}},
	})
}

func (a *App) emitToolExecutionFinished(ctx context.Context, threadID, turnID, executionID string, result bashExecutionResult) {
	if threadID == "" {
		return
	}
	_, _ = a.appendThreadEvent(ctx, &protocolv1.ThreadEvent{
		ThreadId:     threadID,
		TurnId:       turnID,
		Source:       protocolv1.EventSource_TOOL_EXECUTOR,
		ContentTrust: protocolv1.ContentTrust_TRUSTED_VALIDATED,
		Payload: &protocolv1.ThreadEvent_ToolExecutionFinished{ToolExecutionFinished: &protocolv1.ToolExecutionFinished{
			ExecutionId: executionID,
			ExitCode:    int32(result.ExitCode),
			DurationMs:  uint64(result.Duration.Milliseconds()),
			TimedOut:    result.TimedOut,
			Truncated:   result.Truncated,
		}},
	})
}
