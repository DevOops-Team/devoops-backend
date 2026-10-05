package vdi

import "time"

const MaxID int64 = 9007199254740991

type User struct {
	ID           int64     `json:"id" bson:"id"`
	Name         string    `json:"name" bson:"name"`
	Email        string    `json:"email" bson:"email"`
	Role         string    `json:"role" bson:"role"`
	CreatedAt    time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt" bson:"updatedAt"`
	PasswordHash []byte    `json:"-" bson:"passwordHash"`
	Deleted      bool      `json:"-" bson:"deleted"`
	Internal     bool      `json:"-" bson:"internal"`
	Reserved     int       `json:"-" bson:"reserved"`
	Version      int64     `json:"-" bson:"version"`
}
type OS struct {
	ID      int64  `json:"id" bson:"id"`
	Name    string `json:"name" bson:"name"`
	Type    string `json:"type" bson:"type"`
	Version string `json:"version" bson:"version"`
	ImageID string `json:"-" bson:"imageId"`
}
type Failure struct {
	Code      string `json:"code" bson:"code"`
	Message   string `json:"message" bson:"message"`
	Retryable bool   `json:"retryable" bson:"retryable"`
}
type DesktopResponse struct {
	ID              int64     `json:"id" bson:"id"`
	UserID          int64     `json:"userId" bson:"userId"`
	Name            string    `json:"name" bson:"name"`
	OSID            int64     `json:"osId" bson:"osId"`
	CPUCores        int       `json:"cpuCores" bson:"cpuCores"`
	MemoryGB        int       `json:"memoryGb" bson:"memoryGb"`
	StorageGB       int       `json:"storageGb" bson:"storageGb"`
	OS              OS        `json:"os" bson:"os"`
	Status          string    `json:"status" bson:"status"`
	NodeName        *string   `json:"nodeName" bson:"nodeName"`
	ConnectionState string    `json:"connectionState" bson:"connectionState"`
	CanConnect      bool      `json:"canConnect" bson:"canConnect"`
	Failure         *Failure  `json:"failure" bson:"failure"`
	CreatedAt       time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt" bson:"updatedAt"`
}
type Desktop struct {
	DesktopResponse `bson:",inline"`
	VMID            string    `bson:"vmId"`
	ImageID         string    `bson:"imageId"`
	FlavorID        string    `bson:"flavorId"`
	Phase           string    `bson:"phase"`
	Resolved        bool      `bson:"resolved"`
	Submitted       bool      `bson:"submitted"`
	Deleted         bool      `bson:"deleted"`
	Reserved        bool      `bson:"reserved"`
	DeleteRequested bool      `bson:"deleteRequested"`
	DeleteActor     int64     `bson:"deleteActor"`
	Lease           string    `bson:"lease"`
	LeaseUntil      time.Time `bson:"leaseUntil"`
	NextRun         time.Time `bson:"nextRun"`
	Revision        int64     `bson:"revision"`
}

func (d Desktop) Response(actor int64) DesktopResponse {
	r := d.DesktopResponse
	r.OS.ImageID = ""
	r.CanConnect = r.Status == "RUNNING" && r.ConnectionState == "READY" && actor == r.UserID
	return r
}

type Event struct {
	ID         int64     `json:"id" bson:"id"`
	ActorID    int64     `json:"actorId" bson:"actorId"`
	DesktopID  *int64    `json:"desktopId" bson:"desktopId"`
	Action     string    `json:"action" bson:"action"`
	TargetType *string   `json:"targetType" bson:"targetType"`
	Detail     *string   `json:"detail" bson:"detail"`
	CreatedAt  time.Time `json:"createdAt" bson:"createdAt"`
}
type Session struct {
	Hash      string    `bson:"hash"`
	UserID    int64     `bson:"userId"`
	ExpiresAt time.Time `bson:"expiresAt"`
}
type Idempotency struct {
	ActorID     int64           `bson:"actorId"`
	Key         string          `bson:"key"`
	Fingerprint string          `bson:"fingerprint"`
	Response    DesktopResponse `bson:"response"`
	ExpiresAt   time.Time       `bson:"expiresAt"`
}
type CreateRequest struct {
	Name      string `json:"name"`
	OSID      int64  `json:"osId"`
	CPUCores  int    `json:"cpuCores"`
	MemoryGB  int    `json:"memoryGb"`
	StorageGB int    `json:"storageGb"`
}
type Spec struct {
	FlavorID                      string
	CPUCores, MemoryGB, StorageGB int
}
type VM struct{ ID, Status, IP, Node string }
type Cloud interface {
	Spec(ctx Context, image string) (Spec, error)
	Create(ctx Context, d Desktop) (string, error)
	Find(ctx Context, desktopID int64) ([]VM, error)
	Get(ctx Context, id string) (VM, error)
	Delete(ctx Context, id string) error
	Ready(ctx Context, ip string) bool
}
