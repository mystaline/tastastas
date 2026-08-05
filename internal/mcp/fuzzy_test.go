package mcp

import "testing"

func TestMCP_RememberRejectsNearMissProjectID(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	// Seed an existing project.
	var seedOut RememberOutput
	callTool(t, cs, "remember", RememberInput{
		ProjectID: "acme-backend",
		Content:   "seed fact",
	}, &seedOut)

	// Near-miss (1-char typo) must be rejected, not silently create a
	// second project.
	res := callTool(t, cs, "remember", RememberInput{
		ProjectID: "acme-backned", // transposed 'e'/'n'
		Content:   "should not be stored",
	}, &RememberOutput{})
	if !res.IsError {
		t.Fatalf("expected error for near-miss project_id, got success: %+v", res)
	}
}

func TestMCP_RememberAllowsExactMatchProjectID(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	var seedOut RememberOutput
	callTool(t, cs, "remember", RememberInput{
		ProjectID: "acme-backend",
		Content:   "seed fact",
	}, &seedOut)

	res := callTool(t, cs, "remember", RememberInput{
		ProjectID: "acme-backend",
		Content:   "second fact, same project",
	}, &RememberOutput{})
	if res.IsError {
		t.Fatalf("expected exact-match project_id to succeed, got error: %+v", res.Content)
	}
}

func TestMCP_RememberAllowsGenuinelyNewProjectID(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	// No existing projects at all — must not block first-ever write.
	res := callTool(t, cs, "remember", RememberInput{
		ProjectID: "brand-new-project",
		Content:   "first fact ever",
	}, &RememberOutput{})
	if res.IsError {
		t.Fatalf("expected genuinely-new project_id to succeed, got error: %+v", res.Content)
	}
}

func TestMCP_ExtractAndRememberRejectsNearMissProjectID(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	var seedOut RememberOutput
	callTool(t, cs, "remember", RememberInput{
		ProjectID: "acme-backend",
		Content:   "seed fact",
	}, &seedOut)

	res := callTool(t, cs, "extract_and_remember", ExtractAndRememberInput{
		ProjectID:    "acme-backned",
		Conversation: "irrelevant, should be rejected before extraction runs",
	}, &ExtractAndRememberOutput{})
	if !res.IsError {
		t.Fatalf("expected error for near-miss project_id, got success: %+v", res)
	}
}
