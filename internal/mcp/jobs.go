package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ingestJob tracks one async ingest run. Big-directory ingests (docwalk over
// a whole workspace) can take minutes — long enough to blow past any client
// or reverse-proxy HTTP timeout. Rather than hold the request open, POST
// /ingest/{adapter} returns a job_id immediately and the actual work runs in
// a goroutine; GET /ingest/jobs/{id} polls status.
type ingestJob struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"` // "running" | "done" | "error"
	Nodes        int       `json:"nodes_ingested,omitempty"`
	Edges        int       `json:"edges_created,omitempty"`
	Chunks       int       `json:"chunks_created,omitempty"`
	FilesWalked  int       `json:"files_walked,omitempty"`
	FilesSkipped int       `json:"files_skipped,omitempty"`
	Error        string    `json:"error,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitempty"`
}

// jobStore is an in-memory registry of ingest jobs. Deliberately not
// persisted — a job's whole purpose is "did the ingest I just kicked off
// finish", not a durable work queue. Restart the server, lose job history;
// the ingested nodes themselves are already durably in the DB by the time
// status flips to "done".
type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*ingestJob
}

func newJobStore() *jobStore {
	return &jobStore{jobs: map[string]*ingestJob{}}
}

func (js *jobStore) create() *ingestJob {
	js.mu.Lock()
	defer js.mu.Unlock()
	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	j := &ingestJob{ID: id, Status: "running", StartedAt: time.Now()}
	js.jobs[id] = j
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
func (js *jobStore) finish(id string, nodes, edges, chunks int, err error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, ok := js.jobs[id]
	if !ok {
		return
	}
	j.EndedAt = time.Now()
	if err != nil {
		j.Status = "error"
		j.Error = err.Error()
		return
	}
	j.Status = "done"
	j.Nodes, j.Edges, j.Chunks = nodes, edges, chunks
}

// updateCounts lets the background goroutine push progress (files walked/skipped)
// before the job completes. Caller must hold no other locks.
func (js *jobStore) updateCounts(id string, filesWalked, filesSkipped int) {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, ok := js.jobs[id]
	if !ok {
		return
	}
	j.FilesWalked, j.FilesSkipped = filesWalked, filesSkipped
}

// updateChunksEmbedded lets the background goroutine push embedding progress.
func (js *jobStore) updateChunksEmbedded(id string, chunksEmbedded int) {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, ok := js.jobs[id]
	if !ok {
		return
	}
	j.Chunks = chunksEmbedded
}

// runAsync kicks off ingest+chunk+embed in a background goroutine using a
// context detached from the originating HTTP request (so client disconnect
// / request-scoped cancellation doesn't kill a long ingest mid-flight).
func (js *jobStore) runAsync(job *ingestJob, work func(ctx context.Context) (nodes, edges, chunks int, err error)) {
	go func() {
		nodes, edges, chunks, err := work(context.Background())
		js.finish(job.ID, nodes, edges, chunks, err)
	}()
}