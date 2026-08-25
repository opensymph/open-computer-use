package main

// Clean-room render-proxy generation (aligned with polished-renderer proxy specs:
// crf 17, veryfast, keyint=1 all-intra for seekable proxies).

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type proxyArtifact struct {
	Path      string `json:"path"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	CreatedAt string `json:"createdAt"`
}

type renderProxiesMetadata struct {
	Version   int            `json:"version"`
	Source    string         `json:"source"`
	Primary   *proxyArtifact `json:"primary1080p,omitempty"`
	Full      *proxyArtifact `json:"full,omitempty"`
	CreatedAt string         `json:"createdAt"`
}

func generateRenderProxies(sourceVideo, outDir string, want1080, wantFull bool) (renderProxiesMetadata, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return renderProxiesMetadata{}, fmt.Errorf("ffmpeg is required for proxy generation")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return renderProxiesMetadata{}, err
	}
	_, srcW, srcH, err := probeVideoDurationMs(sourceVideo)
	if err != nil {
		return renderProxiesMetadata{}, err
	}
	meta := renderProxiesMetadata{
		Version:   1,
		Source:    sourceVideo,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if want1080 {
		path := filepath.Join(outDir, "proxy-1080p.mp4")
		targetH := 1080
		if srcH > 0 && srcH < 1080 {
			targetH = srcH
		}
		if err := runProxyEncode(sourceVideo, path, targetH); err != nil {
			return meta, err
		}
		meta.Primary = &proxyArtifact{
			Path: path, Width: 0, Height: targetH, CreatedAt: meta.CreatedAt,
		}
	}
	if wantFull {
		path := filepath.Join(outDir, "proxy-full.mp4")
		if err := runProxyEncode(sourceVideo, path, 0); err != nil {
			return meta, err
		}
		meta.Full = &proxyArtifact{
			Path: path, Width: srcW, Height: srcH, CreatedAt: meta.CreatedAt,
		}
	}
	metaPath := filepath.Join(outDir, "render-proxies.json")
	data, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(metaPath, data, 0o644)
	return meta, nil
}

func runProxyEncode(input, output string, targetHeight int) error {
	args := []string{"-nostdin", "-y", "-i", input}
	if targetHeight > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=-2:%d:flags=lanczos", targetHeight))
	}
	args = append(args,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "17",
		"-pix_fmt", "yuv420p",
		"-profile:v", "high",
		"-x264-params", "keyint=1:min-keyint=1:scenecut=0:bframes=0",
		// Omit +faststart: some artifact/network filesystems fail the
		// moov rewrite pass ("Error closing file: Input/output error").
		"-an",
		output,
	)
	cmd := exec.Command("ffmpeg", args...)
	logPath := output + ".log"
	lf, _ := os.Create(logPath)
	if lf != nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
		defer lf.Close()
	}
	if err := cmd.Run(); err != nil {
		detail, _ := os.ReadFile(logPath)
		return fmt.Errorf("proxy encode failed: %v: %s", err, trimTail(string(detail), 400))
	}
	return nil
}

func runProxyCommand(args []string, stdout io.Writer) error {
	var input, outDir string
	want1080, wantFull := true, true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--input", "-i":
			i++
			if i >= len(args) {
				return fmt.Errorf("--input requires a value")
			}
			input = args[i]
		case "--output-dir", "-o":
			i++
			if i >= len(args) {
				return fmt.Errorf("--output-dir requires a value")
			}
			outDir = args[i]
		case "--no-1080p":
			want1080 = false
		case "--no-full":
			wantFull = false
		default:
			return fmt.Errorf("unknown proxy option: %s", args[i])
		}
	}
	if input == "" {
		return fmt.Errorf("record proxy requires --input <raw.mp4>")
	}
	if outDir == "" {
		outDir = filepath.Join(filepath.Dir(input), "render-proxies")
	}
	meta, err := generateRenderProxies(input, outDir, want1080, wantFull)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "render proxies ready: dir=%s primary=%v full=%v\n",
		outDir, meta.Primary != nil, meta.Full != nil)
	return nil
}
