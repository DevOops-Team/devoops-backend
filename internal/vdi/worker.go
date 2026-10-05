package vdi

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"time"
)

type Worker struct {
	Store                                 *Store
	Cloud                                 Cloud
	Now                                   func() time.Time
	Interval, ReadyTimeout, LeaseDuration time.Duration
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		now := w.Now().UTC().Truncate(time.Millisecond)
		n, e := w.Store.C("desktops").CountDocuments(ctx, bson.M{"deleted": false, "phase": bson.M{"$ne": "done"}, "nextRun": bson.M{"$lte": now}, "leaseUntil": bson.M{"$lte": now}})
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		for range n {
			if e := w.Tick(ctx); e != nil {
				if ctx.Err() != nil {
					return nil
				}
				return e
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (w *Worker) Tick(ctx context.Context) error {
	now := w.Now().UTC().Truncate(time.Millisecond)
	lease := uuid.NewString()
	var d Desktop
	e := w.Store.C("desktops").FindOneAndUpdate(ctx, bson.M{"deleted": false, "phase": bson.M{"$ne": "done"}, "nextRun": bson.M{"$lte": now}, "leaseUntil": bson.M{"$lte": now}}, bson.M{"$set": bson.M{"lease": lease, "leaseUntil": now.Add(w.LeaseDuration)}}, options.FindOneAndUpdate().SetSort(bson.D{{Key: "nextRun", Value: 1}}).SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return nil
	}
	if e != nil {
		return e
	}
	event := ""
	original := d.Revision
	// Submission is durable BEFORE Nova POST. Recovered submissions only reconcile,
	// including an empty listing, which cannot prove an in-flight POST never committed.
	if d.Phase == "queued" {
		d.Submitted = true
		d.Phase = "submitted"
		result, e := w.Store.C("desktops").UpdateOne(ctx, bson.M{"id": d.ID, "lease": lease, "revision": original, "deleteRequested": false}, bson.M{"$set": bson.M{"submitted": true, "phase": "submitted"}})
		if e != nil {
			return e
		}
		if result.ModifiedCount == 0 {
			return w.release(ctx, d, original)
		}
		vmID, createErr := w.Cloud.Create(ctx, d)
		if createErr == nil || errors.Is(createErr, ErrCreateRejected) {
			// An API deletion may have changed revision during Nova POST. Preserve
			// the submission result under the lease, without reverting that deletion.
			resultErr := w.Store.Tx(ctx, func(c context.Context) error {
				var fresh Desktop
				if e := w.Store.C("desktops").FindOne(c, bson.M{"id": d.ID, "lease": lease}).Decode(&fresh); e != nil {
					return e
				}
				fresh.Resolved = true
				if createErr == nil {
					fresh.VMID = vmID
					if !fresh.DeleteRequested {
						fresh.Phase = "monitor"
					}
				} else {
					fresh.Submitted = false
					fresh.Failure = &Failure{"OPENSTACK_CREATE_FAILED", "VM 생성 요청이 거부되었습니다.", true}
					if !fresh.DeleteRequested {
						fresh.Status = "ERROR"
						fresh.ConnectionState = "UNAVAILABLE"
						fresh.Phase = "cleanup"
					}
				}
				if _, e := w.Store.C("desktops").ReplaceOne(c, bson.M{"id": fresh.ID, "lease": lease}, fresh); e != nil {
					return e
				}
				if createErr != nil {
					if e := w.Store.Event(c, w.Store.SystemID, &fresh.ID, "DESKTOP_CREATE_FAILED", "DESKTOP", "VM 생성 요청 거부", now); e != nil {
						return e
					}
				}
				d = fresh
				return nil
			})
			if errors.Is(resultErr, mongo.ErrNoDocuments) {
				return nil
			}
			if resultErr != nil {
				return resultErr
			}
			original = d.Revision
		} else {
			d.Failure = &Failure{"OPENSTACK_CREATE_FAILED", "VM 생성 결과를 확인 중입니다.", true}
		}
	}
	if d.VMID == "" && d.Submitted {
		list, e := w.Cloud.Find(ctx, d.ID)
		if e != nil {
			if now.Sub(d.CreatedAt) >= w.ReadyTimeout && d.Status != "ERROR" {
				d.Status = "ERROR"
				d.ConnectionState = "UNAVAILABLE"
				d.Failure = &Failure{"OPENSTACK_CREATE_FAILED", "VM 생성 결과가 불확실하여 복구 중입니다.", true}
				return w.save(ctx, d, original, "DESKTOP_CREATE_FAILED", false)
			}
			return w.save(ctx, d, original, event, false)
		}
		if len(list) == 1 {
			d.VMID = list[0].ID
			d.Resolved = true
			if !d.DeleteRequested && d.Phase != "cleanup" {
				d.Phase = "monitor"
				d.Failure = nil
			}
		} else if len(list) > 1 {
			d.Resolved = true
			if d.Status != "ERROR" {
				event = "DESKTOP_CREATE_FAILED"
			}
			d.Status = "ERROR"
			d.ConnectionState = "UNAVAILABLE"
			d.Failure = &Failure{"RESOURCE_CLEANUP_FAILED", "중복 자원을 정리 중입니다.", true}
			d.Phase = "cleanup"
			claimed := false
			e := w.Store.Tx(ctx, func(c context.Context) error {
				claimed = false
				result, e := w.Store.C("desktops").UpdateOne(c, bson.M{"id": d.ID, "revision": original, "lease": lease}, bson.M{"$set": bson.M{"resolved": true, "phase": d.Phase, "status": d.Status, "connectionState": d.ConnectionState, "failure": d.Failure}})
				if e != nil {
					return e
				}
				if result.MatchedCount == 0 {
					return nil
				}
				claimed = true
				if event != "" {
					return w.Store.Event(c, w.Store.SystemID, &d.ID, event, "DESKTOP", event, now)
				}
				return nil
			})
			if e != nil {
				return e
			}
			if !claimed {
				return w.release(ctx, d, original)
			}
			event = ""
			for _, vm := range list {
				_ = w.Cloud.Delete(ctx, vm.ID)
			}
		} else {
			if d.Resolved && (d.Phase == "cleanup" || d.DeleteRequested) {
				return w.finishWithEvent(ctx, d, original, event)
			}
			if now.Sub(d.CreatedAt) >= w.ReadyTimeout && d.Status != "ERROR" {
				d.Status = "ERROR"
				d.ConnectionState = "UNAVAILABLE"
				d.Failure = &Failure{"OPENSTACK_CREATE_FAILED", "VM 생성 결과가 불확실하여 복구 중입니다.", true}
				event = "DESKTOP_CREATE_FAILED"
			}
			// Keep reservation and reconciliation until a VM can be identified.
			return w.save(ctx, d, original, event, false)
		}
	}
	if d.DeleteRequested || d.Phase == "cleanup" {
		if d.VMID == "" && !d.Submitted {
			return w.finishWithEvent(ctx, d, original, event)
		}
		if d.VMID == "" {
			return w.save(ctx, d, original, event, false)
		}
		vm, e := w.Cloud.Get(ctx, d.VMID)
		if errors.Is(e, ErrVMNotFound) {
			// Metadata scan also catches extra resources before quota release.
			list, e := w.Cloud.Find(ctx, d.ID)
			if e != nil {
				return w.release(ctx, d, original)
			}
			if len(list) == 0 {
				return w.finishWithEvent(ctx, d, original, event)
			}
			d.VMID = list[0].ID
			return w.save(ctx, d, original, event, false)
		}
		if e != nil {
			return w.release(ctx, d, original)
		}
		_ = vm
		if e = w.Cloud.Delete(ctx, d.VMID); e != nil {
			code := "RESOURCE_CLEANUP_FAILED"
			if d.DeleteRequested {
				code = "DESKTOP_DELETE_FAILED"
			}
			if d.Failure == nil || d.Failure.Code != code {
				event = "DESKTOP_DELETE_FAILED"
			}
			d.Status = "ERROR"
			d.ConnectionState = "UNAVAILABLE"
			d.Failure = &Failure{code, "VM 정리에 실패하여 다시 시도합니다.", true}
		}
		return w.save(ctx, d, original, event, false)
	}
	if d.VMID != "" {
		vm, e := w.Cloud.Get(ctx, d.VMID)
		if errors.Is(e, ErrVMNotFound) {
			d.Status = "ERROR"
			d.ConnectionState = "UNAVAILABLE"
			d.Failure = &Failure{"OPENSTACK_CREATE_FAILED", "VM을 찾을 수 없습니다.", true}
			d.Phase = "cleanup"
			event = "DESKTOP_CREATE_FAILED"
			return w.save(ctx, d, original, event, false)
		}
		if e != nil {
			if d.Phase == "monitor" && now.Sub(d.CreatedAt) >= w.ReadyTimeout {
				d.Status = "ERROR"
				d.ConnectionState = "UNAVAILABLE"
				d.Phase = "cleanup"
				d.Failure = &Failure{"OPENSTACK_CREATE_FAILED", "VM 준비 제한시간 내 상태를 확인할 수 없습니다.", true}
				return w.save(ctx, d, original, "DESKTOP_CREATE_FAILED", false)
			}
			return w.release(ctx, d, original)
		}
		if vm.Node != "" {
			d.NodeName = &vm.Node
		}
		switch vm.Status {
		case "ACTIVE":
			d.Status = "RUNNING"
			if w.Cloud.Ready(ctx, vm.IP) {
				d.ConnectionState = "READY"
				d.Phase = "watch"
				d.Failure = nil
			} else {
				d.ConnectionState = "PENDING"
				if d.Phase == "watch" {
					d.ConnectionState = "UNAVAILABLE"
				}
			}
		case "SHUTOFF", "SUSPENDED", "PAUSED":
			d.Status = "STOPPED"
			d.ConnectionState = "UNAVAILABLE"
		case "ERROR":
			d.Status = "ERROR"
			d.ConnectionState = "UNAVAILABLE"
			d.Failure = &Failure{"OPENSTACK_CREATE_FAILED", "VM 생성에 실패했습니다.", true}
			d.Phase = "cleanup"
			event = "DESKTOP_CREATE_FAILED"
		default:
			d.Status = "CREATING"
			d.ConnectionState = "PENDING"
		}
		if d.Phase == "monitor" && now.Sub(d.CreatedAt) >= w.ReadyTimeout {
			d.Status = "ERROR"
			d.ConnectionState = "UNAVAILABLE"
			code := "OPENSTACK_CREATE_FAILED"
			if vm.Status == "ACTIVE" {
				code = "RDP_READY_TIMEOUT"
			}
			d.Failure = &Failure{code, "VM 또는 RDP 준비 제한시간을 초과했습니다.", true}
			d.Phase = "cleanup"
			event = "DESKTOP_CREATE_FAILED"
		}
	}
	return w.save(ctx, d, original, event, false)
}
func (w *Worker) release(ctx context.Context, d Desktop, revision int64) error {
	_, e := w.Store.C("desktops").UpdateOne(ctx, bson.M{"id": d.ID, "lease": d.Lease}, bson.M{"$set": bson.M{"leaseUntil": time.Time{}, "nextRun": w.Now().UTC().Truncate(time.Millisecond).Add(w.Interval)}})
	return e
}
func (w *Worker) finishWithEvent(ctx context.Context, d Desktop, revision int64, event string) error {
	if d.DeleteRequested {
		d.Deleted = true
		event = "DESKTOP_DELETE"
	}
	d.Phase = "done"
	d.ConnectionState = "UNAVAILABLE"
	d.CanConnect = false
	return w.save(ctx, d, revision, event, true)
}
func (w *Worker) save(ctx context.Context, d Desktop, revision int64, event string, releaseQuota bool) error {
	wasReserved := d.Reserved
	return w.Store.Tx(ctx, func(c context.Context) error {
		now := w.Now().UTC().Truncate(time.Millisecond)
		d.UpdatedAt = now
		d.NextRun = now.Add(w.Interval)
		d.LeaseUntil = time.Time{}
		d.CanConnect = d.Status == "RUNNING" && d.ConnectionState == "READY"
		d.Revision = revision + 1
		reserved := wasReserved
		if releaseQuota {
			d.Reserved = false
		}
		result, e := w.Store.C("desktops").ReplaceOne(c, bson.M{"id": d.ID, "revision": revision, "lease": d.Lease}, d)
		if e != nil {
			return e
		}
		if result.ModifiedCount == 0 {
			return w.release(c, d, revision)
		}
		if releaseQuota && reserved {
			if _, e = w.Store.C("users").UpdateOne(c, bson.M{"id": d.UserID}, bson.M{"$inc": bson.M{"reserved": -1, "version": 1}}); e != nil {
				return e
			}
		}
		if event != "" {
			actor := w.Store.SystemID
			if event == "DESKTOP_DELETE" && d.DeleteActor > 0 {
				actor = d.DeleteActor
			}
			return w.Store.Event(c, actor, &d.ID, event, "DESKTOP", event, now)
		}
		return nil
	})
}
