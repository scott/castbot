package bundle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/secretask"
	"github.com/scott/castbot/internal/selftool"
)

func TestArchiveRoundTrip(t *testing.T) {
	raw, sum, err := Archive("name: Atlas\n", "Be brief.\n")
	if err != nil {
		t.Fatal(err)
	}
	if sum != Sum(raw) || len(sum) != 64 {
		t.Fatalf("sum %s", sum)
	}
	again, sum2, err := Archive("name: Atlas\n", "Be brief.\n")
	if err != nil {
		t.Fatal(err)
	}
	if sum2 != sum || string(again) != string(raw) {
		t.Fatal("archive is not stable")
	}
	dir := t.TempDir()
	note := filepath.Join(dir, "workspace", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(note), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("stay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir, raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil || string(got) != "name: Atlas\n" {
		t.Fatalf("yaml %q %v", got, err)
	}
	kept, err := os.ReadFile(note)
	if err != nil || string(kept) != "stay" {
		t.Fatal("install removed workspace")
	}
	if _, err := os.Stat(filepath.Join(dir, "memory")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallReplacesASymlink(t *testing.T) {
	raw, _, err := Archive("name: Atlas\n", "Be brief.\n")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "agent.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir, raw); err != nil {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(outside)
	if err != nil || string(kept) != "keep" {
		t.Fatalf("outside %q %v", kept, err)
	}
	info, err := os.Lstat(filepath.Join(dir, "agent.yaml"))
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("yaml mode %v %v", info, err)
	}
	if info.Mode().Perm()&0o007 != 0 || info.Mode().Perm()&0o040 == 0 {
		t.Fatalf("yaml mode %o", info.Mode().Perm())
	}
	got, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil || string(got) != "name: Atlas\n" {
		t.Fatalf("yaml %q %v", got, err)
	}
}

func TestSkillFileRoundTrip(t *testing.T) {
	extras := map[string]string{
		agentskill.SkillFile:  "name: create-agent\n",
		routinetool.SkillFile: "name: routines\n",
		secretask.SkillFile:   "name: api-keys\n",
		selftool.SkillFile:    "name: account\n",
		grouptool.SkillFile:   "name: groups\n",
	}
	raw, _, err := ArchiveFiles("name: Atlas\n", "Be brief.\n", extras)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := Install(dir, raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, agentskill.SkillFile))
	if err != nil || string(got) != extras[agentskill.SkillFile] {
		t.Fatalf("skill %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, "skills", "create-agent"))
	if err != nil || info.Mode().Perm()&0o050 != 0o050 || info.Mode().Perm()&0o020 != 0 {
		t.Fatalf("skill dir %v %v", info, err)
	}
	info, err = os.Stat(filepath.Join(dir, agentskill.SkillFile))
	if err != nil || info.Mode().Perm()&0o040 == 0 || info.Mode().Perm()&0o027 != 0 {
		t.Fatalf("skill file %v %v", info, err)
	}
	routine, err := os.ReadFile(filepath.Join(dir, routinetool.SkillFile))
	if err != nil || string(routine) != extras[routinetool.SkillFile] {
		t.Fatalf("routine skill %q %v", routine, err)
	}
	keys, err := os.ReadFile(filepath.Join(dir, secretask.SkillFile))
	if err != nil || string(keys) != extras[secretask.SkillFile] {
		t.Fatalf("api key skill %q %v", keys, err)
	}
	account, err := os.ReadFile(filepath.Join(dir, selftool.SkillFile))
	if err != nil || string(account) != extras[selftool.SkillFile] {
		t.Fatalf("account skill %q %v", account, err)
	}
	groups, err := os.ReadFile(filepath.Join(dir, grouptool.SkillFile))
	if err != nil || string(groups) != extras[grouptool.SkillFile] {
		t.Fatalf("groups skill %q %v", groups, err)
	}
	for _, name := range []string{
		"skills/create-agent/../../secret.txt",
		"skills/other/SKILL.md",
		"mcp/nested/agents.yaml",
	} {
		if _, _, err := ArchiveFiles("name: Atlas\n", "Be brief.\n", map[string]string{name: "no"}); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}
