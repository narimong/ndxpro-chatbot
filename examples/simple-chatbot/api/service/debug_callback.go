package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/schema"

	"simple-chatbot/api/model"
)

// DebugEmitter is a function type for emitting debug events
type DebugEmitter func(event string, data interface{})

// DebugCallbackHandler implements callbacks.Handler for debug event emission
type DebugCallbackHandler struct {
	emitter    DebugEmitter
	startTimes sync.Map // map[string]time.Time
	stepCount  int
	mu         sync.Mutex
}

// NewDebugCallbackHandler creates a new debug callback handler
func NewDebugCallbackHandler(emitter DebugEmitter) *DebugCallbackHandler {
	return &DebugCallbackHandler{
		emitter: emitter,
	}
}

// generateStepID generates a unique step ID
func (h *DebugCallbackHandler) generateStepID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stepCount++
	return fmt.Sprintf("step_%d", h.stepCount)
}

// getComponentName returns a human-readable component name
func getComponentName(component components.Component) string {
	switch component {
	case components.ComponentOfChatModel:
		return model.ComponentModel
	case components.ComponentOfRetriever:
		return model.ComponentRetriever
	case components.ComponentOfPrompt:
		return model.ComponentPrompt
	case components.ComponentOfEmbedding:
		return "embedding"
	case components.ComponentOfIndexer:
		return "indexer"
	case components.ComponentOfTool:
		return "tool"
	case components.ComponentOfLoader:
		return "loader"
	case components.ComponentOfTransformer:
		return "transformer"
	default:
		return string(component)
	}
}

// OnStart is called when a component starts execution
func (h *DebugCallbackHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	stepID := h.generateStepID()
	h.startTimes.Store(info.Name, time.Now())

	payload := model.DebugStepPayload{
		StepID:    stepID,
		StepName:  info.Name,
		Component: getComponentName(info.Component),
		Status:    model.StatusStarted,
		Input:     summarizeInput(input),
		Timestamp: time.Now().UnixMilli(),
	}

	h.emitter(model.EventDebugStep, payload)
	return ctx
}

// OnEnd is called when a component completes execution
func (h *DebugCallbackHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	var duration int64
	if startTime, ok := h.startTimes.Load(info.Name); ok {
		duration = time.Since(startTime.(time.Time)).Milliseconds()
		h.startTimes.Delete(info.Name)
	}

	payload := model.DebugStepPayload{
		StepID:    "",
		StepName:  info.Name,
		Component: getComponentName(info.Component),
		Status:    model.StatusCompleted,
		Output:    summarizeOutput(output),
		Duration:  duration,
		Timestamp: time.Now().UnixMilli(),
	}

	h.emitter(model.EventDebugStep, payload)
	return ctx
}

// OnError is called when an error occurs
func (h *DebugCallbackHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	var duration int64
	if startTime, ok := h.startTimes.Load(info.Name); ok {
		duration = time.Since(startTime.(time.Time)).Milliseconds()
		h.startTimes.Delete(info.Name)
	}

	payload := model.DebugStepPayload{
		StepID:    "",
		StepName:  info.Name,
		Component: getComponentName(info.Component),
		Status:    model.StatusError,
		Output:    map[string]string{"error": err.Error()},
		Duration:  duration,
		Timestamp: time.Now().UnixMilli(),
	}

	h.emitter(model.EventDebugStep, payload)
	return ctx
}

// OnStartWithStreamInput is called when a component starts with stream input
func (h *DebugCallbackHandler) OnStartWithStreamInput(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	// Close the stream reader as we don't need to process it
	if input != nil {
		input.Close()
	}

	stepID := h.generateStepID()
	h.startTimes.Store(info.Name, time.Now())

	payload := model.DebugStepPayload{
		StepID:    stepID,
		StepName:  info.Name,
		Component: getComponentName(info.Component),
		Status:    model.StatusStarted,
		Input:     map[string]string{"type": "stream"},
		Timestamp: time.Now().UnixMilli(),
	}

	h.emitter(model.EventDebugStep, payload)
	return ctx
}

// OnEndWithStreamOutput is called when a component completes with stream output
func (h *DebugCallbackHandler) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	// Close the stream reader as we don't need to process it
	if output != nil {
		output.Close()
	}

	var duration int64
	if startTime, ok := h.startTimes.Load(info.Name); ok {
		duration = time.Since(startTime.(time.Time)).Milliseconds()
		h.startTimes.Delete(info.Name)
	}

	payload := model.DebugStepPayload{
		StepID:    "",
		StepName:  info.Name,
		Component: getComponentName(info.Component),
		Status:    model.StatusCompleted,
		Output:    map[string]string{"type": "stream"},
		Duration:  duration,
		Timestamp: time.Now().UnixMilli(),
	}

	h.emitter(model.EventDebugStep, payload)
	return ctx
}

// summarizeInput creates a summary of the input for debugging
func summarizeInput(input callbacks.CallbackInput) interface{} {
	if input == nil {
		return nil
	}

	switch v := input.(type) {
	case []*schema.Message:
		summary := make([]map[string]interface{}, 0, len(v))
		for _, msg := range v {
			summary = append(summary, map[string]interface{}{
				"role":    string(msg.Role),
				"content": truncateString(msg.Content, 100),
			})
		}
		return map[string]interface{}{
			"messages_count": len(v),
			"messages":       summary,
		}
	case string:
		return map[string]interface{}{
			"query": truncateString(v, 200),
		}
	case map[string]interface{}:
		return v
	default:
		return map[string]interface{}{
			"type": fmt.Sprintf("%T", v),
		}
	}
}

// summarizeOutput creates a summary of the output for debugging
func summarizeOutput(output callbacks.CallbackOutput) interface{} {
	if output == nil {
		return nil
	}

	switch v := output.(type) {
	case *schema.Message:
		result := map[string]interface{}{
			"role":    string(v.Role),
			"content": truncateString(v.Content, 200),
		}
		if len(v.ToolCalls) > 0 {
			result["tool_calls_count"] = len(v.ToolCalls)
		}
		return result
	case []*schema.Document:
		return map[string]interface{}{
			"documents_count": len(v),
		}
	case string:
		return map[string]interface{}{
			"result": truncateString(v, 200),
		}
	default:
		return map[string]interface{}{
			"type": fmt.Sprintf("%T", v),
		}
	}
}

// truncateString truncates a string to the specified length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
