package scope

import "testing"

func TestMarkerLen(t *testing.T) {
	if len(Marker) != markerLen {
		t.Fatalf("markerLen=%d but len(Marker)=%d", markerLen, len(Marker))
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		stage   string
		wantErr bool
	}{
		{"simple", "repo-a", "main", false},
		{"slash stage", "repo-a", "feature/auth", false},
		{"percent in stage", "repo-a", "100%done", false},
		{"unicode stage", "repo-a", "分支", false},
		{"stage equal to marker", "repo-a", Marker, false},
		{"empty base defaults", "", "main", false},
		{"empty stage errors", "repo-a", "", true},
		{"blank stage ok (not empty)", "repo-a", " ", false},
		{"control char stage errors", "repo-a", "bad\x01stage", true},
		{"base already has marker errors", "repo-a" + Marker + "x", "main", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			enc, err := Encode(c.base, c.stage)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Encode(%q,%q) expected error, got %q", c.base, c.stage, enc)
				}
				return
			}
			if err != nil {
				t.Fatalf("Encode(%q,%q) unexpected error: %v", c.base, c.stage, err)
			}
			wantBase := c.base
			if wantBase == "" {
				wantBase = "default"
			}
			base, stage, staged, err := Decode(enc)
			if err != nil {
				t.Fatalf("Decode(%q) unexpected error: %v", enc, err)
			}
			if !staged {
				t.Fatalf("Decode(%q) staged=false, want true", enc)
			}
			if base != wantBase {
				t.Fatalf("Decode(%q) base=%q want %q", enc, base, wantBase)
			}
			if stage != c.stage {
				t.Fatalf("Decode(%q) stage=%q want %q", enc, stage, c.stage)
			}
		})
	}
}

func TestDecodeLegacyUnscoped(t *testing.T) {
	base, stage, staged, err := Decode("repo-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if staged {
		t.Fatalf("staged=true, want false")
	}
	if base != "repo-a" || stage != "" {
		t.Fatalf("base=%q stage=%q, want repo-a/empty", base, stage)
	}
}

func TestDecodeMalformedEscape(t *testing.T) {
	_, _, _, err := Decode("repo-a" + Marker + "%zz")
	if err == nil {
		t.Fatal("expected error for malformed escape")
	}
}

func TestDecodeEmptyStageAfterMarker(t *testing.T) {
	_, _, _, err := Decode("repo-a" + Marker)
	if err == nil {
		t.Fatal("expected error for empty stage")
	}
}

func TestResolve(t *testing.T) {
	if _, err := Resolve("repo-a", "", true); err == nil {
		t.Fatal("expected error when requireStage=true and stage empty")
	}
	got, err := Resolve("repo-a", "", false)
	if err != nil || got != "repo-a" {
		t.Fatalf("got %q, %v; want repo-a, nil", got, err)
	}
	got, err = Resolve("repo-a", "main", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantBase, wantStage, staged, _ := Decode(got)
	if !staged || wantBase != "repo-a" || wantStage != "main" {
		t.Fatalf("Resolve encoded wrong: %q", got)
	}
}

func TestProjectIDFromRemote(t *testing.T) {
	cases := []struct {
		in, id, name string
		ok           bool
	}{
		{"git@gitea.example:Org/repo.git", "gitea.example/Org/repo", "repo", true},
		{"https://gitea.example/Org/repo.git", "gitea.example/Org/repo", "repo", true},
		{"ssh://git@gitea.example/Org/repo", "gitea.example/Org/repo", "repo", true},
		{"", "", "", false},
	}
	for _, c := range cases {
		id, name, ok := ProjectIDFromRemote(c.in)
		if ok != c.ok || id != c.id || name != c.name {
			t.Errorf("ProjectIDFromRemote(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, id, name, ok, c.id, c.name, c.ok)
		}
	}
}

func TestEdgeAllowedTruthTable(t *testing.T) {
	unscoped := "repo-a"
	stagedMain, _ := Encode("repo-a", "main")
	stagedMain2, _ := Encode("repo-b", "main")
	stagedFeature, _ := Encode("repo-a", "feature")

	cases := []struct {
		name     string
		from, to string
		want     bool
	}{
		{"unscoped/unscoped", unscoped, unscoped, true},
		{"unscoped/staged", unscoped, stagedMain, true},
		{"staged/unscoped", stagedMain, unscoped, true},
		{"staged/staged same stage", stagedMain, stagedMain2, true},
		{"staged/staged different stage", stagedMain, stagedFeature, false},
		{"empty/empty", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EdgeAllowed(c.from, c.to)
			if got != c.want {
				t.Fatalf("EdgeAllowed(%q,%q)=%v want %v", c.from, c.to, got, c.want)
			}
		})
	}
}
