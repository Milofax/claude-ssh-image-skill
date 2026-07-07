package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// socketGlob matches the per-client Unix-domain sockets that each SSH session
// exposes on the shared host via `RemoteForward /tmp/ccimg-<uniq>.sock ...`.
// Multiple pasters (different clients/sessions) coexist as distinct paths.
const socketGlob = "/tmp/ccimg-*.sock"

type response struct {
	OK    bool   `json:"ok"`
	Image string `json:"image"`
	Error string `json:"error"`
}

// discoverSocket resolves which ccimgd socket to talk to.
//
//	(1) If CCIMG_SOCK is set, use exactly that path.
//	(2) Otherwise enumerate socketGlob and pick the most recently modified one
//	    (the paster that was active last). If several candidates exist, a short
//	    warning is logged so it is clear which one was chosen.
//	(3) If none exist, return a clear, actionable error.
func discoverSocket() (string, error) {
	if p := os.Getenv("CCIMG_SOCK"); p != "" {
		return p, nil
	}

	matches, err := filepath.Glob(socketGlob)
	if err != nil {
		return "", fmt.Errorf("failed to enumerate sockets %s: %w", socketGlob, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf(
			"no ccimgd socket found (%s) — is the daemon running on the client "+
				"and does your SSH session carry the RemoteForward?", socketGlob)
	}

	best := ""
	var bestMod time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue // vanished between glob and stat, or unreadable
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = m, info.ModTime()
		}
	}
	if best == "" {
		return "", fmt.Errorf(
			"no usable ccimgd socket found among %d candidate(s) matching %s",
			len(matches), socketGlob)
	}

	if len(matches) > 1 {
		fmt.Fprintf(os.Stderr,
			"ccimg: %d ccimgd sockets found; using most recently modified: %s\n",
			len(matches), best)
	}

	return best, nil
}

func main() {
	sockPath, err := discoverSocket()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to ccimgd at %s: %v\n", sockPath, err)
		os.Exit(1)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("{}\n"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to send request: %v\n", err)
		os.Exit(1)
	}

	var buf []byte
	tmp := make([]byte, 65536)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
		for _, b := range tmp[:n] {
			if b == '\n' {
				goto done
			}
		}
	}
done:

	var resp response
	if err := json.Unmarshal(buf, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse response: %v\n", err)
		os.Exit(1)
	}

	if !resp.OK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	imgData, err := base64.StdEncoding.DecodeString(resp.Image)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to decode image: %v\n", err)
		os.Exit(1)
	}

	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("clipboard-%d.png", os.Getpid()))
	if err := os.WriteFile(tmpFile, imgData, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write image: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(tmpFile)
}
