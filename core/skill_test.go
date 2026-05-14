package core

import (
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
