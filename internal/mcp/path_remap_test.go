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

func TestRemapRoot(t *testing.T) {
	t.Setenv("HOST_WORKSPACE_DIR", "/home/user/Workspace")

	ok := []struct {
		name string
		in   string
		want string
	}{
		{"exact match", "/home/user/Workspace", "/workspaces"},
		{"prefix match", "/home/user/Workspace/projects/foo", "/workspaces/projects/foo"},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, err := remapRoot(c.in)
			if err != nil {
				t.Fatalf("remapRoot(%q): unexpected error %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("remapRoot(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	outside := []struct {
		name string
		in   string
	}{
		{"windows drive outside prefix", "D:/Kerja"},
		{"unrelated path", "/opt/other/proj"},
		{"prefix guard /home/user2", "/home/user2/proj"},
	}
	for _, c := range outside {
		t.Run(c.name, func(t *testing.T) {
			if _, err := remapRoot(c.in); err == nil {
				t.Fatalf("remapRoot(%q): want error, got nil", c.in)
			}
		})
	}
}

func TestRemapRootCaseInsensitive(t *testing.T) {
	t.Setenv("HOST_WORKSPACE_DIR", "D:/Kerja")

	for _, in := range []string{"d:/Kerja/proj", "D:/KERJA/proj", "D:/Kerja"} {
		want := "/workspaces"
		if in != "D:/Kerja" {
			want = "/workspaces/proj"
		}
		got, err := remapRoot(in)
		if err != nil {
			t.Fatalf("remapRoot(%q): unexpected error %v", in, err)
		}
		if got != want {
			t.Fatalf("remapRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemapRootNoEnv(t *testing.T) {
	t.Setenv("HOST_WORKSPACE_DIR", "")
	got, err := remapRoot("/home/user/proj")
	if err != nil {
		t.Fatalf("remapRoot with empty host prefix: unexpected error %v", err)
	}
	if got != "/home/user/proj" {
		t.Fatalf("remapRoot with empty host prefix = %q, want identity", got)
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
