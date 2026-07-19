// --- Input/Output types (Go types auto-generate MCP JSON schema) ---
package mcp

// --- Input/Output types (Go types auto-generate MCP JSON schema) ---

type RememberInput struct {
	ProjectID  string  `json:"project_id,omitempty"`
	NodeType   string  `json:"node_type,omitempty"`
	ID         string  `json:"id,omitempty"`
	Title      string  `json:"title,omitempty"`
	Content    string  `json:"content"`
	Importance float64 `json:"importance,omitempty"`
}
type RememberOutput struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type RecallInput struct {
	ProjectID string `json:"project_id,omitempty"`
	Query     string `json:"query"`
	Limit     int    `json:"limit,omitempty"`
}
type RecallOutput struct {
	Results []RecallItem `json:"results"`
}
type RecallItem struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Content  string  `json:"content"`
	NodeType string  `json:"node_type"`
	Score    float64 `json:"score"`
}

type ForgetInput struct {
	ID string `json:"id"`
}
type ForgetOutput struct {
	Status string `json:"status"`
}

type LinkInput struct {
	FromID     string  `json:"from_id"`
	ToID       string  `json:"to_id"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence,omitempty"`
}
type LinkOutput struct {
	Status string `json:"status"`
}

type IngestInput struct {
	Adapter    string `json:"adapter"`
	Root       string `json:"root"`
	ConfigPath string `json:"config_path,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
}
type IngestOutput struct {
	NodesIngested int `json:"nodes_ingested"`
	EdgesCreated  int `json:"edges_created"`
}

type ExtractAndRememberInput struct {
	ProjectID    string `json:"project_id,omitempty"`
	Conversation string `json:"conversation"`
}
type ExtractAndRememberOutput struct {
	Facts []ExtractedFactResult `json:"facts"`
}
type ExtractedFactResult struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "created" | "merged"
}

type CheckImpactInput struct {
	ID       string `json:"id"`
	MaxDepth int    `json:"max_depth,omitempty"`
}
type CheckImpactOutput struct {
	StaleNodes []StaleNode `json:"stale_nodes"`
}
type StaleNode struct {
	ID       string `json:"id"`
	NodeType string `json:"node_type"`
}
