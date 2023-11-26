// Copyright (c) 2023 BVK Chaitanya

package server

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"strings"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/dbutil"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/job"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvk/tradebot/looper"
	"github.com/bvk/tradebot/runtime"
	"github.com/bvk/tradebot/waller"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
)

// createJob creates a job instance for the given trader id. Current state of
// the job is fetched from the database. Returns true if the job requires a
// manual resume request from the user.
func (t *Trader) createJob(ctx context.Context, id string) (*job.Job, bool, error) {
	key := path.Join(JobsKeyspace, id)
	gstate, err := dbutil.Get[gobs.TraderJobState](ctx, t.db, key)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}
	}
	var state job.State
	if gstate.CurrentState != "" {
		state = job.State(gstate.CurrentState)
	}

	type TradeJob interface {
		UID() string
		ProductID() string
		ExchangeName() string
		Run(context.Context, *runtime.Runtime) error
	}

	var v TradeJob
	if limit, ok := t.limiterMap.Load(id); ok {
		v = limit
	} else if loop, ok := t.looperMap.Load(id); ok {
		v = loop
	} else if wall, ok := t.wallerMap.Load(id); ok {
		v = wall
	} else {
		return nil, false, fmt.Errorf("job %s not found: %w", id, os.ErrNotExist)
	}

	ename, pname := v.ExchangeName(), v.ProductID()
	product, err := t.getProduct(ctx, ename, pname)
	if err != nil {
		return nil, false, fmt.Errorf("could not load product %q in exchange %q: %w", pname, ename, err)
	}

	j := job.New(state, func(ctx context.Context) error {
		return v.Run(ctx, &runtime.Runtime{Product: product, Database: t.db})
	})
	return j, gstate.NeedsManualResume, nil
}

// doPause pauses a running job. If the job is not running and is not final
// it's state is updated to manually-paused state.
func (t *Trader) doPause(ctx context.Context, req *api.JobPauseRequest) (*api.JobPauseResponse, error) {
	j, ok := t.jobMap.Load(req.UID)
	if !ok {
		v, _, err := t.createJob(ctx, req.UID)
		if err != nil {
			return nil, fmt.Errorf("job %s not found: %w", req.UID, os.ErrNotExist)
		}
		j = v
	}

	if job.IsFinal(j.State()) {
		resp := &api.JobPauseResponse{
			FinalState: string(j.State()),
		}
		return resp, nil
	}

	if err := j.Pause(); err != nil {
		return nil, fmt.Errorf("could not pause job %s: %w", req.UID, err)
	}

	gstate := &gobs.TraderJobState{
		CurrentState:      string(j.State()),
		NeedsManualResume: true,
	}
	key := path.Join(JobsKeyspace, req.UID)
	if err := dbutil.Set(ctx, t.db, key, gstate); err != nil {
		return nil, err
	}
	t.jobMap.Delete(req.UID)

	resp := &api.JobPauseResponse{
		FinalState: gstate.CurrentState,
	}
	return resp, nil
}

// doResume resumes a non-final job.
func (t *Trader) doResume(ctx context.Context, req *api.JobResumeRequest) (*api.JobResumeResponse, error) {
	j, ok := t.jobMap.Load(req.UID)
	if !ok {
		v, _, err := t.createJob(ctx, req.UID)
		if err != nil {
			return nil, fmt.Errorf("job %s not found: %w", req.UID, os.ErrNotExist)
		}
		j = v
	}

	if job.IsFinal(j.State()) {
		resp := &api.JobResumeResponse{
			FinalState: string(j.State()),
		}
		return resp, nil
	}

	if err := j.Resume(t.closeCtx); err != nil {
		return nil, err
	}

	gstate := &gobs.TraderJobState{
		CurrentState:      string(j.State()),
		NeedsManualResume: false,
	}
	key := path.Join(JobsKeyspace, req.UID)
	if err := dbutil.Set(ctx, t.db, key, gstate); err != nil {
		return nil, err
	}
	t.jobMap.Store(req.UID, j)

	resp := &api.JobResumeResponse{
		FinalState: gstate.CurrentState,
	}
	return resp, nil
}

// doCancel cancels a non-final job. If job is running, it will be stopped.
func (t *Trader) doCancel(ctx context.Context, req *api.JobCancelRequest) (*api.JobCancelResponse, error) {
	j, ok := t.jobMap.Load(req.UID)
	if !ok {
		v, _, err := t.createJob(ctx, req.UID)
		if err != nil {
			return nil, fmt.Errorf("job %s not found: %w", req.UID, os.ErrNotExist)
		}
		j = v
	}

	if job.IsFinal(j.State()) {
		resp := &api.JobCancelResponse{
			FinalState: string(j.State()),
		}
		return resp, nil
	}

	if err := j.Cancel(); err != nil {
		return nil, err
	}

	gstate := &gobs.TraderJobState{
		CurrentState: string(j.State()),
	}
	key := path.Join(JobsKeyspace, req.UID)
	if err := dbutil.Set(ctx, t.db, key, gstate); err != nil {
		return nil, err
	}
	t.jobMap.Delete(req.UID)

	resp := &api.JobCancelResponse{
		FinalState: gstate.CurrentState,
	}
	return resp, nil
}

