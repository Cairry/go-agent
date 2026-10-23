package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go-agent/internal/llm"
	"go-agent/internal/schema"
	"go-agent/internal/tools"
)

type skillRoutingClient struct {
	responses  []*schema.LLMResponse
	toolCounts []int
}

func (c *skillRoutingClient) GenerateStream(_ context.Context, _ []*schema.Message, toolSchemas []map[string]interface{}, _ llm.StreamCallback) (*schema.LLMResponse, error) {
	c.toolCounts = append(c.toolCounts, len(toolSchemas))
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func (c *skillRoutingClient) Close() error { return nil }

func TestRunRecognizesSkillBeforeProvidingTools(t *testing.T) {
	skillsDir := t.TempDir()
	skillDir := filepath.Join(skillsDir, "create-file")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: create-file\ndescription: Create a new file safely\n---\nAsk for confirmation before writing."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	loader := tools.NewSkillLoader(skillsDir)
	if _, err := loader.DiscoverSkills(); err != nil {
		t.Fatal(err)
	}
	client := &skillRoutingClient{
		responses: []*schema.LLMResponse{
			{Content: `{"skill":"create-file"}`},
			{Content: "Please confirm before I create the file."},
		},
	}
	a := New(client, WithSkillLoader(loader), WithSystemPrompt("base prompt"), WithWorkspace(t.TempDir()))

	if _, err := a.Run(context.Background(), "Create a file"); err != nil {
		t.Fatal(err)
	}

	if len(client.toolCounts) != 2 {
		t.Fatalf("expected router and normal LLM calls, got %d", len(client.toolCounts))
	}
	if client.toolCounts[0] != 0 {
		t.Fatalf("expected intent router to receive no tools, got %d", client.toolCounts[0])
	}
	if client.toolCounts[1] == 0 {
		t.Fatal("expected normal LLM call to receive tools")
	}
	if a.activeSkill == nil || a.activeSkill.Name != "create-file" {
		t.Fatalf("expected create-file skill to be active")
	}
}

func TestParseSkillSelection(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{name: "json", content: `{"skill":"create-file"}`, want: "create-file"},
		{name: "wrapped json", content: "```json\n{\"skill\":\"create-file\"}\n```", want: "create-file"},
		{name: "no match", content: `{"skill":""}`, want: ""},
		{name: "invalid", content: "create-file", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSkillSelection(tc.content)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Skill != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got.Skill)
			}
		})
	}
}
