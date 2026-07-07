package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

// defaultSocket is the local Unix-domain socket ccimgd listens on. The client's
// SSH RemoteForward maps a unique remote socket path on the (shared) target host
// to this local endpoint, e.g.:
//
//	RemoteForward /tmp/ccimg-<hostname>-<user>.sock /tmp/ccimgd-unix.sock
//
// Override with the CCIMGD_SOCK environment variable if needed.
const defaultSocket = "/tmp/ccimgd-unix.sock"

func socketPath() string {
	if p := os.Getenv("CCIMGD_SOCK"); p != "" {
		return p
	}
	return defaultSocket
}

type response struct {
	OK    bool   `json:"ok"`
	Image string `json:"image,omitempty"`
	Error string `json:"error,omitempty"`
}

// pngMagic is the 8-byte PNG file signature. Used to decide whether a file on
// disk is already a PNG (return as-is) or needs conversion for the wire protocol.
var pngMagic = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// imageExts are the file extensions we treat as images without further probing.
var imageExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".tiff": true,
	".tif":  true,
	".heic": true,
}

// getClipboardImage returns PNG bytes for the current clipboard contents.
//
// Fallback chain (behaviour of the first step is unchanged from the original):
//
//	(1) Raw image data on the pasteboard (pngpaste / wl-paste / xclip).
//	(2) macOS "file & clipboard" case (e.g. CleanShot): no raw image bytes, but a
//	    file-URL pointing at an image file on disk. Read/convert that file to PNG.
//	(3) Neither → the same clear "empty" error as before.
func getClipboardImage() ([]byte, error) {
	if out, err := rawClipboardImage(); err == nil && len(out) > 0 {
		return out, nil
	}
	if out, err := clipboardFileURLImage(); err == nil && len(out) > 0 {
		return out, nil
	}
	return nil, fmt.Errorf("Clipboard is empty or does not contain an image")
}

// rawClipboardImage pulls raw PNG bytes directly off the pasteboard. This is the
// original code path and is intentionally left unchanged in behaviour.
func rawClipboardImage() ([]byte, error) {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin":
		cmd = exec.Command("pngpaste", "-")
	case os.Getenv("WAYLAND_DISPLAY") != "":
		cmd = exec.Command("wl-paste", "--type", "image/png")
	default:
		cmd = exec.Command("xclip", "-selection", "clipboard", "-target", "image/png", "-o")
	}
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("no raw image on clipboard")
	}
	return out, nil
}

// clipboardFileURLImage handles the macOS "file & clipboard" case: the pasteboard
// holds a file-URL (public.file-url) rather than raw image bytes. We resolve the
// POSIX path via osascript and, if it points at an image file, return its PNG
// bytes. osascript throws (non-zero exit) when there is no furl on the clipboard,
// which we treat as "no file-URL present".
func clipboardFileURLImage() ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("file-URL clipboard fallback is only supported on macOS")
	}
	// «class furl» = the clipboard's file-reference flavor. Fails cleanly when
	// the clipboard carries no file reference (e.g. plain text).
	out, err := exec.Command("osascript", "-e", "POSIX path of (the clipboard as «class furl»)").Output()
	if err != nil {
		return nil, fmt.Errorf("no file-URL on clipboard: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return nil, fmt.Errorf("empty file-URL path on clipboard")
	}
	return imageBytesFromFile(path)
}

// isImagePath reports whether a path has a recognized image file extension.
func isImagePath(p string) bool {
	return imageExts[strings.ToLower(filepath.Ext(p))]
}

// fileReportsImage uses file(1) as a content-based fallback for files whose
// extension we don't recognize. Returns true only for an image/* MIME type.
func fileReportsImage(path string) bool {
	out, err := exec.Command("file", "-b", "--mime-type", path).Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "image/")
}

