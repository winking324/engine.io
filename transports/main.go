package transports

import (
	"github.com/zishang520/engine.io/v2/types"
)

type transports struct {
	New             func(*types.HttpContext) Transport
	HandlesUpgrades bool
	UpgradesTo      *types.Set[string]
}

var _transports map[string]*transports

func init() {
	_transports = map[string]*transports{
		"polling": {
			// Polling polymorphic New.
			New: func(ctx *types.HttpContext) Transport {
				if ctx.Query().Has("j") {
					return NewJSONP(ctx)
				}
				return NewPolling(ctx)
			},
			HandlesUpgrades: false,
			UpgradesTo:      types.NewSet("websocket", "webtransport"),
		},

		"websocket": {
			New: func(ctx *types.HttpContext) Transport {
				return NewWebSocket(ctx)
			},
			HandlesUpgrades: true,
			UpgradesTo:      types.NewSet[string](),
		},

		"webtransport": {
			New: func(ctx *types.HttpContext) Transport {
				return NewWebTransport(ctx)
			},
			HandlesUpgrades: true,
			UpgradesTo:      types.NewSet[string](),
		},
	}
}

func Transports() map[string]*transports {
	return _transports
}
