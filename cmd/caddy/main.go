package main

import (
	caddycmd "github.com/caddyserver/caddy/v2/cmd"

	// plug in Caddy modules here
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/tobilg/caddy-duckdb-module"
)

func main() {
	caddycmd.Main()
}
