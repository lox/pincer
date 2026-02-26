package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapWorkspaceSeedsTemplateFilesWithoutOverwrite(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "workspace")
	if err := bootstrapWorkspace(root); err != nil {
		t.Fatalf("bootstrap workspace: %v", err)
	}

	for _, tc := range []struct {
		name      string
		mustMatch string
	}{
		{name: "HEARTBEAT.md", mustMatch: "# Periodic Tasks"},
		{name: "SOUL.md", mustMatch: "security-first autonomous assistant"},
		{name: "IDENTITY.md", mustMatch: "# Identity"},
	} {
		path := filepath.Join(root, tc.name)
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.name, err)
		}
		if !strings.Contains(string(contents), tc.mustMatch) {
			t.Fatalf("expected %s to contain %q, got: %q", tc.name, tc.mustMatch, string(contents))
		}
	}

	customSoul := "# custom soul\n"
	soulPath := filepath.Join(root, "SOUL.md")
	if err := os.WriteFile(soulPath, []byte(customSoul), 0o644); err != nil {
		t.Fatalf("write custom SOUL.md: %v", err)
	}

	if err := bootstrapWorkspace(root); err != nil {
		t.Fatalf("bootstrap workspace second pass: %v", err)
	}

	soulAfter, err := os.ReadFile(soulPath)
	if err != nil {
		t.Fatalf("read SOUL.md after second bootstrap: %v", err)
	}
	if string(soulAfter) != customSoul {
		t.Fatalf("expected existing SOUL.md to be preserved, got %q", string(soulAfter))
	}
}

func TestBootstrapWorkspaceUsesTemplatesDirectoryWhenPresent(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)

	if err := os.MkdirAll("templates", 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join("templates", "SOUL.md"), []byte("# template soul\n"), 0o644); err != nil {
		t.Fatalf("write templates/SOUL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join("templates", "IDENTITY.md"), []byte("# template identity\n"), 0o644); err != nil {
		t.Fatalf("write templates/IDENTITY.md: %v", err)
	}

	root := filepath.Join(wd, "workspace")
	if err := bootstrapWorkspace(root); err != nil {
		t.Fatalf("bootstrap workspace: %v", err)
	}

	soul, err := os.ReadFile(filepath.Join(root, "SOUL.md"))
	if err != nil {
		t.Fatalf("read workspace SOUL.md: %v", err)
	}
	if string(soul) != "# template soul\n" {
		t.Fatalf("expected workspace SOUL.md to be copied from templates, got %q", string(soul))
	}

	identity, err := os.ReadFile(filepath.Join(root, "IDENTITY.md"))
	if err != nil {
		t.Fatalf("read workspace IDENTITY.md: %v", err)
	}
	if string(identity) != "# template identity\n" {
		t.Fatalf("expected workspace IDENTITY.md to be copied from templates, got %q", string(identity))
	}
}
