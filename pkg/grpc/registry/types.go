package registry

import (
	"context"
	"io"
)

type Registry interface {
	Register(ctx context.Context, si ServiceInstance) error
	UnRegister(ctx context.Context, si ServiceInstance) error

	ListServices(ctx context.Context, name string) ([]ServiceInstance, error)
	Subscribe(name string) <-chan Event

	io.Closer
}

type ServiceInstance struct {
	Name         string // im
	Address      string // 1.1.0.129
	ID           string // 01
	InitCapacity int64
	MaxCapacity  int64
	IncreaseStep int64
	GrowthRate   float64
}

type EventType int

const (
	EventTypeUnknown EventType = iota
	EventTypeAdd
	EventTypeDelete
)

func (e EventType) IsAdd() bool {
	return e == EventTypeAdd
}

func (e EventType) IsDelete() bool {
	return e == EventTypeDelete
}

type Event struct {
	Type     EventType
	Instance ServiceInstance
}
