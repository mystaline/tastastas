// Package onboard provides the unified "onboard" entry point.
// Auto-detects adapters, runs zero-LLM pipeline, runs Tier 2 inline.
package onboard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mystaline-dev/tastastas/internal/embed"
	"github.com/mystaline-dev/tastastas/internal/ingest/codeast"
	"github.com/mystaline-dev/tastastas/internal/ingest/docwalk"
	"github.com/mystaline-dev/tastastas/internal/ingest/gitrepo"
	"github.com/mystaline-dev/tastastas/internal/ingest/markdownglob"
	"github.com/mystaline-dev/tastastas/internal/ingest/obsidian"
	"github.com/mystaline-dev/tastastas/internal/store"
)

type Config struct {
	CWD       string
	ProjectID string
	Scope     string // "cwd" or "subtree"
	Embedder  embed.EmbedderBackend
	Store     store.Store // injected from caller, not opened internally
}

type Result struct {
	CWD                 string
	ProjectID           string
	AlreadyOnboarded    bool // true if project already has nodes, skip re-ingest
	DetectedAdapters    []string
	CodeSymbols         int
	CallGraphEdges      int
	ImportEdges         int
	GenericDocs         int
	ConventionsInferred int
	AutoLinked          int
	ProposalsQueued     int
	FilesWalked         int
	DurationMs          int64
	AllNodes            []store.Node // all nodes ingested during this run
}

