package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSkillFileInvalidName(t *testing.T) {
	gen := NewSkillGenerator(t.TempDir())
	_, err := gen.ReadSkillFile("Bad_Name")
	if !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestReadSkillFileNotFound(t *testing.T) {
	gen := NewSkillGenerator(t.TempDir())
	_, err := gen.ReadSkillFile("missing")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
}

func TestSkillGeneratorRemoveErrors(t *testing.T) {
	// Empty root returns nil without work.
	genEmpty := NewSkillGenerator("")
	if err := genEmpty.Remove("anything"); err != nil {
		t.Fatalf("expected nil for empty root, got %v", err)
	}

	// Invalid name returns EINVAL.
	gen := NewSkillGenerator(t.TempDir())
	if err := gen.Remove("Bad_Name"); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}

	// RemoveAll error path: create a skill dir and make it unwritable.
	root := t.TempDir()
	gen2 := NewSkillGenerator(root)
	if err := gen2.Generate(SkillConfig{
		Name:        "test-skill",
		Description: "d",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, "test-skill")
	if err := os.Chmod(skillDir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(skillDir, 0o755) // ensure cleanup succeeds
	if err := gen2.Remove("test-skill"); err == nil {
		t.Fatal("expected error when directory is unwritable")
	}
}
