// Package onboard provides the unified "onboard" entry point.
// Auto-detects adapters, runs zero-LLM pipeline, runs Tier 2 inline.
package onboard

import (
	"context"
	"fmt"
	"log"
	"math"
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
	BatchSize int         // 0 = default 32
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
	HierarchyNodes      int
	AutoLinked          int
	ProposalsQueued     int
	FilesWalked         int
	FilesSkipped        int
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

	// Graceful check: if project already has nodes AND chunks, skip
	// re-ingestion. Gating on NodeCount alone let a project that had nodes
	// but never got chunked (crash mid-run, embedder down that one time)
	// stay permanently unchunked — every future onboard call would hit
	// this fast-path and never retry chunk+embed.
	stats, err := db.Stats(ctx, cfg.ProjectID)
	if err == nil && stats.NodeCount > 0 && stats.ChunkCount > 0 {
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

	allNodes, allEdges, detectedAdapters, filesWalked, filesSkipped, err := AutoDetectAdapters(ctx, db, cfg.CWD, cfg.ProjectID)
	if err != nil {
		return Result{}, err
	}

	// Persist base nodes/edges
	for _, n := range allNodes {
		if err := db.UpsertNode(ctx, n); err != nil {
			return Result{}, fmt.Errorf("upsert node %s: %w", n.ID, err)
		}
	}
	if err := db.UpsertEdges(ctx, allEdges); err != nil {
		return Result{}, fmt.Errorf("upsert edges: %w", err)
	}

	// Embedding
	embedBatchSize := cfg.BatchSize
	if embedBatchSize <= 0 {
		embedBatchSize = 32
	}
	if cfg.Embedder != nil {
		if err := EmbedNodes(ctx, db, allNodes, cfg.Embedder, embedBatchSize); err != nil {
			return Result{}, err
		}
	}

	// InferConventions
	convNodes := InferConventions(ctx, db, cfg.ProjectID, allNodes)
	for _, n := range convNodes {
		if err := db.UpsertNode(ctx, n); err != nil {
			return Result{}, err
		}
	}
	allNodes = append(allNodes, convNodes...)

	// Directory hierarchy backbone: synthetic nodes connecting every real
	// node up to a single repo-root node via "contains" edges. Purely
	// additive, derived from SourcePath only — see hierarchy.go.
	hierNodes, hierEdges := BuildHierarchy(cfg.ProjectID, allNodes)
	for _, n := range hierNodes {
		if err := db.UpsertNode(ctx, n); err != nil {
			return Result{}, err
		}
	}
	if err := db.UpsertEdges(ctx, hierEdges); err != nil {
		return Result{}, err
	}
	allNodes = append(allNodes, hierNodes...)

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
		HierarchyNodes:      len(hierNodes),
		AutoLinked:          auto,
		ProposalsQueued:     queued,
		FilesWalked:         filesWalked,
		FilesSkipped:        filesSkipped,
		AllNodes:            allNodes,
	}, nil
}