func Run(ctx context.Context, cfg Config) (Result, error) {
	start := time.Now()
	if cfg.CWD == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Result{}, err
		}
		cfg.CWD = cwd
	}
	if cfg.ProjectID == "" {
		cfg.ProjectID = "default"
	}
	if cfg.Scope == "" {
		cfg.Scope = "subtree"
	}

	db := cfg.Store

	// Graceful check: if project already has nodes, skip re-ingestion.
	stats, err := db.Stats(ctx, cfg.ProjectID)
	if err == nil && stats.NodeCount > 0 {
		elapsed := time.Since(start).Milliseconds()
		detected := []string{}
		if stats.ConventionCnt > 0 {
			detected = append(detected, "already-onboarded")
		}

		return Result{
			CWD:              cfg.CWD,
			ProjectID:        cfg.ProjectID,
			AlreadyOnboarded: true,
			DetectedAdapters: detected,
			DurationMs:       elapsed,
		}, nil
	}

	var allNodes []store.Node
	var allEdges []store.Edge
	detectedAdapters := []string{}
	filesWalked := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error

	// Each adapter detects itself and runs concurrently.
	type adapterResult struct {
		name   string
		nodes  []store.Node
		edges  []store.Edge
		walked int
		err    error
	}
	ch := make(chan adapterResult, 5)

	startAdapter := func(name string, fn func() ([]store.Node, []store.Edge, int, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodes, edges, walked, err := fn()

			ch <- adapterResult{
				name:   name,
				nodes:  nodes,
				edges:  edges,
				walked: walked,
				err:    err,
			}
		}()
	}

	// codeast
	if hasCodeast(cfg.CWD) {
		langs := detectLanguages(cfg.CWD)
		startAdapter("codeast", func() ([]store.Node, []store.Edge, int, error) {
			ca := codeast.New(db, codeast.Config{
				Root: cfg.CWD, ProjectID: cfg.ProjectID, Languages: langs,
			})

			nodes, edges, err := ca.Ingest(ctx)
			if err != nil {
				return nil, nil, 0, err
			}

			return nodes, edges, countFiles(cfg.CWD, langs), nil
		})
	}

	//  ||
	// 	hasFile(cfg.CWD, "AGENTS.md") ||
	// 	hasFile(cfg.CWD, "CLAUDE.md") ||
	// 	hasFile(cfg.CWD, "README.md")
	// gitrepo
	if hasFile(cfg.CWD, "MEMORY.md") {
		startAdapter("gitrepo", func() ([]store.Node, []store.Edge, int, error) {
			nodes, err := gitrepo.Ingest(
				gitrepo.Config{
					Root:      cfg.CWD,
					ProjectID: cfg.ProjectID,
				},
			)
			if err != nil {
				return nil, nil, 0, err
			}

			return nodes, nil, countMEMORYFiles(cfg.CWD), nil
		})
	}

	// docwalk
	if exists(filepath.Join(cfg.CWD, ".memoryrc.yaml")) {
		startAdapter("docwalk", func() ([]store.Node, []store.Edge, int, error) {
			dwcfg, err := docwalk.LoadConfig(
				filepath.Join(cfg.CWD, ".memoryrc.yaml"),
			)
			if err != nil {
				return nil, nil, 0, fmt.Errorf("docwalk load config: %w", err)
			}

			dwcfg.ProjectID = cfg.ProjectID
			nodes, edges, walked, _, err := docwalk.Ingest(
				cfg.CWD,
				dwcfg,
			)

			return nodes, edges, walked, err
		})
	}

	// obsidian
	if exists(filepath.Join(cfg.CWD, ".obsidian")) {
		startAdapter("obsidian", func() ([]store.Node, []store.Edge, int, error) {
			nodes, edges, err := obsidian.Ingest(
				obsidian.Config{
					Root:      cfg.CWD,
					ProjectID: cfg.ProjectID,
				},
			)

			return nodes, edges, countFiles(cfg.CWD, nil), err
		})
	}

	// markdown-glob
	if hasMD(cfg.CWD) {
		startAdapter("markdown-glob", func() ([]store.Node, []store.Edge, int, error) {
			nodes, err := markdownglob.Ingest(
				markdownglob.Config{
					Root:      cfg.CWD,
					ProjectID: cfg.ProjectID,
				},
			)

			return nodes, nil, countFiles(cfg.CWD, nil), err
		})
	}

	// Collect results
	go func() {
		wg.Wait()
		close(ch)
	}()

	for r := range ch {
		mu.Lock()

		detectedAdapters = append(detectedAdapters, r.name)
		filesWalked += r.walked
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}

		allNodes = append(allNodes, r.nodes...)
		allEdges = append(allEdges, r.edges...)

		mu.Unlock()
	}
	if firstErr != nil {
		return Result{}, firstErr
	}

	// Persist base nodes/edges
	for _, n := range allNodes {
		if err := db.UpsertNode(ctx, n); err != nil {
			return Result{}, fmt.Errorf("upsert node %s: %w", n.ID, err)
		}
	}
	for _, e := range allEdges {
		if err := db.UpsertEdge(ctx, e); err != nil {
			return Result{}, fmt.Errorf("upsert edge %s->%s: %w", e.FromID, e.ToID, err)
		}
	}

	// Embedding
	if cfg.Embedder != nil {
		if err := embedNodes(ctx, db, allNodes, cfg.Embedder); err != nil {
			return Result{}, err
		}
	}

	// inferConventions
	convNodes := inferConventions(ctx, db, cfg.ProjectID, allNodes)
	for _, n := range convNodes {
		if err := db.UpsertNode(ctx, n); err != nil {
			return Result{}, err
		}
	}
	allNodes = append(allNodes, convNodes...)

	// Tier 2 inline linking
	auto, queued := Tier2ScoreAndLink(ctx, db, cfg.ProjectID, allNodes)

	return Result{
		CWD:                 cfg.CWD,
		ProjectID:           cfg.ProjectID,
		DetectedAdapters:    detectedAdapters,
		CodeSymbols:         countCodeSymbols(allNodes),
		CallGraphEdges:      countCallEdges(allEdges),
		ImportEdges:         countImportEdges(allEdges),
		GenericDocs:         countGenericDocs(allNodes),
		ConventionsInferred: len(convNodes),
		AutoLinked:          auto,
		ProposalsQueued:     queued,
		FilesWalked:         filesWalked,
		AllNodes:            allNodes,
	}, nil
}

