package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// pngBytes is a real, multi-pixel PNG that image tools (sips) can decode. Built
// once via the stdlib encoder so tests don't depend on any fixture file.
var pngBytes = mustPNG()

func mustPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 60), uint8(y * 60), 200, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestIsImagePath(t *testing.T) {
	cases := map[string]bool{
		"/a/b.png":       true,
		"/a/b.PNG":       true,
		"/a/b.jpg":       true,
		"/a/b.jpeg":      true,
		"/a/b.gif":       true,
		"/a/b.tiff":      true,
		"/a/b.tif":       true,
		"/a/b.heic":      true,
		"/a/b.txt":       false,
		"/a/b":           false,
		"/a/b.png.txt":   false,
		"/a/image.webpx": false,
	}
	for in, want := range cases {
		if got := isImagePath(in); got != want {
			t.Errorf("isImagePath(%q) = %v, want %v", in, got, want)
		}
	}
}

// A PNG on disk must be returned byte-for-byte (no conversion).
func TestImageBytesFromFile_PNGReadDirect(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(p, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := imageBytesFromFile(p)
	if err != nil {
		t.Fatalf("imageBytesFromFile: %v", err)
	}
	if !bytes.Equal(got, pngBytes) {
		t.Fatalf("PNG bytes were altered; got %d bytes, want %d", len(got), len(pngBytes))
	}
}

// A PNG whose extension lies (e.g. .jpg) must still be detected via magic bytes
// and returned as valid PNG (HasPrefix pngMagic).
func TestImageBytesFromFile_MislabeledPNG(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shot.jpg") // wrong extension on purpose
	if err := os.WriteFile(p, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := imageBytesFromFile(p)
	if err != nil {
		t.Fatalf("imageBytesFromFile: %v", err)
	}
	if !bytes.HasPrefix(got, pngMagic) {
		t.Fatalf("expected PNG output, got prefix %x", got[:min(8, len(got))])
	}
}

// A non-PNG image (real JPEG produced by sips) must be converted to PNG.
func TestImageBytesFromFile_NonPNGConverted(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sips only available on macOS")
	}
	if _, err := exec.LookPath("sips"); err != nil {
		t.Skip("sips not available")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	if err := os.WriteFile(src, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	jpg := filepath.Join(dir, "real.jpg")
	if out, err := exec.Command("sips", "-s", "format", "jpeg", src, "--out", jpg).CombinedOutput(); err != nil {
		t.Skipf("could not build test jpeg: %v: %s", err, out)
	}
	// Sanity: the jpeg must NOT start with the PNG magic.
	raw, _ := os.ReadFile(jpg)
	if bytes.HasPrefix(raw, pngMagic) {
		t.Fatal("test jpeg unexpectedly starts with PNG magic")
	}
	got, err := imageBytesFromFile(jpg)
	if err != nil {
		t.Fatalf("imageBytesFromFile: %v", err)
	}
	if !bytes.HasPrefix(got, pngMagic) {
		t.Fatalf("expected converted PNG output, got prefix %x", got[:min(8, len(got))])
	}
}

// (min is the Go builtin.)

// A plain text file must be rejected (security: never ship non-image files).
func TestImageBytesFromFile_RejectsText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(p, []byte("top secret password"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := imageBytesFromFile(p); err == nil {
		t.Fatal("expected error for text file, got nil")
	}
}

// A text file with an image extension must still be rejected: the extension
// passes isImagePath, but the content is not an image, so sips conversion fails.
func TestImageBytesFromFile_RejectsTextWithImageExt(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sips only available on macOS")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "fake.png") // image extension, junk content
	if err := os.WriteFile(p, []byte("this is definitely not a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := imageBytesFromFile(p); err == nil {
		t.Fatal("expected error for non-image content, got nil")
	}
}

// Missing files and directories must error, not panic.
func TestImageBytesFromFile_MissingAndDir(t *testing.T) {
	if _, err := imageBytesFromFile("/no/such/file.png"); err == nil {
		t.Fatal("expected error for missing file")
	}
	dir := t.TempDir()
	if _, err := imageBytesFromFile(dir); err == nil {
		t.Fatal("expected error for directory")
	}
}