// AutoDetectAdapters runs all matching adapters concurrently against root,
// returns merged nodes, edges, detected adapter names, and file count.
// Used by both onboard.Run() and the MCP ingest tool.
func AutoDetectAdapters(ctx context.Context, db store.Store, root, projectID string) (nodes []store.Node, edges []store.Edge, adapters []string, filesWalked, filesSkipped int, err error) {
	var mu sync.Mutex
	var wg sync.WaitGroup

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
			n, e, w, er := fn()
			ch <- adapterResult{name: name, nodes: n, edges: e, walked: w, err: er}
		}()
	}

	// ---- detection + dispatch ----

	if hasCodeast(root) {
		langs := detectLanguages(root)
		startAdapter("codeast", func() ([]store.Node, []store.Edge, int, error) {
			ca := codeast.New(db, codeast.Config{Root: root, ProjectID: projectID, Languages: langs})
			n, e, caErr := ca.Ingest(ctx)
			return n, e, countFiles(root, langs), caErr
		})
	}
	if hasFile(root, "MEMORY.md") {
		startAdapter("gitrepo", func() ([]store.Node, []store.Edge, int, error) {
			n, gitErr := gitrepo.Ingest(gitrepo.Config{Root: root, ProjectID: projectID})
			return n, nil, countMEMORYFiles(root), gitErr
		})
	}
	if exists(filepath.Join(root, ".memoryrc.yaml")) {
		startAdapter("docwalk", func() ([]store.Node, []store.Edge, int, error) {
			cfg, loadErr := docwalk.LoadConfig(filepath.Join(root, ".memoryrc.yaml"))
			if loadErr != nil {
				return nil, nil, 0, fmt.Errorf("docwalk load config: %w", loadErr)
			}
			// Don't override cfg.ProjectID — docwalk's .memoryrc.yaml is the
			// source of truth. The caller's projectID (which may be "default"
			// if unset) would overwrite a meaningful name like
			// "example-vault" and create mismatched node IDs. Non-
			// docwalk adapters (codeast, gitrepo, obsidian, markdown-glob)
			// don't have their own config, so they still receive the caller's
			// projectID below.
			n, e, w, _, dwErr := docwalk.Ingest(root, cfg)
			return n, e, w, dwErr
		})
	}
	if exists(filepath.Join(root, ".obsidian")) {
		startAdapter("obsidian", func() ([]store.Node, []store.Edge, int, error) {
			n, e, obErr := obsidian.Ingest(obsidian.Config{Root: root, ProjectID: projectID})
			return n, e, countFiles(root, nil), obErr
		})
	}
	// markdown-glob is docwalk's untyped fallback: skip it when .memoryrc.yaml
	// exists, since docwalk already walks every .md file (typed via mappings,
	// or generic-doc via its catch-all). Running both races two adapters over
	// the same paths — same node ID, different node_type — and whichever
	// finishes last wins the UpsertNode ON CONFLICT, silently discarding the
	// configured type.
	if hasMD(root) && !exists(filepath.Join(root, ".memoryrc.yaml")) {
		startAdapter("markdown-glob", func() ([]store.Node, []store.Edge, int, error) {
			n, mdErr := markdownglob.Ingest(markdownglob.Config{Root: root, ProjectID: projectID})
			return n, nil, countFiles(root, nil), mdErr
		})
	}

	// ---- collect ----
	go func() { wg.Wait(); close(ch) }()

	for r := range ch {
		mu.Lock()
		adapters = append(adapters, r.name)
		filesWalked += r.walked
		if r.err != nil {
			err = r.err
			mu.Unlock()
			return // first adapter error fails fast
		}
		nodes = append(nodes, r.nodes...)
		edges = append(edges, r.edges...)
		mu.Unlock()
	}
	return
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

// EmbedNodes batch-embeds node content.
func EmbedNodes(ctx context.Context, db store.Store, nodes []store.Node, emb embed.EmbedderBackend, batchSize int) error {
	if batchSize <= 0 {
		batchSize = 32
	}
	var failed []string
	for i := 0; i < len(nodes); i += batchSize {
		end := i + batchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		batch := nodes[i:end]

		var texts []string
		var idx []int
		for j := range batch {
			if batch[j].Content != "" && batch[j].NodeType != "directory" {
				texts = append(texts, batch[j].Content)
				idx = append(idx, j)
			}
		}
		if len(texts) == 0 {
			continue
		}

		vecs, err := emb.EmbedBatch(ctx, texts)
		if err != nil {
			log.Printf("EmbedNodes: batch %d-%d failed (%v), skipping %d nodes", i, end, err, len(texts))
			for _, t := range texts {
				if len(failed) > 10 {
					break
				}
				failed = append(failed, truncateText(t, 60))
			}
			continue
		}

		for k, v := range vecs {
			batch[idx[k]].Embedding = v
			if err := db.UpsertNode(ctx, batch[idx[k]]); err != nil {
				return fmt.Errorf("upsert embed %s: %w", batch[idx[k]].ID, err)
			}
		}
	}
	if len(failed) > 0 {
		msg := fmt.Sprintf("EmbedNodes: %d batch(es) failed", len(failed))
		if len(failed) == 11 {
			msg += fmt.Sprintf(", first %d: %v ...and more", 10, failed[:10])
		} else {
			msg += ": " + strings.Join(failed, ", ")
		}
		log.Print(msg)
	}
	return nil
}

func truncateText(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + ".."
}

