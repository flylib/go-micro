package main

import (
	"embed"
	"github.com/flylib/go-micro/cmd"

	_ "github.com/flylib/go-micro/cmd/micro/api"
	_ "github.com/flylib/go-micro/cmd/micro/cli"
	_ "github.com/flylib/go-micro/cmd/micro/cli/build"
	_ "github.com/flylib/go-micro/cmd/micro/cli/deploy"
	_ "github.com/flylib/go-micro/cmd/micro/resource"
	_ "github.com/flylib/go-micro/cmd/micro/run"
	"github.com/flylib/go-micro/cmd/micro/server"
)

//go:embed web/styles.css web/main.js web/templates/*
var webFS embed.FS

var version = "5.0.0-dev"

func init() {
	server.HTML = webFS
}

func main() {
	cmd.Init(
		cmd.Name("micro"),
		cmd.Version(version),
	)
}
