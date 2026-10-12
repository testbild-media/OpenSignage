package app

import (
	"opensignage/internal/server/content"
	"opensignage/internal/server/core"
)

func init() { Register(func(a *core.App) any { return content.New(a) }) }
