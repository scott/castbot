// Package demo seeds the sample workspace used for screenshots.
package demo

import (
	"context"
	"time"

	"github.com/scott/castbot/internal/store"
)

// Seed adds the sample agents, a chat, and two other sessions when the account has no agents yet.
func Seed(ctx context.Context, st *store.Store, email string) error {
	user, err := st.FindUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	existing, err := st.ListAgents(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	pinned, err := st.SystemSection(ctx, user.ID, "pinned")
	if err != nil {
		return err
	}
	agents, err := st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		return err
	}
	research, err := st.CreateSection(ctx, user.ID, "Research")
	if err != nil {
		return err
	}
	created := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	type spec struct {
		section string
		in      store.AgentInput
		lines   [][2]string
	}
	rows := []spec{
		{pinned.ID, store.AgentInput{Name: "Atlas", Title: "Docs helper", Color: "c1", Model: "Provider A / model-large", Status: "ready", Activity: "writing", Approvals: 1, CreatedAt: created}, [][2]string{
			{"user", "Update the FAQ in example-org/docs-bot with the new pricing tiers from pricing.md, then push a branch for review."},
			{"agent", "On it. I will read the current FAQ and pricing.md first."},
			{"user", "Also add a line about annual billing while you wait."},
			{"agent", "Sure. I added a line about annual billing to the same draft."},
		}},
		{pinned.ID, store.AgentInput{Name: "Ledger", Title: "Finance reports", Color: "c4", Status: "paused", CreatedAt: created}, nil},
		{agents.ID, store.AgentInput{Name: "Pip", Title: "Inbox triage", Color: "c2", Status: "ready", Unread: 2, CreatedAt: created}, nil},
		{agents.ID, store.AgentInput{Name: "Juniper", Title: "Repo maintainer", Color: "c3", Status: "provisioning", Activity: "Resuming", CreatedAt: created}, nil},
		{agents.ID, store.AgentInput{Name: "Moss", Title: "Web research", Color: "c5", Status: "failed", CreatedAt: created}, nil},
		{agents.ID, store.AgentInput{Name: "Wren", Title: "Release notes", Color: "c6", Status: "paused", CreatedAt: created}, nil},
		{research.ID, store.AgentInput{Name: "Birch", Title: "Field notes", Color: "c0", Status: "ready", CreatedAt: created}, nil},
	}
	for _, row := range rows {
		row.in.SectionID = row.section
		agent, thread, err := st.CreateAgent(ctx, user.ID, row.in)
		if err != nil {
			return err
		}
		for _, line := range row.lines {
			author := user.ID
			if line[0] == "agent" {
				author = agent.ID
			}
			if _, err := st.CreateMessage(ctx, user.ID, thread.ID, line[0], author, line[1]); err != nil {
				return err
			}
		}
	}
	phones := []string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	}
	for _, ua := range phones {
		if _, err := st.CreateSession(ctx, user.ID, ua, true); err != nil {
			return err
		}
	}
	return nil
}
