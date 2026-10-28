// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// Long engine work (a check lasts up to half a minute, a scan several) runs as a job: one call starts it and hands back
// an id, another reads how far it is. A call to a peer plugin may last a minute at most.

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

const (
	maxRunningJobs = 2
	maxKeptJobs    = 24
	jobKeep        = 20 * time.Minute
)

var errBusy = errors.New("the WARP engine is busy with other jobs: try again in a minute")

// jobView is what a caller reads of a job.
type jobView struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`  // check or scan
	State    string    `json:"state"` // running, done, failed or canceled
	Stage    string    `json:"stage,omitempty"`
	Done     int       `json:"done"`
	Total    int       `json:"total"`
	Error    string    `json:"error,omitempty"`
	Result   any       `json:"result,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
}

// job is one piece of work and how far it got.
type job struct {
	mu     sync.Mutex
	v      jobView
	cancel context.CancelFunc
}

func (j *job) set(stage string, done, total int) {
	j.mu.Lock()
	j.v.Stage, j.v.Done, j.v.Total = stage, done, total
	j.mu.Unlock()
}

// view is a copy that can be encoded while the job moves on.
func (j *job) view() jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.v
}

type jobBoard struct {
	mu   sync.Mutex
	jobs map[string]*job
	now  func() time.Time
}

func newJobBoard() *jobBoard { return &jobBoard{jobs: map[string]*job{}, now: time.Now} }

// start runs fn as a job of the given kind and limits how many run at once.
func (b *jobBoard) start(kind string, limit time.Duration, fn func(ctx context.Context, j *job) (any, error)) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked()
	running := 0
	for _, j := range b.jobs {
		if j.view().State == "running" {
			running++
		}
	}
	if running >= maxRunningJobs {
		return "", errBusy
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	j := &job{v: jobView{ID: hex.EncodeToString(raw[:]), Kind: kind, State: "running", Started: b.now()}, cancel: cancel}
	b.jobs[j.v.ID] = j
	go func() {
		defer cancel()
		result, err := fn(ctx, j)
		j.mu.Lock()
		defer j.mu.Unlock()
		j.v.Finished = b.now()
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			j.v.State = "canceled"
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			j.v.State, j.v.Error = "failed", "the job took too long and was stopped"
		case err != nil:
			j.v.State, j.v.Error = "failed", err.Error()
		default:
			j.v.State, j.v.Result = "done", result
		}
	}()
	return j.v.ID, nil
}

func (b *jobBoard) get(id string) (jobView, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	j, ok := b.jobs[id]
	if !ok {
		return jobView{}, false
	}
	return j.view(), true
}

func (b *jobBoard) stop(id string) bool {
	b.mu.Lock()
	j, ok := b.jobs[id]
	b.mu.Unlock()
	if ok {
		j.cancel()
	}
	return ok
}

// sweepLocked forgets old finished jobs, and the oldest ones beyond maxKeptJobs.
func (b *jobBoard) sweepLocked() {
	now := b.now()
	var finished []*job
	for id, j := range b.jobs {
		v := j.view()
		if v.State == "running" {
			continue
		}
		if now.Sub(v.Finished) > jobKeep {
			delete(b.jobs, id)
			continue
		}
		finished = append(finished, j)
	}
	if len(b.jobs) <= maxKeptJobs {
		return
	}
	sort.Slice(finished, func(i, k int) bool { return finished[i].view().Finished.Before(finished[k].view().Finished) })
	for _, j := range finished {
		if len(b.jobs) <= maxKeptJobs {
			break
		}
		delete(b.jobs, j.v.ID)
	}
}
