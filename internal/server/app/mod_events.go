package app

import (
	"opensignage/internal/server/core"
	"opensignage/internal/server/events"
)

func init() {
	Register(func(a *core.App) any { return events.New(a) })
}
