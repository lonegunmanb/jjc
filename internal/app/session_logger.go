package app

import (
	"fmt"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

// logSessionEvent prints a structured single-line log entry for one Copilot
// SessionEvent. It is intended to be registered via session.On(...) so callers
// gain end-to-end visibility into the agent's internal processing: turns,
// reasoning, tool calls, permission requests, sub-agents, hooks, compaction,
// errors, and idle/shutdown.
//
// The cardID parameter identifies the Trello card whose worker session
// produced the event, so multi-card concurrent log lines can be separated.
func logSessionEvent(logger sysevent.Sink, cardID string, e copilot.SessionEvent) {

	// Drop high-frequency / low-signal events that otherwise drown out the log.
	switch e.Type {
	case "tool.execution_partial_result", "session.background_tasks_changed":
		return
	}

	switch d := e.Data.(type) {

	// ---------------- session lifecycle ----------------
	case *copilot.SessionStartData:
		model := strDeref(d.SelectedModel)
		sysevent.Emitf(logger, "copilot_sdk", "sub=session_start card=%s session_id=%s model=%s producer=%s version=%v",
			cardID, d.SessionID, model, d.Producer, d.CopilotVersion)
	case *copilot.SessionResumeData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=session_resume card=%s", cardID)
	case *copilot.SessionShutdownData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=session_shutdown card=%s shutdown_type=%s premium_requests=%v api_duration_ms=%v",
			cardID, d.ShutdownType, d.TotalPremiumRequests, d.TotalAPIDurationMs)
	case *copilot.SessionIdleData:
		aborted := false
		if d.Aborted != nil {
			aborted = *d.Aborted
		}
		sysevent.Emitf(logger, "copilot_sdk", "sub=session_idle card=%s aborted=%t", cardID, aborted)
	case *copilot.SessionErrorData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=session_error card=%s error_type=%s message=%q status=%v",
			cardID, d.ErrorType, d.Message, intDeref(d.StatusCode))
	case *copilot.SessionInfoData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=session_info card=%s info_type=%s message=%q",
			cardID, d.InfoType, d.Message)
	case *copilot.SessionWarningData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=session_warning card=%s warning_type=%s message=%q",
			cardID, d.WarningType, d.Message)

	// ---------------- assistant turn ----------------
	case *copilot.AssistantTurnStartData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=assistant_turn_start card=%s turn_id=%s",
			cardID, d.TurnID)
	case *copilot.AssistantIntentData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=assistant_intent card=%s intent=%q",
			cardID, d.Intent)
	case *copilot.AssistantReasoningData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=assistant_reasoning card=%s chars=%d preview=%q",
			cardID, len(d.Content), preview(d.Content, 200))
	case *copilot.AssistantMessageData:
		// Skip empty assistant placeholder messages: when the model only emits
		// a tool call (no text), the SDK still fires AssistantMessageData with
		// chars=0; the subsequent tool_start log line carries the useful info.
		if len(d.Content) == 0 {
			return
		}
		sysevent.Emitf(logger, "copilot_sdk", "sub=assistant_message card=%s message_id=%s chars=%d tool_requests=%d preview=%q",
			cardID, d.MessageID, len(d.Content), len(d.ToolRequests), preview(d.Content, 200))
	case *copilot.AssistantTurnEndData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=assistant_turn_end card=%s turn_id=%s",
			cardID, d.TurnID)
	case *copilot.AssistantUsageData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=assistant_usage card=%s model=%s in=%v out=%v cache_read=%v cache_write=%v duration_ms=%v",
			cardID, d.Model,
			floatDeref(d.InputTokens), floatDeref(d.OutputTokens),
			floatDeref(d.CacheReadTokens), floatDeref(d.CacheWriteTokens),
			floatDeref(d.Duration))

	// ---------------- tool execution ----------------
	case *copilot.ToolUserRequestedData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=tool_user_requested card=%s tool=%s call_id=%s",
			cardID, d.ToolName, d.ToolCallID)
	case *copilot.ToolExecutionStartData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=tool_start card=%s tool=%s call_id=%s args_preview=%q",
			cardID, d.ToolName, d.ToolCallID, preview(fmt.Sprintf("%v", d.Arguments), 200))
	case *copilot.ToolExecutionProgressData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=tool_progress card=%s call_id=%s message=%q",
			cardID, d.ToolCallID, d.ProgressMessage)
	case *copilot.ToolExecutionCompleteData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=tool_complete card=%s call_id=%s success=%t",
			cardID, d.ToolCallID, d.Success)

	// ---------------- permissions ----------------
	case *copilot.PermissionRequestedData:
		toolName := strDeref(d.PermissionRequest.ToolName)
		sysevent.Emitf(logger, "copilot_sdk", "sub=permission_requested card=%s request_id=%s kind=%s tool=%s",
			cardID, d.RequestID, d.PermissionRequest.Kind, toolName)
	case *copilot.PermissionCompletedData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=permission_completed card=%s request_id=%s result_kind=%s",
			cardID, d.RequestID, d.Result.Kind)

	// ---------------- sub-agents ----------------
	case *copilot.SubagentStartedData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=subagent_started card=%s call_id=%s name=%s display=%q",
			cardID, d.ToolCallID, d.AgentName, d.AgentDisplayName)
	case *copilot.SubagentCompletedData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=subagent_completed card=%s call_id=%s name=%s tool_calls=%v tokens=%v duration_ms=%v",
			cardID, d.ToolCallID, d.AgentName,
			floatDeref(d.TotalToolCalls), floatDeref(d.TotalTokens), floatDeref(d.DurationMs))
	case *copilot.SubagentFailedData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=subagent_failed card=%s call_id=%s name=%s error=%q",
			cardID, d.ToolCallID, d.AgentName, d.Error)

	// ---------------- hooks ----------------
	case *copilot.HookStartData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=hook_start card=%s hook_id=%s type=%s",
			cardID, d.HookInvocationID, d.HookType)
	case *copilot.HookEndData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=hook_end card=%s hook_id=%s type=%s success=%t",
			cardID, d.HookInvocationID, d.HookType, d.Success)

	// ---------------- system / context ----------------
	case *copilot.SystemMessageData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=system_message card=%s role=%s chars=%d",
			cardID, d.Role, len(d.Content))
	case *copilot.SystemNotificationData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=system_notification card=%s kind=%v preview=%q",
			cardID, d.Kind, preview(d.Content, 200))
	case *copilot.SessionTruncationData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=truncation card=%s pre_tokens=%v post_tokens=%v removed_tokens=%v removed_msgs=%v",
			cardID, d.PreTruncationTokensInMessages, d.PostTruncationTokensInMessages,
			d.TokensRemovedDuringTruncation, d.MessagesRemovedDuringTruncation)
	case *copilot.SessionCompactionStartData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=compaction_start card=%s system_tokens=%v conv_tokens=%v tools_tokens=%v",
			cardID, floatDeref(d.SystemTokens), floatDeref(d.ConversationTokens), floatDeref(d.ToolDefinitionsTokens))
	case *copilot.SessionCompactionCompleteData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=compaction_complete card=%s success=%t pre_tokens=%v post_tokens=%v removed_tokens=%v",
			cardID, d.Success, floatDeref(d.PreCompactionTokens), floatDeref(d.PostCompactionTokens), floatDeref(d.TokensRemoved))

	case *copilot.UserMessageData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=user_message card=%s chars=%d source=%s",
			cardID, len(d.Content), strDeref(d.Source))
	case *copilot.AbortData:
		sysevent.Emitf(logger, "copilot_sdk", "sub=abort card=%s reason=%q", cardID, d.Reason)

	default:
		// Catch-all so unhandled event types are still observable.
		sysevent.Emitf(logger, "copilot_sdk", "sub=other card=%s event_type=%s data_type=%T",
			cardID, e.Type, e.Data)
	}
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func intDeref(p *int64) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func floatDeref(p *float64) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// preview returns at most maxRunes runes from s, single-line, with "..."
// appended when truncated. Newlines and tabs are replaced by spaces so the
// resulting log line stays on one row.
func preview(s string, maxRunes int) string {
	if s == "" {
		return ""
	}
	cleaned := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	r := []rune(cleaned)
	if len(r) <= maxRunes {
		return cleaned
	}
	return string(r[:maxRunes]) + "..."
}
