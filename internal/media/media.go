// Package media validates uploads and converts them into data URLs the
// upstream OpenAI-compatible server accepts. Video probing and frame
// extraction shell out to ffprobe/ffmpeg.
package media

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Kind returns "image", "video" or "" for unsupported input.
func Kind(filename, contentType string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tiff":
		return "image"
	case ".mp4", ".mov", ".webm", ".mkv", ".avi", ".m4v", ".mpeg", ".mpg":
		return "video"
	}
	if strings.HasPrefix(contentType, "image/") {
		return "image"
	}
	if strings.HasPrefix(contentType, "video/") {
		return "video"
	}
	return ""
}

// ImageDataURL sniffs the mime type and returns a base64 data URL.
func ImageDataURL(buf []byte) string {
	mime := http.DetectContentType(buf)
	if !strings.HasPrefix(mime, "image/") {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(buf)
}

// SaveTemp copies an upload into a temp file and returns its path and size.
func SaveTemp(r io.Reader, name string) (path string, size int64, err error) {
	f, err := os.CreateTemp("", "mod-*"+filepath.Ext(name))
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		os.Remove(f.Name())
		return "", 0, err
	}
	return f.Name(), n, nil
}

// ProbeDuration returns the media duration in seconds via ffprobe.
func ProbeDuration(path string) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe parse: %w", err)
	}
	return d, nil
}

// VideoDataURL base64-encodes a video file as a data URL.
func VideoDataURL(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// FramesDataURLs extracts up to max evenly spaced JPEG frames (max 768px wide)
// and returns them as data URLs. Used as a fallback when the upstream rejects
// native video input.
func FramesDataURLs(path string, max int) ([]string, error) {
	if max <= 0 {
		max = 8
	}
	dur, err := ProbeDuration(path)
	if err != nil || dur <= 0 {
		dur = 8
	}
	fps := float64(max) / dur
	if fps > 1 {
		fps = 1
	}
	dir, err := os.MkdirTemp("", "frames-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	pattern := filepath.Join(dir, "f_%02d.jpg")
	cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-i", path,
		"-vf", fmt.Sprintf("fps=%.4f,scale='min(768,iw)':-2", fps),
		"-frames:v", strconv.Itoa(max), "-q:v", "4", pattern)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w (%.160s)", err, string(out))
	}
	files, _ := filepath.Glob(filepath.Join(dir, "f_*.jpg"))
	sort.Strings(files)
	out := make([]string, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		out = append(out, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(raw))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no frames extracted")
	}
	return out, nil
}
