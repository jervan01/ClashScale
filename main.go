package main

import (
	"embed"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	app := NewApp()
	installShutdownHook(app.cleanup)

	err := wails.Run(&options.App{
		Title:     "ClashScale",
		Width:     1080,
		Height:    740,
		MinWidth:  900,
		MinHeight: 620,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 18, G: 22, B: 31, A: 1},
		StartHidden:      app.autostartLaunch, // 开机自启时不弹窗，只在托盘
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app, app.clash, app.ts},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "clashscale-5d1e0b7a-dog",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				app.showWindow() // 再次双击图标 → 唤出已有窗口
			},
		},
		Windows: &windows.Options{
			Theme: windows.SystemDefault,
			// WebView2 的缓存也放进 data 目录，不往 %AppData% 写东西
			WebviewUserDataPath: filepath.Join(app.dataDir, "webview"),
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
