package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/ApolloF/syncer/internal/logx"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	for _, a := range os.Args[1:] {
		if a == "--background" || a == "--backup" {
			runBackground()
			return
		}
	}

	app := NewApp()
	err := wails.Run(&options.App{
		Title:     "Syncer",
		Width:     1040,
		Height:    700,
		MinWidth:  760,
		MinHeight: 520,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		OnStartup:        app.startup,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "b1c9c8a4-syncer-apollof",
		},
		Bind: []interface{}{app},
		Windows: &windows.Options{
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			BackdropType:         windows.Mica,
			Theme:                windows.SystemDefault,
		},
	})
	if err != nil {
		logx.Printf("fatal: %v", err)
	}
}
