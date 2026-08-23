// releasetool replaces the repo's last python3 one-liners in the build and
// release shell scripts, keeping the toolchain at Go + Swift + bash.
//
// Subcommands:
//
//	version <plugin.json>                        print the top-level "version" field
//	manifest <out.json> <repo-root> <tarball-dir>  write release-manifest.json
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: releasetool <version|manifest> [args]")
	}
	var err error
	switch os.Args[1] {
	case "version":
		err = printVersion(os.Args[2:])
	case "manifest":
		err = writeManifest(os.Args[2:])
	default:
		fatalf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "releasetool: "+format+"\n", args...)
	os.Exit(1)
}

func printVersion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: releasetool version <plugin.json>")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	fmt.Println(manifest.Version)
	return nil
}

// Manifest mirrors the JSON schema the retired python3 heredoc produced,
// field order included (Python dicts preserve insertion order).
type manifestArtifact struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

type manifestDistribution struct {
	Type         string `json:"type"`
	PackageCount int    `json:"package_count"`
	StagingDir   string `json:"staging_dir"`
}

type releaseManifest struct {
	Repository     string               `json:"repository"`
	GitSHA         string               `json:"git_sha"`
	GeneratedAtUTC string               `json:"generated_at_utc"`
	Artifacts      []manifestArtifact   `json:"artifacts"`
	Distribution   manifestDistribution `json:"distribution"`
}

func writeManifest(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: releasetool manifest <out.json> <repo-root> <tarball-dir>")
	}
	manifestPath, repoRoot, tarballDir := args[0], args[1], args[2]

	tarballs, err := filepath.Glob(filepath.Join(tarballDir, "*.tgz"))
	if err != nil {
		return err
	}
	sort.Strings(tarballs)
	artifacts := []manifestArtifact{}
	for _, tarball := range tarballs {
		info, err := os.Stat(tarball)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, manifestArtifact{
			Name:      filepath.Base(tarball),
			SizeBytes: info.Size(),
		})
	}

	repository := os.Getenv("GITHUB_REPOSITORY")
	if repository == "" {
		repository = "local"
	}
	gitSHA := os.Getenv("GITHUB_SHA")
	if gitSHA == "" {
		out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD").Output()
		if err != nil {
			return fmt.Errorf("resolve git sha: %w", err)
		}
		gitSHA = strings.TrimSpace(string(out))
	}
	stagingDir, err := filepath.Rel(repoRoot, tarballDir)
	if err != nil {
		return err
	}

	manifest := releaseManifest{
		Repository:     repository,
		GitSHA:         gitSHA,
		GeneratedAtUTC: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Artifacts:      artifacts,
		Distribution: manifestDistribution{
			Type:         "npm",
			PackageCount: len(artifacts),
			StagingDir:   filepath.ToSlash(stagingDir),
		},
	}

	// Python json.dumps(indent=2) + trailing newline; SetEscapeHTML(false)
	// matches Python's default of not escaping HTML characters.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		return err
	}
	return os.WriteFile(manifestPath, buf.Bytes(), 0o644)
}
