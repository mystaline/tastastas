package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func execLookPathGit() (string, error) {
	return exec.LookPath("git")
}

func initGitRepoWithRemote(t *testing.T, dir, remoteURL string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"remote", "add", "origin", remoteURL},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

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
	got, err := repositoryRoot(dir, "", "")
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	if got != dir {
		t.Fatalf("repositoryRoot = %q, want %q", got, dir)
	}
}

// TestRepositoryRootUsesVisibleCWDWhenWorkspaceRootUnset guards the error
// text in repositoryRoot/resolveRef against logic drift: with
// SERVER_WORKSPACE_ROOT unset, a server-visible cwd must still resolve
// verbatim (the default lives in main.go, not here — server.go's
// serverWorkspaceRoot() stays a pure getenv for t.Setenv test isolation).
func TestRepositoryRootUsesVisibleCWDWhenWorkspaceRootUnset(t *testing.T) {
	t.Setenv("SERVER_WORKSPACE_ROOT", "")
	dir := t.TempDir()
	got, err := repositoryRoot(dir, "", "")
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	if got != dir {
		t.Fatalf("repositoryRoot = %q, want %q", got, dir)
	}
}

func TestRepositoryRootRefusesBareWorkspaceRootFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SERVER_WORKSPACE_ROOT", root)

	if _, err := repositoryRoot("", "", ""); err == nil {
		t.Fatal("repositoryRoot with no cwd/repository_url/project_id: want error, got nil (would have walked the whole workspace root)")
	}

	sub := filepath.Join(root, "my-project")
	if err := os.MkdirAll(filepath.Join(sub, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	got, err := repositoryRoot("", "", "my-project")
	if err != nil {
		t.Fatalf("repositoryRoot with matching project_id: %v", err)
	}
	if got != sub {
		t.Fatalf("repositoryRoot = %q, want %q", got, sub)
	}

	if _, err := repositoryRoot("", "", "no-such-project"); err == nil {
		t.Fatal("repositoryRoot with unmatched project_id: want error, got nil")
	}
}

// TestRepositoryRootFindsNestedRepoByName mirrors the real-world layout
// reported live: repos are nested under an org/group directory
// (e.g. /workspaces/Personal/repo-a), not direct children of
// SERVER_WORKSPACE_ROOT. project_id-only resolution must still find them.
func TestRepositoryRootFindsNestedRepoByName(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SERVER_WORKSPACE_ROOT", root)

	nested := filepath.Join(root, "Personal", "tastastas")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	got, err := repositoryRoot("", "", "tastastas")
	if err != nil {
		t.Fatalf("repositoryRoot with nested project_id: %v", err)
	}
	if got != nested {
		t.Fatalf("repositoryRoot = %q, want %q", got, nested)
	}
}

// TestRepositoryRootFindsNestedRepoByURL mirrors the real-world case where
// the client's local cwd is unrelated (even a different OS path like
// D:/Kerja/proj) — only repository_url identity should matter, and the
// server must find the matching nested repo regardless of client cwd.
func TestRepositoryRootFindsNestedRepoByURL(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git binary not available")
	}
	root := t.TempDir()
	t.Setenv("SERVER_WORKSPACE_ROOT", root)

	nested := filepath.Join(root, "Personal", "tastastas")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	initGitRepoWithRemote(t, nested, "git@github.com:mystaline/tastastas.git")

	got, err := repositoryRoot("D:/Kerja/some-unrelated-client-path", "git@github.com:mystaline/tastastas.git", "tastastas")
	if err != nil {
		t.Fatalf("repositoryRoot with nested repo + unrelated cwd: %v", err)
	}
	if got != nested {
		t.Fatalf("repositoryRoot = %q, want %q", got, nested)
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

func TestResolveRefLocalFallbackNonGit(t *testing.T) {
	dir := t.TempDir()
	if got := resolveRef(dir); got != "local" {
		t.Fatalf("resolveRef(non-git dir) = %q, want %q", got, "local")
	}
}

func TestResolveRefLocalFallbackEmptyCWD(t *testing.T) {
	if got := resolveRef(""); got != "local" {
		t.Fatalf("resolveRef(\"\") = %q, want %q", got, "local")
	}
}

func TestResolveRefLocalFallbackUnbornHEAD(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if got := resolveRef(dir); got != "local" {
		t.Fatalf("resolveRef(unborn HEAD) = %q, want %q", got, "local")
	}
}

func TestResolveRefDetectsRealBranch(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cmd := exec.Command("git", "-C", dir, "add", "f.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "commit", "-q", "-m", "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	got := resolveRef(dir)
	if got == "" || got == "local" || got == "HEAD" {
		t.Fatalf("resolveRef(committed repo) = %q, want a real branch name", got)
	}
}

// TestResolveRepoSuffixMatch resolves a client-side cwd that lives in a
// different path namespace than the server (client /home/x/Workspace/...
// vs server temp root) via longest-suffix matching against the repo index.
func TestResolveRepoSuffixMatch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SERVER_WORKSPACE_ROOT", root)

	nested := filepath.Join(root, "Personal", "tastastas")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	other := filepath.Join(root, "other", "thing")
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	rr, err := resolveRepo("/home/x/Workspace/Personal/tastastas", "", "")
	if err != nil {
		t.Fatalf("resolveRepo: %v", err)
	}
	if rr.Root != nested {
		t.Fatalf("Root = %q, want %q", rr.Root, nested)
	}
}

func TestResolveRepoSuffixAmbiguous(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SERVER_WORKSPACE_ROOT", root)
	for _, name := range []string{"a/tastastas", "b/tastastas"} {
		dir := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	_, err := resolveRepo("/home/x/tastastas", "", "")
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "repository_url") {
		t.Fatalf("error should suggest repository_url: %v", err)
	}
}

func TestResolveRepoIdentityFromRemote(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	initGitRepoWithRemote(t, dir, "git@gitea.example:Org/repo.git")

	rr, err := resolveRepo(dir, "", "")
	if err != nil {
		t.Fatalf("resolveRepo: %v", err)
	}
	if rr.ProjectID != "gitea.example/Org/repo" {
		t.Fatalf("ProjectID = %q, want gitea.example/Org/repo", rr.ProjectID)
	}
	if rr.ProjectName != "repo" {
		t.Fatalf("ProjectName = %q, want repo", rr.ProjectName)
	}
	if rr.RepositoryURL != "gitea.example/Org/repo" {
		t.Fatalf("RepositoryURL = %q, want gitea.example/Org/repo", rr.RepositoryURL)
	}
}

func TestResolveRepoNonGitDirUsesBasename(t *testing.T) {
	dir := t.TempDir()
	rr, err := resolveRepo(dir, "", "")
	if err != nil {
		t.Fatalf("resolveRepo: %v", err)
	}
	base := filepath.Base(dir)
	if rr.ProjectID != base || rr.ProjectName != base {
		t.Fatalf("ProjectID=%q ProjectName=%q, want basename %q", rr.ProjectID, rr.ProjectName, base)
	}
	if rr.RepositoryURL != "" {
		t.Fatalf("RepositoryURL = %q, want empty", rr.RepositoryURL)
	}
}

func TestResolveRepoLegacyProjectIDOverrides(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	initGitRepoWithRemote(t, dir, "git@gitea.example:Org/repo.git")

	rr, err := resolveRepo(dir, "", "myproj")
	if err != nil {
		t.Fatalf("resolveRepo: %v", err)
	}
	if rr.ProjectID != "myproj" {
		t.Fatalf("ProjectID = %q, want myproj", rr.ProjectID)
	}
}

func TestValidateCloneURL(t *testing.T) {
	valid := []string{
		"https://gitea.example/Org/repo.git",
		"ssh://git@gitea.example/Org/repo.git",
		"git@gitea.example:Org/repo.git",
	}
	for _, in := range valid {
		if err := validateCloneURL(in); err != nil {
			t.Errorf("validateCloneURL(%q): unexpected error: %v", in, err)
		}
	}
	invalid := []string{
		"",
		"--upload-pack=x",
		"file:///etc",
		"https://gitea.example/Org/a b",
	}
	for _, in := range invalid {
		if err := validateCloneURL(in); err == nil {
			t.Errorf("validateCloneURL(%q): expected error, got nil", in)
		}
	}
}

func TestSidecarProjectID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gitea.example/Org/repo::stage:main", "Org/repo"},
		{"gitea.example/Org/repo", "Org/repo"},
		{"default::stage:local", "default"},
		{"default", "default"},
	}
	for _, c := range cases {
		if got := sidecarProjectID(c.in); got != c.want {
			t.Fatalf("sidecarProjectID(%q) = %q, want %q", c.in, got, c.want)
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
