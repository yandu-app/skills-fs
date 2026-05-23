package core

import (
	"os"
	"path/filepath"
	"strings"
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
	// Windows does not honor Unix permission bits, so skip.
	if _, err := os.Stat("/proc"); os.IsNotExist(err) {
		t.Skip("skipping unwritable directory test on Windows")
	}
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

func TestSkillListEmpty(t *testing.T) {
	gen := NewSkillGenerator(t.TempDir())
	if got := gen.List(); len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}
}

func TestValidateSkillConfigDisabled(t *testing.T) {
	if err := validateSkillConfig(SkillConfig{Enabled: false}); err != nil {
		t.Fatalf("expected nil for disabled skill, got %v", err)
	}
}

func TestValidateSkillConfigDescriptionTooLong(t *testing.T) {
	cfg := SkillConfig{
		Name:        "test-skill",
		Description: string(make([]byte, 1025)),
		Enabled:     true,
	}
	if err := validateSkillConfig(cfg); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestValidateSkillConfigCompatibilityTooLong(t *testing.T) {
	cfg := SkillConfig{
		Name:          "test-skill",
		Description:   "ok",
		Compatibility: string(make([]byte, 501)),
		Enabled:       true,
	}
	if err := validateSkillConfig(cfg); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestSkillListNonEmpty(t *testing.T) {
	gen := NewSkillGenerator(t.TempDir())
	if err := gen.Generate(SkillConfig{
		Name:        "alpha",
		Description: "first",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := gen.Generate(SkillConfig{
		Name:        "beta",
		Description: "second",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	list := gen.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(list))
	}
	if list[0].Name != "alpha" || list[1].Name != "beta" {
		t.Fatalf("unexpected order: %v", list)
	}
}

func TestSkillGenerateWithOptionalFields(t *testing.T) {
	gen := NewSkillGenerator(t.TempDir())
	cfg := SkillConfig{
		Name:          "full",
		Description:   "all fields",
		License:       "MIT",
		Compatibility: "go1.25",
		AllowedTools:  []string{"tool-a", "tool-b"},
		Metadata:      map[string]string{"key": "value"},
		Enabled:       true,
	}
	if err := gen.Generate(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := gen.ReadSkillFile("full")
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "license: MIT") {
		t.Fatal("missing license in output")
	}
	if !strings.Contains(content, "compatibility: go1.25") {
		t.Fatal("missing compatibility in output")
	}
	if !strings.Contains(content, "allowed-tools:") {
		t.Fatal("missing allowed-tools in output")
	}
	if !strings.Contains(content, "metadata:") {
		t.Fatal("missing metadata in output")
	}
}
