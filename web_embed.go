package main

import (
	"embed"
	"io/fs"
)

//go:embed web
var embeddedWeb embed.FS

func webAssetFS() fs.FS {
	assets, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		panic("embedded web assets unavailable: " + err.Error())
	}
	return assets
}
