package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mystaline-dev/tastastas/internal/store"
)

// ingestMu serializes access to the embedder for ingest jobs. Only one
// goroutine calls chunkAndEmbedNodes at a time; others set their job phase
// to "waiting" so callers see the queue position.
var ingestMu sync.Mutex

// ingestJob tracks one async ingest run. Big-directory ingests (docwalk over
// a whole workspace) can take minutes — long enough to blow past any client
// or reverse-proxy HTTP timeout. Rather than hold the request open, POST
// /ingest/{adapter} returns a job_id immediately and the actual work runs in
// a goroutine; GET /ingest/jobs/{id} polls status.
type ingestJob struct {
	ID              string             `json:"id"`
	Status          string             `json:"status"`          // "running" | "done" | "error"
	Phase           string             `json:"phase,omitempty"` // "walking" | "syncing" | "waiting" | "chunking" | "embedding" | "persisting" | "linking" | "" (done/error)
	Nodes           int                `json:"nodes_ingested,omitempty"`
	Edges           int                `json:"edges_created,omitempty"`
	Chunks          int                `json:"chunks_created,omitempty"`
	ChunksTotal     int                `json:"chunks_total,omitempty"`
	Conventions     int                `json:"conventions_inferred,omitempty"`
	AutoLinked      int                `json:"auto_linked,omitempty"`
	ProposalsQueued int                `json:"proposals_queued,omitempty"`
	FilesWalked     int                `json:"files_walked,omitempty"`
	FilesSkipped    int                `json:"files_skipped,omitempty"`
	Error           string             `json:"error,omitempty"`
	StartedAt       time.Time          `json:"started_at"`
	EndedAt         time.Time          `json:"ended_at,omitempty"`
	cancel          context.CancelFunc // for job cancellation
}

// jobStore is an in-memory registry of ingest jobs. Deliberately not
// persisted — a job's whole purpose is "did the ingest I just kicked off
// finish", not a durable work queue. Restart the server, lose job history;
// the ingested nodes themselves are already durably in the DB by the time
// status flips to "done".
type jobStore struct {
	store store.Store // for interrupted-run marker
	mu    sync.RWMutex
	jobs  map[string]*ingestJob
}

func newJobStore(st store.Store) *jobStore {
	return &jobStore{store: st, jobs: map[string]*ingestJob{}}
}

func (js *jobStore) create() *ingestJob {
	js.mu.Lock()
	defer js.mu.Unlock()

	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx // used by cancel

	j := &ingestJob{
		ID:        id,
		Status:    "running",
		Phase:     "walking",
		StartedAt: time.Now(),
		cancel:    cancel,
	}
	js.jobs[id] = j

	// Record interrupted-run marker. Cleared by finish().
	if js.store != nil {
		_ = js.store.SetJobMarker(context.Background())
	}
	return j
}

func (js *jobStore) get(id string) (ingestJob, bool) {
	js.mu.RLock()
	defer js.mu.RUnlock()

	j, ok := js.jobs[id]
	if !ok {
		return ingestJob{}, false
	}
	return *j, true // copy: caller must not see mutations after unlock
}

// finish marks the job done/error and updates final counts.
func (js *jobStore) finish(
	id string,
	nodes, edges, chunks int,
	err error,
	extra ...int,
) {
	js.mu.Lock()
	defer js.mu.Unlock()

	j, ok := js.jobs[id]
	if !ok {
		return
	}
	j.EndedAt = time.Now()

	j.Phase = ""
	if err != nil {
		j.Status = "error"
		j.Error = err.Error()
	} else {
		j.Status = "done"
		j.Nodes, j.Edges, j.Chunks = nodes, edges, chunks
		if len(extra) >= 3 {
			j.Conventions, j.AutoLinked, j.ProposalsQueued = extra[0], extra[1], extra[2]
		}
	}

	// Cancel any ongoing work
	if j.cancel != nil {
		j.cancel()
	}

	// Clear interrupted-run marker — job finished cleanly.
	if js.store != nil {
		_ = js.store.ClearJobMarker(context.Background())
	}
}

// updateCounts lets the background goroutine push progress (files walked/skipped)
// before the job completes. Also flips phase to "syncing" — the walk is
// done and upserting nodes+edges to DB is about to start.
func (js *jobStore) updateCounts(
	id string,
	filesWalked, filesSkipped int,
) {
	js.mu.Lock()
	defer js.mu.Unlock()

	j, ok := js.jobs[id]
	if !ok {
		return
	}
	j.FilesWalked, j.FilesSkipped = filesWalked, filesSkipped
	j.Phase = "syncing"
}

// updateChunksEmbedded lets the background goroutine push embedding progress.
// chunksTotal is set once, on the first call, from the caller's known batch size.
func (js *jobStore) updateChunksEmbedded(id string, chunksEmbedded, chunksTotal int) {
	js.mu.Lock()
	defer js.mu.Unlock()

	j, ok := js.jobs[id]
	if !ok {
		return
	}
	j.Chunks = chunksEmbedded
	if chunksTotal > 0 {
		j.ChunksTotal = chunksTotal
	}
}

// updatePhase lets the background goroutine mark a phase transition that
// isn't tied to a count update — e.g. "persisting" once all chunks are
// embedded and the bulk DB insert (which reports no progress of its own)
// is about to start. Without this, a multi-minute insert phase is
// indistinguishable from a hang to anyone polling.
func (js *jobStore) updatePhase(id, phase string) {
	js.mu.Lock()
	defer js.mu.Unlock()

	j, ok := js.jobs[id]
	if !ok {
		return
	}
	j.Phase = phase
}

// runAsync kicks off ingest+chunk+embed in a background goroutine using a
// context detached from the originating HTTP request (so client disconnect
// / request-scoped cancellation doesn't kill a long ingest mid-flight).
// The returned context can be cancelled by calling js.cancelJob(id).
func (js *jobStore) runAsync(
	job *ingestJob,
	work func(ctx context.Context) (
		nodes, edges, chunks int,
		err error,
	),
) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		nodes, edges, chunks, err := work(ctx)
		js.finish(job.ID, nodes, edges, chunks, err)
	}()

	return cancel
}
