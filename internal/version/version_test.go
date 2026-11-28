package version

import (
	"runtime"
	"runtime/debug"
	"testing"
)

const (
	rev      = "0123456789abcdef0123456789abcdef01234567"
	otherRev = "fedcba9876543210fedcba9876543210fedcba98"
)

func buildInfo(mainVersion string, settings map[string]string) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		bi := &debug.BuildInfo{Main: debug.Module{Path: "example.com/m", Version: mainVersion}}
		for k, v := range settings {
			bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
		}
		return bi, true
	}
}

func noBuildInfo() (*debug.BuildInfo, bool) { return nil, false }

func TestResolve(t *testing.T) {
	t.Parallel()
	vcs := map[string]string{"vcs": "git", "vcs.revision": rev, "vcs.time": "2026-01-02T03:04:05Z", "vcs.modified": "true"}

	tests := map[string]struct {
		version, commit, date string
		read                  func() (*debug.BuildInfo, bool)
		want                  Info
	}{
		"no ldflags, no build info": {
			version: "dev", read: noBuildInfo,
			want: Info{Version: "dev", Commit: "unknown"},
		},
		"empty ldflags version": {
			read: noBuildInfo,
			want: Info{Version: "dev", Commit: "unknown"},
		},
		"go build in a checkout": {
			version: "dev", read: buildInfo("(devel)", vcs),
			want: Info{Version: "dev", Commit: rev, CommitDate: "2026-01-02T03:04:05Z", Modified: true},
		},
		"go build stamping the version from git": {
			version: "dev", read: buildInfo("v1.2.4-0.20260102030405-0123456789ab+dirty", vcs),
			want: Info{Version: "v1.2.4-0.20260102030405-0123456789ab+dirty", Commit: rev, CommitDate: "2026-01-02T03:04:05Z", Modified: true},
		},
		"go install module@version": {
			version: "dev", read: buildInfo("v1.2.3", nil),
			want: Info{Version: "v1.2.3", Commit: "unknown"},
		},
		"clean checkout": {
			version: "dev", read: buildInfo("", map[string]string{"vcs.revision": rev, "vcs.modified": "false"}),
			want: Info{Version: "dev", Commit: rev},
		},
		"ldflags win": {
			version: "1.2.3", commit: otherRev, date: "2026-03-04T05:06:07Z", read: buildInfo("v9.9.9", vcs),
			// The VCS state is about another commit and is ignored.
			want: Info{Version: "1.2.3", Commit: otherRev, CommitDate: "2026-03-04T05:06:07Z"},
		},
		"ldflags with the same commit": {
			version: "1.2.3", commit: rev, read: buildInfo("v1.2.3", vcs),
			want: Info{Version: "1.2.3", Commit: rev, CommitDate: "2026-01-02T03:04:05Z", Modified: true},
		},
		"ldflags version only": {
			version: "1.2.3", read: buildInfo("(devel)", vcs),
			want: Info{Version: "1.2.3", Commit: rev, CommitDate: "2026-01-02T03:04:05Z", Modified: true},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := resolve(tt.version, tt.commit, tt.date, tt.read)
			tt.want.GoVersion = runtime.Version()
			tt.want.Platform = runtime.GOOS + "/" + runtime.GOARCH
			if got != tt.want {
				t.Errorf("resolve() = %+v\nwant       %+v", got, tt.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		info Info
		want string
	}{
		{
			Info{Version: "1.2.3", Commit: rev, CommitDate: "2026-01-02T03:04:05Z", GoVersion: "go1.27.4", Platform: "linux/amd64"},
			"1.2.3 (commit 0123456789ab, 2026-01-02T03:04:05Z, go1.27.4 linux/amd64)",
		},
		{
			Info{Version: "dev", Commit: rev, Modified: true, GoVersion: "go1.27.4", Platform: "darwin/arm64"},
			"dev (commit 0123456789ab-dirty, go1.27.4 darwin/arm64)",
		},
		{
			Info{Version: "dev", Commit: "unknown", GoVersion: "go1.27.4", Platform: "darwin/arm64"},
			"dev (commit unknown, go1.27.4 darwin/arm64)",
		},
	}
	for _, tt := range tests {
		if got := tt.info.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestShortCommit(t *testing.T) {
	t.Parallel()
	for commit, want := range map[string]string{
		rev:                   "0123456789ab",
		"abc1234":             "abc1234",
		"unknown":             "unknown",
		"not-a-hash-but-long": "not-a-hash-but-long",
	} {
		if got := (Info{Commit: commit}).ShortCommit(); got != want {
			t.Errorf("ShortCommit(%q) = %q, want %q", commit, got, want)
		}
	}
}

func TestGet(t *testing.T) {
	t.Parallel()
	// The test binary has no -ldflags; Get must still describe it.
	info := Get()
	if info.Version == "" || info.Commit == "" || info.GoVersion != runtime.Version() {
		t.Errorf("Get() = %+v", info)
	}
	if Get() != info {
		t.Error("Get() is not stable")
	}
}
