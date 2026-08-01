package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizePathBackslash(t *testing.T) {
	got := normalizePath(`D:\Kerja\proj`)
	want := "D:/Kerja/proj"
	if got != want {
		t.Fatalf("normalizePath backslash: got %q, want %q", got, want)
	}
}

func TestNormalizePathTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	got := normalizePath("~/proj")
	want := filepath.Join(home, "proj")
	if got != want {
		t.Fatalf("normalizePath tilde: got %q, want %q", got, want)
	}
}

func TestRepositoryRootUsesVisibleCWD(t *testing.T) {
	dir := t.TempDir()
	got, err := repositoryRoot(dir, "")
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	if got != dir {
		t.Fatalf("repositoryRoot = %q, want %q", got, dir)
	}
}

func TestNormalizeRepositoryURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://Gitea.example/Org/repo.git", "gitea.example/Org/repo"},
		{"git@gitea.example:Org/repo.git", "gitea.example/Org/repo"},
		{"ssh://git@gitea.example/Org/repo.git", "gitea.example/Org/repo"},
	}
	for _, tc := range cases {
		if got := normalizeRepositoryURL(tc.in); got != tc.want {
			t.Errorf("normalizeRepositoryURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWalkRootPreflight(t *testing.T) {
	dir := t.TempDir()

	if err := walkRootPreflight(dir); err != nil {
		t.Fatalf("walkRootPreflight on valid dir: %v", err)
	}

	missing := filepath.Join(dir, "nope")
	if err := walkRootPreflight(missing); err == nil {
		t.Fatal("walkRootPreflight on missing path: want error, got nil")
	}

	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := walkRootPreflight(file); err == nil {
		t.Fatal("walkRootPreflight on file: want error, got nil")
	}
}
