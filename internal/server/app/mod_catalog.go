package app

import (
	"opensignage/internal/server/catalog"
	"opensignage/internal/server/core"
)

func init() { Register(func(a *core.App) any { return catalog.New(a) }) }
