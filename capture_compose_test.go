package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestComposePNGCropsSelection(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 1000, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1000; x++ {
			full.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 50, 255})
		}
	}

	svc := &CaptureService{
		screenshotImg: full,
		session: &CaptureSession{
			Screen: CaptureScreen{
				Width:       1000,
				Height:      800,
				PixelWidth:  1000,
				PixelHeight: 800,
			},
		},
	}

	req := ExportRequest{
		Selection:      SelectionBounds{X: 100, Y: 50, Width: 200, Height: 150},
		ViewportWidth:  1000,
		ViewportHeight: 800,
	}
	out, err := svc.composePNG(req)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 200 || img.Bounds().Dy() != 150 {
		t.Fatalf("expected 200x150 crop, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestComposePNGUsesViewportForScale(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 1920, 1080))

	svc := &CaptureService{
		screenshotImg: full,
		session: &CaptureSession{
			Screen: CaptureScreen{
				Width:       1280,
				Height:      720,
				PixelWidth:  1920,
				PixelHeight: 1080,
			},
		},
	}

	// Selection covers most of the overlay viewport, not the mismatched screen size.
	req := ExportRequest{
		Selection:      SelectionBounds{X: 100, Y: 80, Width: 800, Height: 600},
		ViewportWidth:  1920,
		ViewportHeight: 1080,
	}
	out, err := svc.composePNG(req)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 800 || img.Bounds().Dy() != 600 {
		t.Fatalf("expected 800x600 crop using viewport scale, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestExportRequestJSONUnmarshal(t *testing.T) {
	raw := []byte(`{"selection":{"x":100,"y":50,"width":200,"height":150},"annotations":[],"viewportWidth":1920,"viewportHeight":1080}`)
	var req ExportRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Selection.Width != 200 {
		t.Fatalf("expected width 200, got %v", req.Selection)
	}
}
