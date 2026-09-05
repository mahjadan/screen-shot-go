package main

import (
	"bytes"
	"embed"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	captureService := NewCaptureService()

	app := application.New(application.Options{
		Name:        "screenshot-go",
		Description: "Tray-first personal screenshot utility",
		Services: []application.Service{
			application.NewService(captureService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Linux: application.LinuxOptions{
			DisableQuitOnLastWindowClosed: true,
		},
	})

	captureService.SetApp(app)
	if err := captureService.Init(); err != nil {
		log.Printf("[capture] init warning: %v", err)
	}

	if runtime.GOOS == "linux" {
		app.Event.OnApplicationEvent(events.Linux.ApplicationStartup, func(event *application.ApplicationEvent) {
			captureService.PrewarmOverlay()
		})
	}

	setupTray(app, captureService)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func setupTray(app *application.App, captureService *CaptureService) {
	log.Printf("[tray] setting up tray")

	systray := app.SystemTray.New()
	icon := buildTrayIcon()
	systray.SetIcon(icon)
	systray.SetTooltip("Screenshot")
	systray.SetLabel("Screenshot")

	menu := app.NewMenu()
	menu.Add("Capture Region").OnClick(func(ctx *application.Context) {
		captureService.TriggerCapture(false)
	})
	menu.Add("Capture Fullscreen").OnClick(func(ctx *application.Context) {
		captureService.TriggerCapture(true)
	})
	menu.AddSeparator()
	menu.Add("Set Default Save Folder").OnClick(func(ctx *application.Context) {
		if _, err := captureService.SelectDefaultSaveFolder(); err != nil {
			captureService.ShowError("Preferences", err)
		}
	})

	autostartEnabled, autostartErr := app.Autostart.IsEnabled()
	autostartItem := menu.AddCheckbox("Launch at Login", autostartEnabled)
	if autostartErr != nil && errors.Is(autostartErr, application.ErrAutostartNotSupported) {
		autostartItem.SetEnabled(false)
	} else {
		autostartItem.OnClick(func(ctx *application.Context) {
			enabled := ctx.ClickedMenuItem().Checked()
			if err := captureService.SetLaunchAtLogin(enabled); err != nil {
				ctx.ClickedMenuItem().SetChecked(!enabled)
				menu.Update()
				captureService.ShowError("Launch at Login", err)
				return
			}
			menu.Update()
		})
	}

	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(ctx *application.Context) {
		log.Printf("[tray] quit requested")
		app.Quit()
	})

	systray.SetMenu(menu)
	systray.OnClick(func() {
		systray.OpenMenu()
	})
	systray.OnRightClick(func() {
		systray.OpenMenu()
	})
}

func buildTrayIcon() []byte {
	const size = 32

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), image.Transparent, image.Point{}, draw.Src)

	cameraBody := image.Rect(4, 9, 28, 25)
	draw.Draw(img, cameraBody, &image.Uniform{C: color.RGBA{R: 25, G: 25, B: 28, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(9, 6, 16, 11), &image.Uniform{C: color.RGBA{R: 25, G: 25, B: 28, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(18, 6, 23, 10), &image.Uniform{C: color.RGBA{R: 229, G: 57, B: 53, A: 255}}, image.Point{}, draw.Src)
	drawRing(img, 16, 17, 7, color.RGBA{R: 239, G: 83, B: 80, A: 255}, 2)
	fillCircle(img, 16, 17, 3, color.RGBA{R: 255, G: 235, B: 238, A: 255})

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil
	}
	return out.Bytes()
}
