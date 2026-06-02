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

type Factory interface {
	New(nodeID string, config panel.NodeConfig, users []panel.User, stats *state.Store) (Runtime, error)
}
