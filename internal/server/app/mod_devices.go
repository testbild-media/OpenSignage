package app

import (
	"opensignage/internal/server/core"
	"opensignage/internal/server/devices"
)

func init() { Register(func(a *core.App) any { return devices.New(a) }) }
