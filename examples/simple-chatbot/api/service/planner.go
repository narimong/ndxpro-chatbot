package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	apimodel "simple-chatbot/api/model"
)

const planningPrompt = `You are an expert planning agent. Given the user's question, create a step-by-step plan to answer it comprehensively.

Requirements:
- Break down into 2-5 clear, specific steps
- Each step should be actionable and focused
- Steps should be in logical order
- Keep step descriptions concise (under 50 characters each)

User question: %s

Return ONLY a valid JSON object in this exact format (no markdown, no explanation):
{"steps": ["step1 description", "step2 description", "step3 description"]}`

// Plan represents an execution plan
type Plan struct {
	ID        string     `json:"plan_id"`
	Objective string     `json:"objective"`
	Steps     []PlanStep `json:"steps"`
}

// PlanStep represents a single step in the plan
type PlanStep struct {
	Index       int    `json:"index"`
	Description string `json:"description"`
	Status      string `json:"status"` // pending, in_progress, completed, error
}

// PlannerService handles planning and execution
type PlannerService struct {
	model   model.ChatModel
	emitter DebugEmitter
}

// NewPlannerService creates a new planner service
func NewPlannerService(chatModel model.ChatModel) *PlannerService {
	return &PlannerService{
		model: chatModel,
	}
}

// SetEmitter sets the debug emitter for the planner
func (p *PlannerService) SetEmitter(emitter DebugEmitter) {
	p.emitter = emitter
}

// emit sends a debug event if emitter is set
func (p *PlannerService) emit(event string, data interface{}) {
	if p.emitter != nil {
		p.emitter(event, data)
	}
}

// CreatePlan generates a plan for the given query
func (p *PlannerService) CreatePlan(ctx context.Context, query string) (*Plan, error) {
	// Emit planning started
	planID := uuid.New().String()[:8]
	p.emit(apimodel.EventDebugPlan, apimodel.DebugPlanPayload{
		PlanID:      planID,
		Objective:   truncateString(query, 100),
		Steps:       []string{},
		CurrentStep: -1,
		Status:      apimodel.StatusPlanning,
		Timestamp:   time.Now().UnixMilli(),
	})

	// Generate plan using LLM
	prompt := fmt.Sprintf(planningPrompt, query)
	messages := []*schema.Message{
		schema.UserMessage(prompt),
	}

	response, err := p.model.Generate(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	// Parse the response
	plan, err := p.parsePlanResponse(response.Content, planID, query)
	if err != nil {
		// If parsing fails, create a default single-step plan
		plan = &Plan{
			ID:        planID,
			Objective: query,
			Steps: []PlanStep{
				{Index: 0, Description: "Answer the question directly", Status: "pending"},
			},
		}
	}

	// Emit plan created
	stepDescriptions := make([]string, len(plan.Steps))
	for i, step := range plan.Steps {
		stepDescriptions[i] = step.Description
	}

	p.emit(apimodel.EventDebugPlan, apimodel.DebugPlanPayload{
		PlanID:      plan.ID,
		Objective:   truncateString(plan.Objective, 100),
		Steps:       stepDescriptions,
		CurrentStep: 0,
		Status:      apimodel.StatusExecuting,
		Timestamp:   time.Now().UnixMilli(),
	})

	return plan, nil
}

// parsePlanResponse parses the LLM response into a Plan
func (p *PlannerService) parsePlanResponse(content, planID, objective string) (*Plan, error) {
	// Clean up the response - remove markdown code blocks if present
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	// Parse JSON
	var result struct {
		Steps []string `json:"steps"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w", err)
	}

	if len(result.Steps) == 0 {
		return nil, fmt.Errorf("no steps in plan")
	}

	// Convert to Plan structure
	plan := &Plan{
		ID:        planID,
		Objective: objective,
		Steps:     make([]PlanStep, len(result.Steps)),
	}

	for i, step := range result.Steps {
		plan.Steps[i] = PlanStep{
			Index:       i,
			Description: step,
			Status:      "pending",
		}
	}

	return plan, nil
}

// UpdateStepStatus updates the status of a plan step and emits debug event
func (p *PlannerService) UpdateStepStatus(plan *Plan, stepIndex int, status string) {
	if stepIndex < 0 || stepIndex >= len(plan.Steps) {
		return
	}

	plan.Steps[stepIndex].Status = status

	stepDescriptions := make([]string, len(plan.Steps))
	for i, step := range plan.Steps {
		stepDescriptions[i] = step.Description
	}

	p.emit(apimodel.EventDebugPlan, apimodel.DebugPlanPayload{
		PlanID:      plan.ID,
		Objective:   truncateString(plan.Objective, 100),
		Steps:       stepDescriptions,
		CurrentStep: stepIndex,
		Status:      apimodel.StatusExecuting,
		Timestamp:   time.Now().UnixMilli(),
	})
}

// CompletePlan marks the plan as completed
func (p *PlannerService) CompletePlan(plan *Plan) {
	stepDescriptions := make([]string, len(plan.Steps))
	for i, step := range plan.Steps {
		plan.Steps[i].Status = apimodel.StatusCompleted
		stepDescriptions[i] = step.Description
	}

	p.emit(apimodel.EventDebugPlan, apimodel.DebugPlanPayload{
		PlanID:      plan.ID,
		Objective:   truncateString(plan.Objective, 100),
		Steps:       stepDescriptions,
		CurrentStep: len(plan.Steps) - 1,
		Status:      apimodel.StatusCompleted,
		Timestamp:   time.Now().UnixMilli(),
	})
}

// GetPlanContext returns a string representation of the plan for context
func (p *PlannerService) GetPlanContext(plan *Plan) string {
	if plan == nil || len(plan.Steps) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Follow this plan to answer:\n")
	for i, step := range plan.Steps {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, step.Description))
	}
	return sb.String()
}
