package app

import (
	"opensignage/internal/server/core"
	"opensignage/internal/server/updates"
)

func init() { Register(func(a *core.App) any { return updates.New(a) }) }