func InferConventions(ctx context.Context, db store.Store, projectID string, nodes []store.Node) []store.Node {
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
	var convEdges []store.Edge

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
			convEdges = append(convEdges, store.Edge{
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
			convEdges = append(convEdges, store.Edge{
				FromID:     n.ID,
				ToID:       m,
				EdgeType:   "convention-member",
				Confidence: 0.8,
			})
		}
	}

	if len(convEdges) > 0 {
		db.UpsertEdges(ctx, convEdges)
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

	tokens := make([][]string, len(newNodes))
	pathSegs := make([][]string, len(newNodes))
	for i, n := range newNodes {
		if n.Embedding == nil || n.NodeType == "directory" {
			continue
		}
		tokens[i] = tokenize(n.Title)
		if n.SourcePath != "" {
			pathSegs[i] = strings.Split(filepath.ToSlash(n.SourcePath), "/")
		}
	}

	var edges []store.Edge

	for i := 0; i < len(newNodes); i++ {
		if tokens[i] == nil {
			continue
		}
		for j := i + 1; j < len(newNodes); j++ {
			if tokens[j] == nil {
				continue
			}

			a := newNodes[i]
			b := newNodes[j]

			cos := cosineSimilarity(a.Embedding, b.Embedding)
			tc := typeCompat(a.NodeType, b.NodeType)
			pp := pathProximitySegs(pathSegs[i], pathSegs[j])
			io := identifierOverlapTokens(tokens[i], tokens[j])

			score := 0.4*cos + 0.2*tc + 0.2*pp + 0.2*io
			score += templateCollisionPenalty(a, b)
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

			edges = append(edges, store.Edge{
				FromID:     a.ID,
				ToID:       b.ID,
				EdgeType:   edgeType,
				Confidence: score,
			})
		}
	}

	if len(edges) > 0 {
		_ = db.UpsertEdges(ctx, edges)
	}

	return autoLinked, proposalsQueued
}

func identifierOverlapTokens(t1, t2 []string) float64 {
	if len(t1) == 0 || len(t2) == 0 {
		return 0
	}

	intersection := 0
	for _, t := range t1 {
		for _, u := range t2 {
			if t == u {
				intersection++
				break
			}
		}
	}

	union := len(t1) + len(t2) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func pathProximitySegs(segs1, segs2 []string) float64 {
	if len(segs1) == 0 || len(segs2) == 0 {
		return 0
	}

	shared := 0
	max := len(segs1)
	if len(segs2) < max {
		max = len(segs2)
	}

	for i := 0; i < max; i++ {
		if segs1[i] == segs2[i] {
			shared++
		} else {
			break
		}
	}

	if shared == 0 {
		return 0
	}
	depth := len(segs1)
	if len(segs2) > depth {
		depth = len(segs2)
	}
	return float64(shared) / float64(depth)
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
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
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
	return pathProximitySegs(
		strings.Split(filepath.ToSlash(p1), "/"),
		strings.Split(filepath.ToSlash(p2), "/"),
	)
}

func identifierOverlap(t1, t2 string) float64 {
	return identifierOverlapTokens(tokenize(t1), tokenize(t2))
}

// tokenize splits an identifier into words on camelCase/PascalCase/snake_case
func tokenize(s string) []string {
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, ".", " ")
	var words []string
	var sb strings.Builder

	for _, r := range s {
		if r == ' ' {
			if sb.Len() > 0 {
				words = append(words, strings.ToLower(sb.String()))
				sb.Reset()
			}
			continue
		}

		if r >= 'A' && r <= 'Z' && sb.Len() > 0 {
			cur := sb.String()
			last := cur[len(cur)-1]
			if last >= 'a' && last <= 'z' {
				words = append(words, strings.ToLower(cur))
				sb.Reset()
				sb.WriteRune(r)
				continue
			}
		}

		sb.WriteRune(r)
	}

	if sb.Len() > 0 {
		words = append(words, strings.ToLower(sb.String()))
	}
	return words
}

var docTypes = map[string]bool{
	"prd": true, "prd-detail": true, "erd": true, "api-spec": true,
	"test-case": true, "template": true, "foundation-doc": true,
	"design-doc": true, "generic-doc": true, "architecture-decision": true,
	"visual-design": true, "obsidian-note": true,
}

func templateCollisionPenalty(a, b store.Node) float64 {
	if !docTypes[a.NodeType] || !docTypes[b.NodeType] {
		return 0
	}
	if a.SourcePath == "" || b.SourcePath == "" {
		return 0
	}
	pa := filepath.ToSlash(a.SourcePath)
	pb := filepath.ToSlash(b.SourcePath)
	if filepath.Base(pa) != filepath.Base(pb) {
		return 0
	}
	if filepath.Dir(pa) == filepath.Dir(pb) {
		return 0
	}
	return -0.20
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
