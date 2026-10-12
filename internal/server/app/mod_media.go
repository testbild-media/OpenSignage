package app

import (
	"opensignage/internal/server/core"
	"opensignage/internal/server/media"
)

func init() { Register(func(a *core.App) any { return media.New(a) }) }
