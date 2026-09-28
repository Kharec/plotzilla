package main

import (
	"embed"
	"log"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// The UI is in English: make GTK's own strings (file chooser buttons and
	// places) match it instead of following the system language.
	os.Setenv("LANGUAGE", "en")

	app := NewApp()
	for _, arg := range os.Args[1:] {
		if !strings.HasPrefix(arg, "-") {
			if _, err := os.Stat(arg); err == nil {
				app.initialPath = arg
			}
			break
		}
	}

	err := wails.Run(&options.App{
		Title:       "Plotzilla",
		Width:       1320,
		Height:      920,
		MinWidth:    900,
		MinHeight:   600,
		AssetServer: &assetserver.Options{Assets: assets},
		// The header bar is drawn by the page, like a GNOME header bar; the
		// transparent background lets the page round the window corners.
		Frameless:        true,
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		Bind:             []interface{}{app},
		Linux: &linux.Options{
			ProgramName:         "plotzilla",
			WindowIsTranslucent: true,
			WebviewGpuPolicy:    linux.WebviewGpuPolicyOnDemand,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