func hasCodeast(root string) bool {
	for _, f := range []string{"go.mod", "package.json", "pyproject.toml", "Cargo.toml"} {
		if exists(filepath.Join(root, f)) {
			return true
		}
	}
	return false
}

func detectLanguages(root string) []string {
	var langs []string
	if exists(filepath.Join(root, "go.mod")) {
		langs = append(langs, "go")
	}
	if exists(filepath.Join(root, "package.json")) {
		langs = append(langs, "typescript")
	}
	if exists(filepath.Join(root, "pyproject.toml")) {
		langs = append(langs, "python")
	}
	if exists(filepath.Join(root, "Cargo.toml")) {
		langs = append(langs, "rust")
	}
	return langs
}

func hasFile(root, name string) bool {
	found := false
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == name {
			found = true
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hasMD(root string) bool {
	found := false
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			found = true
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

func embedNodes(ctx context.Context, db store.Store, nodes []store.Node, emb embed.EmbedderBackend) error {
	const batchSize = 32
	for i := 0; i < len(nodes); i += batchSize {
		end := i + batchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		batch := nodes[i:end]

		// Collect non-empty content for embedding
		var texts []string
		var idx []int
		for j := range batch {
			if batch[j].Content != "" {
				texts = append(texts, batch[j].Content)
				idx = append(idx, j)
			}
		}
		if len(texts) == 0 {
			continue
		}

		vecs, err := emb.EmbedBatch(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed batch %d-%d: %w", i, end, err)
		}

		for k, v := range vecs {
			batch[idx[k]].Embedding = v
			if err := db.UpsertNode(ctx, batch[idx[k]]); err != nil {
				return fmt.Errorf("upsert embed %s: %w", batch[idx[k]].ID, err)
			}
		}
	}
	return nil
}

func inferConventions(ctx context.Context, db store.Store, projectID string, nodes []store.Node) []store.Node {
	// Collect code:function and code:type nodes
	type codeSym struct {
		node store.Node
		pkg  string
		name string
	}
	var funcs []codeSym
	for _, n := range nodes {
		if n.NodeType == "code:function" {
			// ID format: {projectID}/code:function/{pkgpath}.{name}
			rest := strings.TrimPrefix(n.ID, projectID+"/code:function/")
			pkg, name := splitLastDot(rest)
			funcs = append(funcs, codeSym{node: n, pkg: pkg, name: name})
		}
	}
	if len(funcs) == 0 {
		return nil
	}

	var conventions []store.Node
	convID := 0

	// 1. Cluster by name prefix
	prefixes := map[string][]string{} // prefix -> member IDs
	for _, f := range funcs {
		p := namePrefix(f.name)
		if p == "" {
			continue
		}

		key := fmt.Sprintf("%s/%s", f.pkg, p)
		prefixes[key] = append(prefixes[key], f.node.ID)
	}
	for key, members := range prefixes {
		if len(members) < 2 {
			continue
		}

		convID++
		id := fmt.Sprintf("%s/convention/prefix-%d", projectID, convID)

		n := store.Node{
			ID:        id,
			ProjectID: projectID,
			NodeType:  "convention",
			Title:     "Naming convention: " + key,
			Content: fmt.Sprintf(
				"Functions sharing name prefix %q in package %s have %d members",
				key,
				extractPkg(key),
				len(members),
			),
			Status:        "current",
			SourceAdapter: "inferConventions",
			Importance:    0.4,
		}

		conventions = append(conventions, n)
		for _, m := range members {
			db.UpsertEdge(ctx, store.Edge{
				FromID:     n.ID,
				ToID:       m,
				EdgeType:   "convention-member",
				Confidence: 0.7,
			})
		}
	}

	// 2. Cluster by receiver (for methods)
	recvGroups := map[string][]string{} // receiver type -> member IDs
	for _, f := range funcs {
		rest := strings.TrimPrefix(f.node.ID, projectID+"/code:function/")
		pkgFunc := strings.TrimPrefix(rest, f.pkg+".")

		if !strings.Contains(pkgFunc, ".") {
			continue // not a method
		}

		recv := strings.SplitN(pkgFunc, ".", 2)[0]
		key := f.pkg + "." + recv
		recvGroups[key] = append(recvGroups[key], f.node.ID)
	}
	for key, members := range recvGroups {
		if len(members) < 2 {
			continue
		}

		convID++
		id := fmt.Sprintf("%s/convention/receiver-%d", projectID, convID)

		n := store.Node{
			ID:            id,
			ProjectID:     projectID,
			NodeType:      "convention",
			Title:         "Receiver methods: " + key,
			Content:       fmt.Sprintf("Type %q has %d methods", key, len(members)),
			Status:        "current",
			SourceAdapter: "inferConventions",
			Importance:    0.5,
		}

		conventions = append(conventions, n)
		for _, m := range members {
			db.UpsertEdge(ctx, store.Edge{
				FromID:     n.ID,
				ToID:       m,
				EdgeType:   "convention-member",
				Confidence: 0.8,
			})
		}
	}

	return conventions
}

// namePrefix extracts a common naming prefix from a function name.
// "GetUserByID" -> "Get", "handleRequest" -> "handle", "NewServer" -> "New"
func namePrefix(name string) string {
	if len(name) == 0 {
		return ""
	}
	// CamelCase/PascalCase: grab leading uppercase+lowercase sequence
	var prefix []rune
	for i, r := range name {
		if i == 0 && r >= 'A' && r <= 'Z' {
			// PascalCase: take uppercase + following lowercase run
			prefix = append(prefix, r)
		} else if r >= 'a' && r <= 'z' && len(prefix) > 0 {
			prefix = append(prefix, r)
		} else {
			break
		}
	}
	if len(prefix) < 3 {
		// lowercaseStart: grab leading lowercase run ("handle", "to", "is")
		prefix = nil
		for i, r := range name {
			if r >= 'a' && r <= 'z' {
				prefix = append(prefix, r)
			} else {
				break
			}
			_ = i
		}
	}
	if len(prefix) < 2 {
		return ""
	}
	return string(prefix)
}

func splitLastDot(s string) (string, string) {
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

func extractPkg(key string) string {
	i := strings.Index(key, "/")
	if i < 0 {
		return key
	}
	return key[:i]
}

func Tier2ScoreAndLink(ctx context.Context, db store.Store, projectID string, newNodes []store.Node) (int, int) {
	autoLinked := 0
	proposalsQueued := 0

	if len(newNodes) < 2 {
		return 0, 0
	}

	// Score each pair (undirected, so only i<j)
	for i := 0; i < len(newNodes); i++ {
		a := newNodes[i]
		if a.Embedding == nil {
			continue
		}
		for j := i + 1; j < len(newNodes); j++ {
			b := newNodes[j]
			if b.Embedding == nil {
				continue
			}

			cos := cosineSimilarity(a.Embedding, b.Embedding)
			tc := typeCompat(a.NodeType, b.NodeType)
			pp := pathProximity(a.SourcePath, b.SourcePath)
			io := identifierOverlap(a.Title, b.Title)

			score := 0.4*cos + 0.2*tc + 0.2*pp + 0.2*io
			if score < 0.55 {
				continue
			}

			edgeType := "proposed"
			if score > 0.80 {
				edgeType = "auto-linked"
				autoLinked++
			} else {
				proposalsQueued++
			}

			db.UpsertEdge(ctx, store.Edge{
				FromID:     a.ID,
				ToID:       b.ID,
				EdgeType:   edgeType,
				Confidence: score,
			})
		}
	}

	return autoLinked, proposalsQueued
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrtFloat64(normA) * sqrtFloat64(normB))
}

func sqrtFloat64(x float64) float64 {
	// Newton's method for sqrt, no math import
	if x <= 0 {
		return 0
	}
	z := x / 2
	for i := 0; i < 20; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

func typeCompat(t1, t2 string) float64 {
	if t1 == t2 {
		return 1.0
	}
	// Same category prefix (code:, generic-, architecture-) is partial match
	p1 := strings.SplitN(t1, ":", 2)[0]
	p2 := strings.SplitN(t2, ":", 2)[0]
	if p1 == p2 {
		return 0.5
	}
	return 0
}

func pathProximity(p1, p2 string) float64 {
	if p1 == "" || p2 == "" {
		return 0
	}
	parts1 := strings.Split(filepath.ToSlash(p1), "/")
	parts2 := strings.Split(filepath.ToSlash(p2), "/")

	// Count shared prefix segments
	shared := 0
	max := len(parts1)
	if len(parts2) < max {
		max = len(parts2)
	}

	for i := 0; i < max; i++ {
		if parts1[i] == parts2[i] {
			shared++
		} else {
			break
		}
	}

	if shared == 0 {
		return 0
	}
	// Jaccard-like: shared / max(depth)
	depth := len(parts1)
	if len(parts2) > depth {
		depth = len(parts2)
	}
	return float64(shared) / float64(depth)
}

func identifierOverlap(t1, t2 string) float64 {
	tokens1 := tokenize(t1)
	tokens2 := tokenize(t2)
	if len(tokens1) == 0 || len(tokens2) == 0 {
		return 0
	}

	intersection := 0
	for _, t := range tokens1 {
		for _, u := range tokens2 {
			if t == u {
				intersection++
				break
			}
		}
	}

	union := len(tokens1) + len(tokens2) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// tokenize splits an identifier into words on camelCase/PascalCase/snake_case
func tokenize(s string) []string {
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, ".", " ")
	var words []string
	current := ""

	for _, r := range s {
		if r == ' ' {
			if current != "" {
				words = append(words, strings.ToLower(current))
				current = ""
			}
			continue
		}

		if r >= 'A' && r <= 'Z' && current != "" {
			// Upper after lower = new word
			last := current[len(current)-1]
			if last >= 'a' && last <= 'z' {
				words = append(words, strings.ToLower(current))
				current = string(r)
				continue
			}
		}

		current += string(r)
	}

	if current != "" {
		words = append(words, strings.ToLower(current))
	}
	return words
}

func countCodeSymbols(nodes []store.Node) int {
	c := 0
	for _, n := range nodes {
		if strings.HasPrefix(n.NodeType, "code:") {
			c++
		}
	}
	return c
}

func countCallEdges(edges []store.Edge) int {
	c := 0
	for _, e := range edges {
		if e.EdgeType == "calls" {
			c++
		}
	}
	return c
}

func countImportEdges(edges []store.Edge) int {
	c := 0
	for _, e := range edges {
		if e.EdgeType == "imports" {
			c++
		}
	}

	return c
}

func countGenericDocs(nodes []store.Node) int {
	c := 0
	for _, n := range nodes {
		if n.NodeType == "generic-doc" {
			c++
		}
	}

	return c
}

func countFiles(root string, langs []string) int {
	exts := extSet(langs)
	n := 0

	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if exts == nil {
			n++
		} else if exts[filepath.Ext(p)] {
			n++
		}
		return nil
	})

	return n
}

// ponytail: skipDirs list. Add more if projects use other conventions.
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".svn", ".hg", "node_modules", "vendor", ".next", ".turbo",
		"__pycache__", ".pytest_cache", "target", "dist", ".cache":
		return true
	}

	return strings.HasPrefix(name, ".")
}

func countMEMORYFiles(root string) int {
	n := 0
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "MEMORY.md" {
			n++
		}
		return nil
	})
	return n
}

// ponytail: extSet maps language names to file extensions for countFiles.
// Add when a new language needs coverage.
func extSet(langs []string) map[string]bool {
	if len(langs) == 0 {
		return nil
	}
	m := map[string]bool{}
	for _, l := range langs {
		switch l {
		case "go":
			m[".go"] = true
		case "typescript":
			m[".ts"] = true
			m[".tsx"] = true
		case "python":
			m[".py"] = true
		case "rust":
			m[".rs"] = true
		}
	}
	return m
}
