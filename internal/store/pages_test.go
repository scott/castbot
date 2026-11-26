package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

const pageBody = `{"v":1,"blocks":[{"type":"markdown","text":"Hello"}]}`

func TestPagesAccessAndRevision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 39`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema 39 %d %v", n, err)
	}
	owner := makeUser(t, s, "pages-owner@example.com", "Alex Rivera")
	other := makeUser(t, s, "pages-other@example.com", "Blair Quinn")
	member := inviteMember(t, s, owner, "pages-member@example.com", "Pip Lane")
	outsider := makeUser(t, s, "pages-out@example.com", "Casey Drew")

	ownerAgent := makeReadyAgent(t, s, owner, "Atlas")
	otherAgent := makeReadyAgent(t, s, owner, "Moss")
	memberAgent := makeReadyAgent(t, s, member, "Pip")
	outsiderAgent := makeReadyAgent(t, s, outsider, "Glen")

	made, err := s.CreatePage(ctx, owner.ID, ownerAgent.ID, "  Signups  ", pageBody)
	if err != nil {
		t.Fatal(err)
	}
	if made.Visibility != PagePrivate || made.Revision != 1 || made.Title != "Signups" || !pageTokenShape(made.Token) || made.Body != pageBody {
		t.Fatalf("%+v", made)
	}
	if _, err := s.CreatePage(ctx, owner.ID, ownerAgent.ID, "bad", "{"); err != ErrInvalid {
		t.Fatalf("bad body %v", err)
	}
	if _, err := s.PageForUser(ctx, other.ID, made.ID); err != ErrNotFound {
		t.Fatalf("other id %v", err)
	}
	if _, err := s.PageForUser(ctx, other.ID, made.Token); err != ErrNotFound {
		t.Fatalf("other token %v", err)
	}
	if _, err := s.PageForUser(ctx, member.ID, made.Token); err != ErrNotFound {
		t.Fatalf("member private %v", err)
	}
	if _, err := s.PageByLink(ctx, made.Token); err != ErrNotFound {
		t.Fatalf("private link %v", err)
	}
	if _, err := s.PageForAgent(ctx, owner.ID, otherAgent.ID, made.ID); err != ErrNotFound {
		t.Fatalf("sibling agent %v", err)
	}
	if _, err := s.UpdatePage(ctx, owner.ID, otherAgent.ID, made.ID, pageBody, 0); err != ErrNotFound {
		t.Fatalf("sibling update %v", err)
	}
	if _, err := s.PageForUser(ctx, outsider.ID, made.ID); err != ErrNotFound {
		t.Fatalf("outsider %v", err)
	}

	nextBody := `{"v":1,"blocks":[{"type":"markdown","text":"Updated"}]}`
	if _, err := s.UpdatePage(ctx, owner.ID, ownerAgent.ID, made.Token, nextBody, made.Revision+1); err != ErrRevision {
		t.Fatalf("stale %v", err)
	}
	kept, err := s.PageForUser(ctx, owner.ID, made.ID)
	if err != nil || kept.Body != pageBody || kept.Revision != 1 {
		t.Fatalf("stale wrote %#v %v", kept, err)
	}
	updated, err := s.UpdatePage(ctx, owner.ID, ownerAgent.ID, made.ID, nextBody, 1)
	if err != nil || updated.Revision != 2 || updated.Token != made.Token {
		t.Fatalf("%+v %v", updated, err)
	}
	renamed, err := s.RenamePage(ctx, owner.ID, "", made.ID, "Trials")
	if err != nil || renamed.Title != "Trials" || renamed.Revision != 2 || renamed.Token != made.Token {
		t.Fatalf("%+v %v", renamed, err)
	}

	shared, err := s.SharePage(ctx, owner.ID, ownerAgent.ID, made.ID, PageWorkspace, nil, nil)
	if err != nil || shared.Visibility != PageWorkspace {
		t.Fatalf("%+v %v", shared, err)
	}
	if _, err := s.PageForUser(ctx, member.ID, made.ID); err != nil {
		t.Fatalf("member workspace %v", err)
	}
	if _, err := s.PageForAgent(ctx, member.ID, memberAgent.ID, made.ID); err != nil {
		t.Fatalf("member agent %v", err)
	}
	if _, err := s.PageForUser(ctx, outsider.ID, made.Token); err != ErrNotFound {
		t.Fatalf("outsider workspace %v", err)
	}
	if _, err := s.SharePage(ctx, other.ID, "", made.ID, PageLink, nil, nil); err != ErrNotFound {
		t.Fatalf("other share %v", err)
	}
	if got, err := s.PageForUser(ctx, owner.ID, made.ID); err != nil || got.Visibility != PageWorkspace {
		t.Fatalf("share stuck %v %+v", err, got)
	}

	invited, err := s.SharePage(ctx, owner.ID, "", made.ID, PageInvited, []string{member.ID, member.ID}, []string{memberAgent.ID})
	if err != nil || invited.Visibility != PageInvited {
		t.Fatalf("%+v %v", invited, err)
	}
	grants, err := s.PageGrants(ctx, owner.ID, made.ID)
	if err != nil || len(grants) != 2 {
		t.Fatalf("grants %+v %v", grants, err)
	}
	if _, err := s.PageForUser(ctx, member.ID, made.Token); err != nil {
		t.Fatalf("invited member %v", err)
	}
	if _, err := s.PageForUser(ctx, other.ID, made.Token); err != ErrNotFound {
		t.Fatalf("same workspace uninvited %v", err)
	}
	if _, err := s.PageForAgent(ctx, member.ID, memberAgent.ID, made.ID); err != nil {
		t.Fatalf("invited agent %v", err)
	}
	if _, err := s.PageForAgent(ctx, owner.ID, otherAgent.ID, made.ID); err != ErrNotFound {
		t.Fatalf("user grant is not an agent grant %v", err)
	}
	if _, err := s.SharePage(ctx, owner.ID, ownerAgent.ID, made.ID, PageInvited, nil, nil); err != ErrInvalid {
		t.Fatalf("empty invite %v", err)
	}

	linked, err := s.SharePage(ctx, owner.ID, ownerAgent.ID, made.ID, PageLink, nil, nil)
	if err != nil || linked.Visibility != PageLink {
		t.Fatalf("%+v %v", linked, err)
	}
	if grants, err = s.PageGrants(ctx, owner.ID, made.ID); err != nil || len(grants) != 0 {
		t.Fatalf("grants left %+v %v", grants, err)
	}
	if _, err := s.PageByLink(ctx, made.Token); err != nil {
		t.Fatalf("public %v", err)
	}
	if _, err := s.PageForAgent(ctx, outsider.ID, outsiderAgent.ID, made.Token); err != nil {
		t.Fatalf("outsider token %v", err)
	}
	if _, err := s.PageForAgent(ctx, outsider.ID, outsiderAgent.ID, made.ID); err != ErrNotFound {
		t.Fatalf("outsider id %v", err)
	}
	if _, err := s.PageByLink(ctx, made.ID); err != ErrNotFound {
		t.Fatalf("id is not a link %v", err)
	}

	owned, err := s.ListAgentPages(ctx, owner.ID, ownerAgent.ID)
	if err != nil || len(owned) != 1 || owned[0].Body != "" || owned[0].Title != "Trials" {
		t.Fatalf("list %+v %v", owned, err)
	}
	sharedWith, err := s.ListPagesSharedWithUser(ctx, member.ID)
	if err != nil || len(sharedWith) != 0 {
		t.Fatalf("link is not a workspace share %+v %v", sharedWith, err)
	}
	if _, err := s.SharePage(ctx, owner.ID, "", made.ID, PageWorkspace, nil, nil); err != nil {
		t.Fatal(err)
	}
	sharedWith, err = s.ListPagesSharedWithUser(ctx, member.ID)
	if err != nil || len(sharedWith) != 1 || sharedWith[0].ID != made.ID {
		t.Fatalf("shared user %+v %v", sharedWith, err)
	}
	sharedAgent, err := s.ListPagesSharedWithAgent(ctx, member.ID, memberAgent.ID)
	if err != nil || len(sharedAgent) != 1 {
		t.Fatalf("shared agent %+v %v", sharedAgent, err)
	}

	if err := s.DeletePage(ctx, owner.ID, ownerAgent.ID, made.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PageForUser(ctx, owner.ID, made.Token); err != ErrNotFound {
		t.Fatalf("deleted %v", err)
	}
	if _, err := s.PageByLink(ctx, made.Token); err != ErrNotFound {
		t.Fatalf("deleted link %v", err)
	}
	again, err := s.CreatePage(ctx, owner.ID, ownerAgent.ID, "Signups", pageBody)
	if err != nil || again.Token == made.Token {
		t.Fatalf("token reused %+v %v", again, err)
	}
}

func TestPageCaps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "cap-owner@example.com", "Alex Rivera")
	agent := makeReadyAgent(t, s, owner, "Atlas")
	for i := 0; i < maxPagesPerAgent; i++ {
		if _, err := s.CreatePage(ctx, owner.ID, agent.ID, "Report", pageBody); err != nil {
			t.Fatalf("create %d %v", i, err)
		}
	}
	if _, err := s.CreatePage(ctx, owner.ID, agent.ID, "Report", pageBody); !errors.Is(err, ErrPageCap) {
		t.Fatalf("agent cap %v", err)
	}
	stamp := s.stamp(s.Now())
	for i := 0; i < maxPagesPerWorkspace-maxPagesPerAgent; i++ {
		id, err := newID()
		if err != nil {
			t.Fatal(err)
		}
		token, err := newPageToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO pages(id, workspace_id, user_id, agent_id, title, token, visibility, body, revision, created_at, updated_at) VALUES (?, ?, ?, ?, 'Filled', ?, 'private', ?, 1, ?, ?)`,
			id, owner.WorkspaceID, owner.ID, agent.ID, token, pageBody, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	second := makeReadyAgent(t, s, owner, "Moss")
	if _, err := s.CreatePage(ctx, owner.ID, second.ID, "Report", pageBody); !errors.Is(err, ErrPageCap) {
		t.Fatalf("workspace cap %v", err)
	}
}

