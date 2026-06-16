package kernel

import (
	"context"

	"hynode/internal/panel"
	"hynode/internal/state"
)

type Runtime interface {
	Start(context.Context) error
	Close() error
}

// HotReloader is an optional interface that a Runtime can implement to support
// user list updates without a full restart. This avoids disconnections when
// only the user list changes (e.g. new user added, user disabled).
type HotReloader interface {
	ReloadUsers(users []panel.User) error
}

type Factory interface {
	New(nodeID string, config panel.NodeConfig, users []panel.User, stats *state.Store) (Runtime, error)
}
