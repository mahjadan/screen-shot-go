package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kbinani/screenshot"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.design/x/clipboard"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

type CaptureService struct {
	mu             sync.RWMutex
	app            *application.App
	overlay        *application.WebviewWindow
	screenshotPNG  []byte
	screenshotImg  image.Image
	session        *CaptureSession
	config         AppConfig
	configPath     string
	clipboardReady bool
}

type AppConfig struct {
	DefaultSaveDir string `json:"defaultSaveDir"`
}

type CaptureSession struct {
	Fullscreen  bool            `json:"fullscreen"`
	ImageBase64 string          `json:"imageBase64"`
	Screen      CaptureScreen   `json:"screen"`
	Selection   SelectionBounds `json:"selection"`
}

type CaptureScreen struct {
	Name        string  `json:"name"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	PixelWidth  int     `json:"pixelWidth"`
	PixelHeight int     `json:"pixelHeight"`
	ScaleFactor float64 `json:"scaleFactor"`
}

type SelectionBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type ExportRequest struct {
	Selection   SelectionBounds `json:"selection"`
	Annotations []Annotation    `json:"annotations"`
}

type Annotation struct {
	Type     string  `json:"type"`
	Color    string  `json:"color"`
	X1       float64 `json:"x1"`
	Y1       float64 `json:"y1"`
	X2       float64 `json:"x2"`
	Y2       float64 `json:"y2"`
	Text     string  `json:"text"`
	FontSize float64 `json:"fontSize"`
}

func NewCaptureService() *CaptureService {
	return &CaptureService{}
}

func (s *CaptureService) SetApp(app *application.App) {
	s.app = app
}

func (s *CaptureService) Init() error {
	if s.app == nil {
		return errors.New("application is not attached")
	}

	if err := s.loadConfig(); err != nil {
		return err
	}

	if err := clipboard.Init(); err != nil {
		s.app.Logger.Warn("[capture] clipboard unavailable", "error", err)
		return nil
	}

	s.clipboardReady = true
	return nil
}

func (s *CaptureService) TriggerCapture(fullscreen bool) {
	go func() {
		if err := s.beginCapture(fullscreen); err != nil {
			s.ShowError("Capture Failed", err)
		}
	}()
}

func (s *CaptureService) PrewarmOverlay() {
	if s.app == nil {
		return
	}

	application.InvokeAsync(func() {
		primary := s.app.Screen.GetPrimary()
		if primary == nil {
			return
		}
		if err := s.ensureOverlayWindow(primary); err != nil {
			s.app.Logger.Warn("[capture] overlay prewarm failed", "error", err)
		}
	})
}

func (s *CaptureService) beginCapture(fullscreen bool) error {
	s.mu.Lock()
	if s.session != nil {
		s.mu.Unlock()
		return errors.New("a capture session is already active")
	}
	s.mu.Unlock()

	primary := s.app.Screen.GetPrimary()
	if primary == nil {
		return errors.New("unable to detect the primary display")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var (
		img      image.Image
		pngBytes []byte
		err      error
	)

	logPrefix := "[capture]"
	s.app.Logger.Info(logPrefix+" starting capture", "fullscreen", fullscreen, "screen", primary.Name)

	if isWaylandSession() && runtime.GOOS == "linux" {
		img, pngBytes, err = s.captureLinuxWaylandWithAnchor(ctx, primary)
	} else {
		img, pngBytes, err = s.captureViaScreenshot(ctx)
	}
	if err != nil {
		return err
	}

	pixelWidth := img.Bounds().Dx()
	pixelHeight := img.Bounds().Dy()
	screenWidth := primary.Size.Width
	screenHeight := primary.Size.Height
	if screenWidth == 0 || screenHeight == 0 {
		screenWidth = pixelWidth
		screenHeight = pixelHeight
	}

	session := &CaptureSession{
		Fullscreen:  fullscreen,
		ImageBase64: base64.StdEncoding.EncodeToString(pngBytes),
		Screen: CaptureScreen{
			Name:        primary.Name,
			Width:       screenWidth,
			Height:      screenHeight,
			PixelWidth:  pixelWidth,
			PixelHeight: pixelHeight,
			ScaleFactor: float64(primary.ScaleFactor),
		},
	}
	if fullscreen {
		session.Selection = SelectionBounds{
			X:      0,
			Y:      0,
			Width:  float64(screenWidth),
			Height: float64(screenHeight),
		}
	}

	s.mu.Lock()
	s.screenshotPNG = pngBytes
	s.screenshotImg = img
	s.session = session
	s.mu.Unlock()

	var showErr error
	application.InvokeSync(func() {
		showErr = s.showOverlay(primary, fullscreen)
	})
	return showErr
}

func (s *CaptureService) captureLinuxWaylandWithAnchor(ctx context.Context, primary *application.Screen) (image.Image, []byte, error) {
	parentWindow, cleanup, err := acquirePortalParentWindow()
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.app.Logger.Warn("[capture] portal parent window unavailable", "error", err)
		parentWindow = ""
	}

	return s.captureLinuxWayland(ctx, primary, parentWindow)
}

func (s *CaptureService) captureLinuxWayland(ctx context.Context, primary *application.Screen, parentWindow string) (image.Image, []byte, error) {
	var errs []string

	if img, pngBytes, err := s.captureViaGrim(ctx, primary.Name); err == nil {
		return img, pngBytes, nil
	} else {
		errs = append(errs, err.Error())
	}

	if img, pngBytes, err := s.captureViaPortal(ctx, parentWindow); err == nil {
		return img, pngBytes, nil
	} else {
		errs = append(errs, err.Error())
	}

	return nil, nil, formatWaylandCaptureError(errs)
}

func (s *CaptureService) captureViaGrim(ctx context.Context, outputName string) (image.Image, []byte, error) {
	if _, err := exec.LookPath("grim"); err != nil {
		return nil, nil, errors.New("grim is not installed")
	}

	args := []string{"-"}
	if strings.TrimSpace(outputName) != "" {
		args = []string{"-o", outputName, "-"}
	}

	cmd := exec.CommandContext(ctx, "grim", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, nil, fmt.Errorf("grim capture failed: %s", msg)
	}

	pngBytes := stdout.Bytes()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("grim returned invalid PNG: %w", err)
	}
	return img, pngBytes, nil
}

func (s *CaptureService) captureViaPortal(ctx context.Context, parentWindow string) (image.Image, []byte, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, nil, fmt.Errorf("portal screenshot requires a D-Bus session bus: %w", err)
	}
	defer conn.Close()

	token := fmt.Sprintf("screenshot_go_%d", time.Now().UnixNano())
	options := map[string]dbus.Variant{
		"interactive":  dbus.MakeVariant(false),
		"modal":        dbus.MakeVariant(false),
		"handle_token": dbus.MakeVariant(token),
	}

	signals := make(chan *dbus.Signal, 1)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)

	const matchRule = "type='signal',sender='org.freedesktop.portal.Desktop',interface='org.freedesktop.portal.Request',member='Response'"
	if err := conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, matchRule).Err; err != nil {
		return nil, nil, fmt.Errorf("portal screenshot failed to subscribe to D-Bus signals: %w", err)
	}
	defer conn.BusObject().Call("org.freedesktop.DBus.RemoveMatch", 0, matchRule)

	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	call := obj.Call("org.freedesktop.portal.Screenshot.Screenshot", 0, parentWindow, options)
	if call.Err != nil {
		return nil, nil, fmt.Errorf("portal screenshot call failed: %w", call.Err)
	}

	var requestPath dbus.ObjectPath
	if err := call.Store(&requestPath); err != nil {
		return nil, nil, fmt.Errorf("portal screenshot returned invalid response: %w", err)
	}

	request := conn.Object("org.freedesktop.portal.Desktop", requestPath)

	for {
		select {
		case <-ctx.Done():
			_ = request.Call("org.freedesktop.portal.Request.Close", 0).Err
			return nil, nil, fmt.Errorf("portal screenshot timed out: %w", ctx.Err())
		case signal := <-signals:
			if signal == nil || signal.Path != requestPath || signal.Name != "org.freedesktop.portal.Request.Response" {
				continue
			}

			var response uint32
			var results map[string]dbus.Variant
			if err := dbus.Store(signal.Body, &response, &results); err != nil {
				return nil, nil, fmt.Errorf("portal screenshot returned malformed response: %w", err)
			}
			if response != 0 {
				return nil, nil, fmt.Errorf("portal screenshot denied or cancelled (code %d)", response)
			}

			uriVariant, ok := results["uri"]
			if !ok {
				return nil, nil, errors.New("portal screenshot succeeded but returned no image URI")
			}

			uriStr, ok := uriVariant.Value().(string)
			if !ok || uriStr == "" {
				return nil, nil, errors.New("portal screenshot returned invalid image URI")
			}

			filePath, err := portalScreenshotPath(uriStr)
			if err != nil {
				return nil, nil, fmt.Errorf("portal screenshot returned unreadable URI: %w", err)
			}

			pngBytes, err := os.ReadFile(filePath)
			if err != nil {
				return nil, nil, fmt.Errorf("portal screenshot failed to read captured image: %w", err)
			}

			img, err := png.Decode(bytes.NewReader(pngBytes))
			if err != nil {
				return nil, nil, fmt.Errorf("portal screenshot returned invalid PNG: %w", err)
			}

			return img, pngBytes, nil
		}
	}
}

func (s *CaptureService) captureViaScreenshot(ctx context.Context) (image.Image, []byte, error) {
	type result struct {
		img *image.RGBA
		err error
	}

	ch := make(chan result, 1)
	go func() {
		img, err := screenshot.CaptureDisplay(0)
		ch <- result{img: img, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, nil, errors.New("screen capture timed out")
	case res := <-ch:
		if res.err != nil {
			return nil, nil, fmt.Errorf("screen capture failed: %w", res.err)
		}
		var out bytes.Buffer
		if err := png.Encode(&out, res.img); err != nil {
			return nil, nil, fmt.Errorf("failed to encode screenshot: %w", err)
		}
		return res.img, out.Bytes(), nil
	}
}

func (s *CaptureService) ensureOverlayWindow(primary *application.Screen) error {
	if primary == nil {
		return errors.New("primary display is unavailable")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.overlay != nil {
		return nil
	}

	s.overlay = s.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       "capture-overlay",
		Title:                      "Screenshot",
		URL:                        "/",
		Screen:                     primary,
		Width:                      primary.Size.Width,
		Height:                     primary.Size.Height,
		Hidden:                     true,
		Frameless:                  true,
		AlwaysOnTop:                true,
		DisableResize:              true,
		DefaultContextMenuDisabled: true,
		StartState:                 application.WindowStateFullscreen,
		BackgroundColour:           application.NewRGB(0, 0, 0),
		KeyBindings: map[string]func(window application.Window){
			"Escape": func(window application.Window) {
				_ = s.CancelCapture()
			},
		},
	})
	s.overlay.OnWindowEvent(events.Common.WindowClosing, func(event *application.WindowEvent) {
		s.mu.Lock()
		s.clearCaptureSessionLocked()
		s.mu.Unlock()
	})

	return nil
}

func (s *CaptureService) showOverlay(primary *application.Screen, fullscreen bool) error {
	if err := s.ensureOverlayWindow(primary); err != nil {
		return err
	}

	s.mu.Lock()
	overlay := s.overlay
	s.mu.Unlock()
	if overlay == nil {
		return errors.New("capture overlay is unavailable")
	}

	overlay.SetSize(primary.Size.Width, primary.Size.Height)
	overlay.ForceReload()
	overlay.Show()
	overlay.Focus()
	s.app.Logger.Info("[capture] overlay opened", "fullscreen", fullscreen)
	return nil
}

func (s *CaptureService) GetCaptureState() (*CaptureSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.session == nil {
		return nil, errors.New("no active capture session")
	}

	copySession := *s.session
	return &copySession, nil
}

func (s *CaptureService) CopyToClipboard(req ExportRequest) error {
	pngBytes, err := s.composePNG(req)
	if err != nil {
		return err
	}

	if !s.clipboardReady {
		return errors.New("clipboard support is not available on this machine")
	}

	clipboard.Write(clipboard.FmtImage, pngBytes)
	s.app.Logger.Info("[capture] copied image to clipboard")
	return s.closeAndReset()
}

func (s *CaptureService) SaveWithDialog(req ExportRequest) (string, error) {
	pngBytes, err := s.composePNG(req)
	if err != nil {
		return "", err
	}

	app := application.Get()
	path, err := app.Dialog.SaveFile().
		SetFilename(timestampedFilename()).
		SetDirectory(s.defaultSaveDir()).
		AddFilter("PNG Image", "*.png").
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	if err := os.WriteFile(path, pngBytes, 0o644); err != nil {
		return "", fmt.Errorf("failed to save screenshot: %w", err)
	}
	s.app.Logger.Info("[capture] screenshot saved", "path", path)
	if err := s.closeAndReset(); err != nil {
		return "", err
	}
	return path, nil
}

func (s *CaptureService) SaveToFile(path string, req ExportRequest) error {
	pngBytes, err := s.composePNG(req)
	if err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("save path is required")
	}
	if err := os.WriteFile(path, pngBytes, 0o644); err != nil {
		return fmt.Errorf("failed to save screenshot: %w", err)
	}
	s.app.Logger.Info("[capture] screenshot saved", "path", path)
	return s.closeAndReset()
}

func (s *CaptureService) CancelCapture() error {
	s.app.Logger.Info("[capture] capture cancelled")
	return s.closeAndReset()
}

func (s *CaptureService) CloseCaptureWindow() error {
	return s.closeAndReset()
}

func (s *CaptureService) SetLaunchAtLogin(enabled bool) error {
	if enabled {
		if err := s.app.Autostart.Enable(); err != nil {
			if errors.Is(err, application.ErrAutostartNotSupported) {
				return errors.New("launch at login is not supported on this platform")
			}
			return fmt.Errorf("failed to enable launch at login: %w", err)
		}
		return nil
	}

	if err := s.app.Autostart.Disable(); err != nil {
		if errors.Is(err, application.ErrAutostartNotSupported) {
			return errors.New("launch at login is not supported on this platform")
		}
		return fmt.Errorf("failed to disable launch at login: %w", err)
	}
	return nil
}

func (s *CaptureService) SelectDefaultSaveFolder() (string, error) {
	app := application.Get()
	path, err := app.Dialog.OpenFile().
		SetTitle("Select Default Save Folder").
		SetDirectory(s.defaultSaveDir()).
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	s.mu.Lock()
	s.config.DefaultSaveDir = path
	s.mu.Unlock()

	if err := s.saveConfig(); err != nil {
		return "", err
	}
	return path, nil
}

func (s *CaptureService) GetPreferences() AppConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *CaptureService) ShowError(title string, err error) {
	if err == nil || s.app == nil {
		return
	}
	s.app.Logger.Error("[capture] "+title, "error", err)
	s.app.Dialog.Error().
		SetTitle(title).
		SetMessage(err.Error()).
		Show()
}

func (s *CaptureService) composePNG(req ExportRequest) ([]byte, error) {
	s.mu.RLock()
	if s.session == nil || s.screenshotImg == nil {
		s.mu.RUnlock()
		return nil, errors.New("no active capture session")
	}

	session := *s.session
	src := s.screenshotImg
	s.mu.RUnlock()

	if req.Selection.Width < 5 || req.Selection.Height < 5 {
		return nil, errors.New("selection is too small")
	}

	scaleX := float64(session.Screen.PixelWidth) / float64(max(session.Screen.Width, 1))
	scaleY := float64(session.Screen.PixelHeight) / float64(max(session.Screen.Height, 1))

	srcRect := clampRect(image.Rect(
		int(math.Round(req.Selection.X*scaleX)),
		int(math.Round(req.Selection.Y*scaleY)),
		int(math.Round((req.Selection.X+req.Selection.Width)*scaleX)),
		int(math.Round((req.Selection.Y+req.Selection.Height)*scaleY)),
	), src.Bounds())
	if srcRect.Dx() <= 0 || srcRect.Dy() <= 0 {
		return nil, errors.New("selection falls outside the captured image")
	}

	dst := image.NewRGBA(image.Rect(0, 0, srcRect.Dx(), srcRect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, srcRect.Min, draw.Src)

	for _, annotation := range req.Annotations {
		if err := drawAnnotation(dst, annotation, scaleX, scaleY); err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, fmt.Errorf("failed to encode composed image: %w", err)
	}
	return out.Bytes(), nil
}

func drawAnnotation(dst *image.RGBA, annotation Annotation, scaleX, scaleY float64) error {
	col := parseHexColor(annotation.Color)
	thickness := max(int(math.Round(3*math.Max(scaleX, scaleY))), 2)

	switch annotation.Type {
	case "rectangle":
		rect := normalizedRect(annotation.X1, annotation.Y1, annotation.X2, annotation.Y2, scaleX, scaleY)
		drawStrokeRect(dst, rect, col, thickness)
	case "ellipse":
		rect := normalizedRect(annotation.X1, annotation.Y1, annotation.X2, annotation.Y2, scaleX, scaleY)
		drawEllipse(dst, rect, col, thickness)
	case "arrow":
		x1 := int(math.Round(annotation.X1 * scaleX))
		y1 := int(math.Round(annotation.Y1 * scaleY))
		x2 := int(math.Round(annotation.X2 * scaleX))
		y2 := int(math.Round(annotation.Y2 * scaleY))
		drawArrow(dst, x1, y1, x2, y2, col, thickness)
	case "text":
		x := int(math.Round(annotation.X1 * scaleX))
		y := int(math.Round(annotation.Y1 * scaleY))
		size := annotation.FontSize * math.Max(scaleX, scaleY)
		if size <= 0 {
			size = 18
		}
		drawText(dst, x, y, annotation.Text, col, size)
	default:
		return fmt.Errorf("unsupported annotation type: %s", annotation.Type)
	}

	return nil
}

func normalizedRect(x1, y1, x2, y2, scaleX, scaleY float64) image.Rectangle {
	left := int(math.Round(math.Min(x1, x2) * scaleX))
	top := int(math.Round(math.Min(y1, y2) * scaleY))
	right := int(math.Round(math.Max(x1, x2) * scaleX))
	bottom := int(math.Round(math.Max(y1, y2) * scaleY))
	return image.Rect(left, top, right, bottom)
}

func drawStrokeRect(img *image.RGBA, rect image.Rectangle, col color.RGBA, thickness int) {
	rect = clampRect(rect, img.Bounds())
	if rect.Empty() {
		return
	}

	fillRect(img, image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, min(rect.Min.Y+thickness, rect.Max.Y)), col)
	fillRect(img, image.Rect(rect.Min.X, max(rect.Max.Y-thickness, rect.Min.Y), rect.Max.X, rect.Max.Y), col)
	fillRect(img, image.Rect(rect.Min.X, rect.Min.Y, min(rect.Min.X+thickness, rect.Max.X), rect.Max.Y), col)
	fillRect(img, image.Rect(max(rect.Max.X-thickness, rect.Min.X), rect.Min.Y, rect.Max.X, rect.Max.Y), col)
}

func drawEllipse(img *image.RGBA, rect image.Rectangle, col color.RGBA, thickness int) {
	rect = clampRect(rect, img.Bounds())
	if rect.Empty() {
		return
	}

	cx := float64(rect.Min.X+rect.Max.X) / 2
	cy := float64(rect.Min.Y+rect.Max.Y) / 2
	rx := float64(rect.Dx()) / 2
	ry := float64(rect.Dy()) / 2
	if rx < 1 || ry < 1 {
		return
	}

	steps := max(int(2*math.Pi*math.Max(rx, ry)), 48)
	for i := 0; i < steps; i++ {
		theta := 2 * math.Pi * float64(i) / float64(steps)
		x := int(math.Round(cx + math.Cos(theta)*rx))
		y := int(math.Round(cy + math.Sin(theta)*ry))
		fillCircle(img, x, y, max(thickness/2, 1), col)
	}
}

func drawArrow(img *image.RGBA, x1, y1, x2, y2 int, col color.RGBA, thickness int) {
	drawLine(img, x1, y1, x2, y2, col, thickness)

	angle := math.Atan2(float64(y2-y1), float64(x2-x1))
	headLength := float64(10 + thickness*2)
	leftAngle := angle + (math.Pi * 5 / 6)
	rightAngle := angle - (math.Pi * 5 / 6)

	lx := x2 + int(math.Round(math.Cos(leftAngle)*headLength))
	ly := y2 + int(math.Round(math.Sin(leftAngle)*headLength))
	rx := x2 + int(math.Round(math.Cos(rightAngle)*headLength))
	ry := y2 + int(math.Round(math.Sin(rightAngle)*headLength))

	drawLine(img, x2, y2, lx, ly, col, thickness)
	drawLine(img, x2, y2, rx, ry, col, thickness)
}

func drawLine(img *image.RGBA, x1, y1, x2, y2 int, col color.RGBA, thickness int) {
	dx := float64(x2 - x1)
	dy := float64(y2 - y1)
	steps := max(int(math.Max(math.Abs(dx), math.Abs(dy))), 1)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := int(math.Round(float64(x1) + dx*t))
		y := int(math.Round(float64(y1) + dy*t))
		fillCircle(img, x, y, max(thickness/2, 1), col)
	}
}

func drawText(img *image.RGBA, x, y int, text string, col color.RGBA, size float64) {
	if strings.TrimSpace(text) == "" {
		return
	}

	face, err := newFontFace(size)
	if err != nil {
		face = basicfont.Face7x13
	}
	defer closeFace(face)

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.P(x, y+int(size)),
	}
	d.DrawString(text)
}

func newFontFace(size float64) (font.Face, error) {
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

func closeFace(face font.Face) {
	if closer, ok := face.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func fillRect(img *image.RGBA, rect image.Rectangle, col color.RGBA) {
	rect = clampRect(rect, img.Bounds())
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetRGBA(x, y, col)
		}
	}
}

func fillCircle(img *image.RGBA, cx, cy, radius int, col color.RGBA) {
	if radius <= 0 {
		radius = 1
	}
	for y := cy - radius; y <= cy+radius; y++ {
		for x := cx - radius; x <= cx+radius; x++ {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) > radius*radius {
				continue
			}
			if image.Pt(x, y).In(img.Bounds()) {
				img.SetRGBA(x, y, col)
			}
		}
	}
}

func drawRing(img *image.RGBA, cx, cy, radius int, col color.RGBA, thickness int) {
	for r := max(radius-thickness, 1); r <= radius; r++ {
		fillCircle(img, cx, cy, r, col)
	}
	fillCircle(img, cx, cy, max(radius-thickness-1, 1), color.RGBA{})
}

func parseHexColor(value string) color.RGBA {
	value = strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(value) != 6 {
		return color.RGBA{R: 255, A: 255}
	}

	var rgb uint64
	if _, err := fmt.Sscanf(value, "%06x", &rgb); err != nil {
		return color.RGBA{R: 255, A: 255}
	}

	return color.RGBA{
		R: uint8(rgb >> 16),
		G: uint8((rgb >> 8) & 0xff),
		B: uint8(rgb & 0xff),
		A: 255,
	}
}

func clampRect(rect, bounds image.Rectangle) image.Rectangle {
	if rect.Min.X < bounds.Min.X {
		rect.Min.X = bounds.Min.X
	}
	if rect.Min.Y < bounds.Min.Y {
		rect.Min.Y = bounds.Min.Y
	}
	if rect.Max.X > bounds.Max.X {
		rect.Max.X = bounds.Max.X
	}
	if rect.Max.Y > bounds.Max.Y {
		rect.Max.Y = bounds.Max.Y
	}
	if rect.Min.X > rect.Max.X {
		rect.Min.X = rect.Max.X
	}
	if rect.Min.Y > rect.Max.Y {
		rect.Min.Y = rect.Max.Y
	}
	return rect
}

func (s *CaptureService) closeAndReset() error {
	s.mu.Lock()
	overlay := s.overlay
	s.clearCaptureSessionLocked()
	s.mu.Unlock()

	if overlay != nil {
		overlay.Hide()
	}
	return nil
}

func (s *CaptureService) clearCaptureSessionLocked() {
	s.screenshotPNG = nil
	s.screenshotImg = nil
	s.session = nil
}

func (s *CaptureService) loadConfig() error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("unable to resolve config directory: %w", err)
	}

	appDir := filepath.Join(configDir, "screenshot-go")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return fmt.Errorf("unable to create config directory: %w", err)
	}

	s.configPath = filepath.Join(appDir, "config.json")
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.config = AppConfig{}
			return nil
		}
		return fmt.Errorf("unable to read config: %w", err)
	}

	if err := json.Unmarshal(data, &s.config); err != nil {
		return fmt.Errorf("unable to parse config: %w", err)
	}
	return nil
}

func (s *CaptureService) saveConfig() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.config, "", "  ")
	path := s.configPath
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("unable to encode config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("unable to write config: %w", err)
	}
	return nil
}

func (s *CaptureService) defaultSaveDir() string {
	s.mu.RLock()
	configured := s.config.DefaultSaveDir
	s.mu.RUnlock()
	if configured != "" {
		return configured
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}

	pictures := filepath.Join(home, "Pictures")
	if _, err := os.Stat(pictures); err == nil {
		return pictures
	}
	return home
}

func timestampedFilename() string {
	return fmt.Sprintf("screenshot-%s.png", time.Now().Format("2006-01-02-150405"))
}

func isWaylandSession() bool {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return true
	}
	return strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland")
}

func isGnomeSession() bool {
	for _, token := range strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":") {
		if strings.EqualFold(strings.TrimSpace(token), "gnome") {
			return true
		}
	}
	return false
}

func formatWaylandCaptureError(errs []string) error {
	message := "Wayland capture failed."
	if isGnomeSession() {
		message += " GNOME Wayland does not support grim because it does not expose the wlroots screencopy protocol."
		message += " The app falls back to the XDG Desktop Portal screenshot API."
		message += " Approve the screenshot permission prompt if GNOME shows one, or grant screenshot access for this app in Settings."
	} else {
		message += " This compositor may not support grim's wlroots screencopy protocol."
		message += " The app also tried the XDG Desktop Portal screenshot API."
		message += " Install `grim` on wlroots compositors, or approve the portal screenshot prompt on other desktops."
	}
	if len(errs) > 0 {
		message += " Details: " + strings.Join(errs, "; ")
	}
	return errors.New(message)
}

func portalScreenshotPath(rawURI string) (string, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("unsupported URI scheme %q", parsed.Scheme)
	}

	path := parsed.Path
	if unescaped, err := url.PathUnescape(path); err == nil && unescaped != "" {
		path = unescaped
	}

	return filepath.FromSlash(path), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
