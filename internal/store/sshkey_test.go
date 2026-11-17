package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/connect"
	"golang.org/x/crypto/ssh"
)

func TestConnectionSSHKeyStaysOutOfTheGuest(t *testing.T) {
	s := connectStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "ssh-owner@example.com", "Ada")
	other := makeUser(t, s, "ssh-other@example.com", "Bea")
	secRow, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: secRow.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	publicLine, seed := demoSSHMaterial(t)
	if err := s.SaveConnectionSSHMaterial(ctx, owner.ID, connect.DigitalOcean, publicLine, seed); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConnectionSSHMaterial(ctx, owner.ID, connect.DigitalOcean, publicLine, seed); err != nil {
		t.Fatal("second save")
	}
	if err := s.SaveConnectionSSHMaterial(ctx, owner.ID, connect.GitHub, publicLine, seed); err == nil {
		t.Fatal("github accepted an ssh key")
	}
	if _, err := s.CreateSecret(ctx, owner.ID, SecretInput{Name: DigitalOceanSSHSecret, Value: seed}); err == nil {
		t.Fatal("env create accepted the ssh seed")
	}
	row, err := s.ConnectionSSHKey(ctx, owner.ID, connect.DigitalOcean)
	if err != nil || row.PublicKey != publicLine || row.Fingerprint != "" || row.UserID != owner.ID || row.WorkspaceID != owner.WorkspaceID {
		t.Fatalf("row %+v %v", row, err)
	}
	gotSeed, err := s.SSHKeySeed(ctx, owner.ID, connect.DigitalOcean)
	if err != nil || gotSeed != seed {
		t.Fatal("seed read")
	}
	if _, err := s.ConnectionSSHKey(ctx, other.ID, connect.DigitalOcean); err != ErrNotFound {
		t.Fatal("other user read the public key")
	}
	if _, err := s.SSHKeySeed(ctx, other.ID, connect.DigitalOcean); err != ErrNotFound {
		t.Fatal("other user read the seed")
	}
	sec, err := s.SecretByName(ctx, owner.ID, DigitalOceanSSHSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantSecret(ctx, owner.ID, agent.ID, sec.ID); err == nil {
		t.Fatal("connection seed was granted")
	}
	if _, err := s.db.Exec(`UPDATE secrets SET kind = 'env' WHERE id = ? AND user_id = ?`, sec.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantSecret(ctx, owner.ID, agent.ID, sec.ID); err == nil {
		t.Fatal("forced env kind granted the seed")
	}
	if _, err := s.db.Exec(`INSERT INTO agent_secret_grants(agent_id, secret_id, workspace_id, user_id) VALUES (?, ?, ?, ?)`, agent.ID, sec.ID, owner.WorkspaceID, owner.ID); err != nil {
		t.Fatal(err)
	}
	pulled, err := s.GrantedSecrets(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pulled {
		if BlocksSSHSeed(item.Name) || item.Value == seed || strings.Contains(item.Value, seed) {
			t.Fatal("pull returned the ssh seed")
		}
	}
	meta, err := s.GrantedMeta(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range meta {
		if BlocksSSHSeed(item.Name) {
			t.Fatal("meta listed the ssh seed")
		}
	}
	if _, err := s.db.Exec(`UPDATE secrets SET kind = 'connection' WHERE id = ? AND user_id = ?`, sec.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	const fp = "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"
	if err := s.SetConnectionSSHRemote(ctx, owner.ID, connect.DigitalOcean, fp, "42"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConnectionSSHRemote(ctx, other.ID, connect.DigitalOcean, fp, "42"); err != ErrNotFound {
		t.Fatal("other user wrote the fingerprint")
	}
	row, err = s.ConnectionSSHKey(ctx, owner.ID, connect.DigitalOcean)
	if err != nil || row.Fingerprint != fp || row.RemoteID != "42" {
		t.Fatalf("remote %+v %v", row, err)
	}
	if err := s.SetConnectionSSHRemote(ctx, owner.ID, connect.DigitalOcean, "bb:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99", "42"); err != ErrConflict {
		t.Fatal("fingerprint was replaced")
	}
	has, err := s.SchemaHas(ctx, 32)
	if err != nil || !has {
		t.Fatal("schema 32")
	}
}

func demoSSHMaterial(t *testing.T) (string, string) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x21}, ed25519.SeedSize))
	pub := priv.Public().(ed25519.PublicKey)
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " castbot"
	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	return line, seed
}