func TestPageCards(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	when := time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return when })
	owner := makeUser(t, s, "cards-owner@example.com", "Alex Rivera")
	other := makeUser(t, s, "cards-other@example.com", "Blair Quinn")
	agent := makeReadyAgent(t, s, owner, "Atlas")
	otherAgent := makeReadyAgent(t, s, other, "Glen")
	made, err := s.CreatePage(ctx, owner.ID, agent.ID, "Old report", pageBody)
	if err != nil {
		t.Fatal(err)
	}
	when = when.Add(time.Minute)
	th, err := s.DMThread(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := s.CreateRun(ctx, owner.ID, th.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.AgentPagesTouched(ctx, owner.ID, agent.ID, runRow.StartedAt)
	if err != nil || len(before) != 0 {
		t.Fatalf("older page attached %d %v", len(before), err)
	}
	updated, err := s.UpdatePage(ctx, owner.ID, agent.ID, made.ID, pageBody, 0)
	if err != nil {
		t.Fatal(err)
	}
	touched, err := s.AgentPagesTouched(ctx, owner.ID, agent.ID, runRow.StartedAt)
	if err != nil || len(touched) != 1 || touched[0].ID != made.ID {
		t.Fatalf("touched %d %v", len(touched), err)
	}
	msg, _, err := s.FinishRun(ctx, owner.ID, runRow.ID, "Saved.", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddPageCard(ctx, owner.ID, "", made.ID, updated.Revision); err != ErrInvalid {
		t.Fatalf("empty message %v", err)
	}
	if err := s.AddPageCard(ctx, other.ID, msg.ID, made.ID, updated.Revision); err != ErrNotFound {
		t.Fatalf("other owner %v", err)
	}
	oth, err := s.DMThread(ctx, other.ID, otherAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := s.CreateRun(ctx, other.ID, oth.ID, otherAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherMsg, _, err := s.FinishRun(ctx, other.ID, otherRun.ID, "No.", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddPageCard(ctx, owner.ID, otherMsg.ID, made.ID, updated.Revision); err != ErrNotFound {
		t.Fatalf("other message %v", err)
	}
	if err := s.AddPageCard(ctx, owner.ID, msg.ID, made.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPageCard(ctx, owner.ID, msg.ID, made.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	cards, err := s.PageCardsForMessages(ctx, owner.WorkspaceID, []string{msg.ID, msg.ID, ""})
	if err != nil || len(cards) != 1 || cards[0].Title != "Old report" || cards[0].Revision != updated.Revision || cards[0].CurrentRevision != updated.Revision || cards[0].WorkspaceName != "Personal" || cards[0].PageID != made.ID {
		t.Fatalf("card count %d %v", len(cards), err)
	}
	if cards[0].Token != made.Token {
		t.Fatal("card token")
	}
	if rows, err := s.PageCardsForMessages(ctx, other.WorkspaceID, []string{msg.ID}); err != nil || len(rows) != 0 {
		t.Fatalf("other workspace %d %v", len(rows), err)
	}
	later := `{"v":1,"blocks":[{"type":"markdown","text":"Later"}]}`
	if _, err := s.UpdatePage(ctx, owner.ID, agent.ID, made.ID, later, 0); err != nil {
		t.Fatal(err)
	}
	cards, err = s.PageCardsForMessages(ctx, owner.WorkspaceID, []string{msg.ID})
	if err != nil || len(cards) != 1 || cards[0].Revision >= cards[0].CurrentRevision || cards[0].Title != "Old report" {
		t.Fatal("card revision moved")
	}
	if err := s.DeletePage(ctx, owner.ID, "", made.ID); err != nil {
		t.Fatal(err)
	}
	cards, err = s.PageCardsForMessages(ctx, owner.WorkspaceID, []string{msg.ID})
	if err != nil || len(cards) != 1 || cards[0].Title != "Old report" {
		t.Fatal("deleted page dropped the card")
	}
	gone, err := s.AgentPagesTouched(ctx, owner.ID, agent.ID, runRow.StartedAt)
	if err != nil || len(gone) != 0 {
		t.Fatalf("deleted page still touched %d %v", len(gone), err)
	}
	if err := s.AddPageCard(ctx, owner.ID, msg.ID, made.ID, 1); err != ErrNotFound {
		t.Fatalf("deleted attach %v", err)
	}
}

func makeReadyAgent(t *testing.T, s *Store, user User, name string) Agent {
	t.Helper()
	sec, err := s.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(context.Background(), user.ID, AgentInput{SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func inviteMember(t *testing.T, s *Store, owner User, email, name string) User {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.Hash("correct-horse-1", s.argon)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateInvite(ctx, owner.ID, email, name)
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.AcceptInvite(ctx, token, hash, secret)
	if err != nil {
		t.Fatal(err)
	}
	return member
}