func (t *Trader) doList(ctx context.Context, req *api.JobListRequest) (*api.JobListResponse, error) {
	getState := func(id string) job.State {
		if j, ok := t.jobMap.Load(id); ok {
			return j.State()
		}
		key := path.Join(JobsKeyspace, id)
		v, err := dbutil.Get[gobs.TraderJobState](ctx, t.db, key)
		if err != nil {
			log.Printf("could not fetch job state for %s (ignored): %v", id, err)
			return ""
		}
		return job.State(v.CurrentState)
	}

	resp := new(api.JobListResponse)
	t.limiterMap.Range(func(id string, l *limiter.Limiter) bool {
		name, _ := t.idNameMap.Load(id)
		resp.Jobs = append(resp.Jobs, &api.JobListResponseItem{
			UID:   id,
			Type:  "Limiter",
			State: string(getState(id)),
			Name:  name,
		})
		return true
	})
	t.looperMap.Range(func(id string, l *looper.Looper) bool {
		name, _ := t.idNameMap.Load(id)
		resp.Jobs = append(resp.Jobs, &api.JobListResponseItem{
			UID:   id,
			Type:  "Looper",
			State: string(getState(id)),
			Name:  name,
		})
		return true
	})
	t.wallerMap.Range(func(id string, w *waller.Waller) bool {
		name, _ := t.idNameMap.Load(id)
		resp.Jobs = append(resp.Jobs, &api.JobListResponseItem{
			UID:   id,
			Type:  "Waller",
			State: string(getState(id)),
			Name:  name,
		})
		return true
	})
	return resp, nil
}

func (t *Trader) doRename(ctx context.Context, req *api.JobRenameRequest) (*api.JobRenameResponse, error) {
	if err := req.Check(); err != nil {
		return nil, fmt.Errorf("invalid rename request: %w", err)
	}

	// TODO: If NewName is empty, we should just delete the OldName.

	uid := req.UID

	rename := func(ctx context.Context, rw kv.ReadWriter) error {
		if len(uid) == 0 {
			checksum := md5.Sum([]byte(req.OldName))
			key := path.Join(NamesKeyspace, uuid.UUID(checksum).String())
			old, err := kvutil.Get[gobs.NameData](ctx, rw, key)
			if err != nil {
				return fmt.Errorf("could not load old name data: %w", err)
			}
			if !strings.HasPrefix(old.Data, JobsKeyspace) {
				return fmt.Errorf("old name is not a job: %w", os.ErrInvalid)
			}
			if err := rw.Delete(ctx, key); err != nil {
				return fmt.Errorf("could not delete old name: %w", err)
			}
			uid = strings.TrimPrefix(old.Data, JobsKeyspace)
		}

		// Only valid jobs can be named.
		if _, ok := t.jobMap.Load(uid); !ok {
			if _, _, err := t.createJob(ctx, uid); err != nil {
				return fmt.Errorf("job %s not found: %w", uid, os.ErrNotExist)
			}
		}

		checksum := md5.Sum([]byte(req.NewName))
		nkey := path.Join(NamesKeyspace, uuid.UUID(checksum).String())
		if _, err := rw.Get(ctx, nkey); err == nil {
			return fmt.Errorf("new name already exists: %w", os.ErrExist)
		}

		jkey := path.Join(JobsKeyspace, uid)
		state, err := kvutil.Get[gobs.TraderJobState](ctx, rw, jkey)
		if err != nil {
			return fmt.Errorf("could not load job state: %w", err)
		}
		state.JobName = req.NewName
		if err := kvutil.Set(ctx, rw, jkey, state); err != nil {
			return fmt.Errorf("could not update job name: %w", err)
		}

		v := &gobs.NameData{
			Name: req.NewName,
			Data: path.Join(JobsKeyspace, uid),
		}
		if err := kvutil.Set(ctx, rw, nkey, v); err != nil {
			return fmt.Errorf("could not set new name: %w", err)
		}

		return nil
	}
	if err := kv.WithReadWriter(ctx, t.db, rename); err != nil {
		return nil, err
	}

	t.idNameMap.Store(uid, req.NewName)
	return &api.JobRenameResponse{UID: uid}, nil
}