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

// Tray icon: a simple camera glyph rendered as a monochrome silhouette with
// a punched-out lens hole (a template/silhouette icon can only show detail
// through transparency, since color information is discarded or absent).
//
// macOS renders it via SetTemplateIcon, which lets AppKit auto-invert the
// silhouette for light/dark menu bars. Other platforms (Linux trays, mainly)
// have no equivalent auto-inversion in Wails, so they get a second variant
// with a light halo baked around the dark silhouette so it stays legible on
// both light and dark panel themes.
const (
	trayIconSize        = 64
	trayIconSupersample = 4
	trayIconHaloWidth   = 2.6
)

func inRoundedRect(x, y, x0, y0, x1, y1, radius float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := max64(x0+radius, min64(x, x1-radius))
	cy := max64(y0+radius, min64(y, y1-radius))
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

func inCircle(x, y, cx, cy, r float64) bool {
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// cameraSilhouette reports whether (x, y), in trayIconSize coordinate space,
// is part of the camera glyph: a rounded body with a small viewfinder bump
// on top and a round lens hole punched through it.
func cameraSilhouette(x, y float64) bool {
	body := inRoundedRect(x, y, 6, 20, 58, 50, 5)
	bump := inRoundedRect(x, y, 22, 13, 36, 21, 2)
	lensHole := inCircle(x, y, 32, 35, 9)
	return (body || bump) && !lensHole
}

// renderTrayAlphaMask supersamples cameraSilhouette into an alpha mask,
// optionally dilated by `dilate` pixels (in trayIconSize space) to produce a
// halo around the glyph. Rendering at high resolution and averaging down
// gives free anti-aliasing without needing per-pixel edge math.
func renderTrayAlphaMask(dilate float64) *image.Alpha {
	const hiSize = trayIconSize * trayIconSupersample

	base := make([][]bool, hiSize)
	for hy := range base {
		row := make([]bool, hiSize)
		fy := float64(hy) / trayIconSupersample
		for hx := range row {
			fx := float64(hx) / trayIconSupersample
			row[hx] = cameraSilhouette(fx, fy)
		}
		base[hy] = row
	}

	hi := image.NewAlpha(image.Rect(0, 0, hiSize, hiSize))
	radius := int(dilate * trayIconSupersample)
	for hy := 0; hy < hiSize; hy++ {
		for hx := 0; hx < hiSize; hx++ {
			set := base[hy][hx]
			if !set && radius > 0 {
				for dy := -radius; dy <= radius && !set; dy++ {
					ny := hy + dy
					if ny < 0 || ny >= hiSize {
						continue
					}
					for dx := -radius; dx <= radius; dx++ {
						if dx*dx+dy*dy > radius*radius {
							continue
						}
						nx := hx + dx
						if nx >= 0 && nx < hiSize && base[ny][nx] {
							set = true
							break
						}
					}
				}
			}
			if set {
				hi.SetAlpha(hx, hy, color.Alpha{A: 255})
			}
		}
	}

	out := image.NewAlpha(image.Rect(0, 0, trayIconSize, trayIconSize))
	for y := 0; y < trayIconSize; y++ {
		for x := 0; x < trayIconSize; x++ {
			var sum int
			for dy := 0; dy < trayIconSupersample; dy++ {
				for dx := 0; dx < trayIconSupersample; dx++ {
					sum += int(hi.AlphaAt(x*trayIconSupersample+dx, y*trayIconSupersample+dy).A)
				}
			}
			out.SetAlpha(x, y, color.Alpha{A: uint8(sum / (trayIconSupersample * trayIconSupersample))})
		}
	}
	return out
}

func encodeTrayIconPNG(img image.Image) []byte {
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil
	}
	return out.Bytes()
}

// buildTemplateTrayIcon renders a pure black silhouette for macOS's
// SetTemplateIcon, which AppKit auto-inverts to match the menu bar theme.
func buildTemplateTrayIcon() []byte {
	mask := renderTrayAlphaMask(0)
	img := image.NewRGBA(mask.Bounds())
	draw.DrawMask(img, img.Bounds(), &image.Uniform{C: color.RGBA{A: 255}}, image.Point{}, mask, mask.Bounds().Min, draw.Over)
	return encodeTrayIconPNG(img)
}

// buildOutlinedTrayIcon renders a dark silhouette with a light halo baked
// in, for platforms with no automatic light/dark tray-icon inversion, so it
// stays legible on both light and dark panel themes.
func buildOutlinedTrayIcon() []byte {
	halo := renderTrayAlphaMask(trayIconHaloWidth)
	fill := renderTrayAlphaMask(0)

	img := image.NewRGBA(halo.Bounds())
	draw.DrawMask(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 255, G: 255, B: 255, A: 255}}, image.Point{}, halo, halo.Bounds().Min, draw.Over)
	draw.DrawMask(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 30, G: 30, B: 33, A: 255}}, image.Point{}, fill, fill.Bounds().Min, draw.Over)
	return encodeTrayIconPNG(img)
}

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
	if runtime.GOOS == "darwin" {
		systray.SetTemplateIcon(buildTemplateTrayIcon())
	} else {
		systray.SetIcon(buildOutlinedTrayIcon())
	}
	systray.SetTooltip("Screenshot")
	systray.SetLabel("Screenshot")

	menu := app.NewMenu()
	menu.Add("Capture Region").OnClick(func(ctx *application.Context) {
		captureService.TriggerCapture()
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

