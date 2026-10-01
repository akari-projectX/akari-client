//go:build tools

// Package mihomo pins the mihomo kernel that ships next to akari-client as a
// separate executable (GPL-3.0; see README.md in this directory). Nothing
// here is linked into akari-client. The imports mirror mihomo's main.go so
// `go mod tidy` keeps every module its main package needs.
package mihomo

import (
	_ "github.com/metacubex/mihomo/common/cmd"
	_ "github.com/metacubex/mihomo/component/age"
	_ "github.com/metacubex/mihomo/component/generator"
	_ "github.com/metacubex/mihomo/component/geodata"
	_ "github.com/metacubex/mihomo/component/updater"
	_ "github.com/metacubex/mihomo/config"
	_ "github.com/metacubex/mihomo/constant"
	_ "github.com/metacubex/mihomo/constant/features"
	_ "github.com/metacubex/mihomo/hub"
	_ "github.com/metacubex/mihomo/hub/executor"
	_ "github.com/metacubex/mihomo/log"
	_ "github.com/metacubex/mihomo/rules/provider"
	_ "go.uber.org/automaxprocs/maxprocs"
)
