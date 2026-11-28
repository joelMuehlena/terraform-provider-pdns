// Package version describes the build of the provider binary: the provider
// version, the commit it was built from and the Go toolchain.
//
// Release builds set Version, Commit and CommitDate with -ldflags -X (see
// .goreleaser.yml). Builds without them (go install, go build, a binary used
// with dev_overrides) fall back to the build information the Go toolchain
// embeds in every binary (runtime/debug.ReadBuildInfo). Values set with
// -ldflags take precedence.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// Set with -ldflags "-X <module>/internal/version.<Name>=<value>".
var (
	// Version is the provider version, without a leading "v" in releases.
	Version = "dev"
	// Commit is the full hash of the commit the binary was built from.
	Commit = ""
	// CommitDate is the commit time (RFC 3339). The commit date rather than
	// the build date keeps release builds reproducible, and it is what the
	// toolchain records (vcs.time) for builds without -ldflags.
	CommitDate = ""
)

// unknownCommit is Info.Commit when neither -ldflags nor the build
// information name a commit, e.g. for a build outside a git checkout.
const unknownCommit = "unknown"

// Info describes the build of the running binary.
type Info struct {
	Version    string
	Commit     string
	CommitDate string
	// Modified reports a build from a working tree with uncommitted
	// changes (vcs.modified), so Commit alone does not describe it.
	Modified  bool
	GoVersion string
	// Platform is GOOS/GOARCH.
	Platform string
}

var get = sync.OnceValue(func() Info {
	return resolve(Version, Commit, CommitDate, debug.ReadBuildInfo)
})

// Get returns the build information of the running binary.
func Get() Info {
	return get()
}

// resolve combines the -ldflags values with the build information returned
// by readBuildInfo.
func resolve(version, commit, commitDate string, readBuildInfo func() (*debug.BuildInfo, bool)) Info {
	info := Info{
		Version:    version,
		Commit:     commit,
		CommitDate: commitDate,
		GoVersion:  runtime.Version(),
		Platform:   runtime.GOOS + "/" + runtime.GOARCH,
	}

	if bi, ok := readBuildInfo(); ok && bi != nil {
		if info.Version == "" || info.Version == "dev" {
			// "(devel)" is the version of a main module built from its own
			// directory by toolchains that do not stamp it from git.
			if v := bi.Main.Version; v != "" && v != "(devel)" {
				info.Version = v
			}
		}

		settings := map[string]string{}
		for _, s := range bi.Settings {
			settings[s.Key] = s.Value
		}
		revision := settings["vcs.revision"]
		if info.Commit == "" {
			info.Commit = revision
		}
		// The VCS state describes this build only if it is about the same
		// commit as Commit, which -ldflags may have set to another one.
		if revision != "" && revision == info.Commit {
			info.Modified = settings["vcs.modified"] == "true"
			if info.CommitDate == "" {
				info.CommitDate = settings["vcs.time"]
			}
		}
	}

	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = unknownCommit
	}
	return info
}

// ShortCommit returns the abbreviated commit hash, with a "-dirty" suffix
// for a build from a modified working tree.
func (i Info) ShortCommit() string {
	c := i.Commit
	if len(c) > 12 && isHex(c) {
		c = c[:12]
	}
	if i.Modified {
		c += "-dirty"
	}
	return c
}

func isHex(s string) bool {
	return strings.Trim(s, "0123456789abcdefABCDEF") == ""
}

// String describes the build in one line, e.g.
// "1.2.3 (commit 0123456789ab, 2026-01-02T03:04:05Z, go1.27.4 linux/amd64)".
func (i Info) String() string {
	parts := []string{"commit " + i.ShortCommit()}
	if i.CommitDate != "" {
		parts = append(parts, i.CommitDate)
	}
	parts = append(parts, i.GoVersion+" "+i.Platform)
	return fmt.Sprintf("%s (%s)", i.Version, strings.Join(parts, ", "))
}