// imageBytesFromFile validates that path is a readable, regular image file and
// returns its contents as PNG bytes. Security: only regular files that look like
// images (by extension or file(1) MIME type) are ever read; anything else is
// rejected so the daemon never ships arbitrary non-image files. Non-PNG images
// are converted to PNG so the wire protocol (base64 PNG) stays unchanged.
func imageBytesFromFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("clipboard file not accessible: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("clipboard file is not a regular file: %s", path)
	}
	if !isImagePath(path) && !fileReportsImage(path) {
		return nil, fmt.Errorf("clipboard file is not an image: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read clipboard file: %w", err)
	}
	// Already a PNG (trust content, not extension) → return verbatim.
	if bytes.HasPrefix(data, pngMagic) {
		return data, nil
	}
	// Some other image format → convert to PNG for the wire protocol.
	return convertToPNG(path)
}

// convertToPNG converts an image file to PNG via sips (macOS) and returns the
// resulting bytes. A junk/non-image file with an image extension will make sips
// fail here, which correctly propagates as an error.
func convertToPNG(src string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "ccimgd-*.png")
	if err != nil {
		return nil, fmt.Errorf("could not create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if out, err := exec.Command("sips", "-s", "format", "png", src, "--out", tmpPath).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sips could not convert %s to PNG: %v: %s", src, err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("could not read converted PNG: %w", err)
	}
	if !bytes.HasPrefix(data, pngMagic) {
		return nil, fmt.Errorf("sips output for %s is not a PNG", src)
	}
	return data, nil
}

func handleConn(conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil || n == 0 {
			return
		}
		for _, b := range buf[:n] {
			if b == '\n' {
				goto ready
			}
		}
	}
ready:

	var resp response
	imgData, err := getClipboardImage()
	if err != nil {
		resp = response{OK: false, Error: err.Error()}
	} else {
		resp = response{OK: true, Image: base64.StdEncoding.EncodeToString(imgData)}
	}

	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	conn.Write(data)
}

// version identifies the daemon build. It defaults to the VCS revision embedded
// by `go build`; override at build time with -ldflags "-X main.version=...".
var version = "dev"

// usage is the short help text printed for -h/--help. %s is filled with the
// default socket path; CCIMGD_SOCK overrides it at runtime.
const usage = `ccimgd-unix — clipboard-image daemon (Unix-domain socket)

Usage:
  ccimgd-unix            start the daemon (blocks, serving the socket)
  ccimgd-unix -h|--help  print this help and exit
  ccimgd-unix --version  print version information and exit

Environment:
  CCIMGD_SOCK  socket path to listen on (default %s)
`

// versionString reports the build version: the explicit -ldflags value if set,
// otherwise the VCS revision embedded by the Go toolchain, otherwise "dev".
func versionString() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return version
}

func main() {
	// Handle informational flags before touching the socket: a stray
	// `ccimgd-unix --help` or `--version` must never disturb a running daemon.
	for _, arg := range os.Args[1:] {
		switch arg {
		case "-h", "--help":
			fmt.Printf(usage, defaultSocket)
			os.Exit(0)
		case "--version":
			fmt.Printf("ccimgd-unix %s\n", versionString())
			os.Exit(0)
		}
	}

	path := socketPath()

	// Singleton guard: if a daemon is already listening on this socket, refuse to
	// start rather than clobbering the live endpoint. A successful Dial proves
	// someone is accepting connections there; a failed Dial means the socket is
	// stale or absent, so we fall through to the stale-socket cleanup below.
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		conn.Close()
		fmt.Fprintf(os.Stderr, "ccimgd already running at %s\n", path)
		os.Exit(1)
	}

	// Remove a stale socket file left behind by a previous, non-clean shutdown.
	// Without this, net.Listen("unix", ...) fails with EADDRINUSE ("address
	// already in use") even though no process is actually listening.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: could not remove stale socket %s: %v\n", path, err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to listen on %s: %v\n", path, err)
		os.Exit(1)
	}
	defer listener.Close()

	// Restrict the socket to the owning user; the forwarded connection is fed by
	// the user's own sshd, so no wider access is required.
	if err := os.Chmod(path, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not chmod socket %s: %v\n", path, err)
	}

	fmt.Printf("ccimgd listening on unix:%s\n", path)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		listener.Close()
		os.Remove(path)
		os.Exit(0)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			break
		}
		handleConn(conn)
	}
}
