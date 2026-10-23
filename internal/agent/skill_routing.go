package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go-agent/internal/schema"
	"go-agent/internal/tools"
)

type skillSelection struct {
	Skill       string `json:"skill"`
	Intent      string `json:"intent"`
	Explanation string `json:"explanation"`
}

type skillDecision struct {
	Skill       *tools.Skill
	Intent      string
	Explanation string
}

// selectSkill performs a tool-free intent recognition pass. No user-facing
// tool can be called until this pass has selected a matching skill or returned
// an explicit empty selection.
func (a *Agent) selectSkill(ctx context.Context, prompt string) (*skillDecision, error) {
	if a.skillLoader == nil || len(a.skillLoader.ListSkills()) == 0 {
		return &skillDecision{
			Explanation: "我理解了你的请求，现在开始处理。",
		}, nil
	}

	routerMessages := []*schema.Message{
		{
			Role:    "system",
			Content: a.skillLoader.GetSkillRoutingPrompt(),
		},
		{
			Role:    "user",
			Content: "<user-request>\n" + prompt + "\n</user-request>",
		},
	}

	response, err := a.model.GenerateStream(ctx, routerMessages, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("router LLM call failed: %w", err)
	}
	if response == nil {
		return nil, fmt.Errorf("router LLM returned an empty response")
	}

	selection, err := parseSkillSelection(response.Content)
	if err != nil {
		return nil, err
	}
	if selection.Skill == "" || selection.Skill == "none" || selection.Skill == "no_match" || selection.Skill == "no-skill" {
		return &skillDecision{
			Intent:      selection.Intent,
			Explanation: normalIntentExplanation(selection.Intent),
		}, nil
	}

	skill := a.skillLoader.GetSkill(selection.Skill)
	if skill == nil {
		return nil, fmt.Errorf("router selected unknown skill %q", selection.Skill)
	}
	if selection.Explanation == "" {
		selection.Explanation = fmt.Sprintf("识别到当前请求适合使用 %s Skill。", skill.Name)
	}
	return &skillDecision{
		Skill:       skill,
		Intent:      selection.Intent,
		Explanation: selection.Explanation,
	}, nil
}

func normalIntentExplanation(intent string) string {
	if intent == "" {
		return "我理解了你的请求，现在开始处理。"
	}
	return "我理解你的请求是：" + intent + "，现在开始处理。"
}

func parseSkillSelection(content string) (*skillSelection, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("router returned an empty response")
	}

	var selection skillSelection
	if err := json.Unmarshal([]byte(content), &selection); err != nil {
		start := strings.Index(content, "{")
		end := strings.LastIndex(content, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("router response is not valid JSON: %w", err)
		}
		if extractErr := json.Unmarshal([]byte(content[start:end+1]), &selection); extractErr != nil {
			return nil, fmt.Errorf("router response is not valid JSON: %w", extractErr)
		}
	}

	selection.Skill = strings.TrimSpace(selection.Skill)
	selection.Intent = strings.TrimSpace(selection.Intent)
	selection.Explanation = strings.TrimSpace(selection.Explanation)
	return &selection, nil
}

func (a *Agent) currentSystemPrompt() string {
	if a.activeSkill == nil {
		return a.baseSystemPrompt
	}
	return a.baseSystemPrompt + "\n\n## Active Skill\n\n" + a.activeSkill.ToPrompt()
}

func (a *Agent) setActiveSkill(skill *tools.Skill) {
	a.activeSkill = skill
	systemPrompt := a.currentSystemPrompt()
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		a.messages[0].Content = systemPrompt
		return
	}
	if systemPrompt != "" {
		a.messages = append([]*Message{NewSystemMessage(systemPrompt)}, a.messages...)
	}
}
