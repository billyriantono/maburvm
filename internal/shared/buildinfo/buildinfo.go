// Package buildinfo reports which build of MaburVM is running.
//
// It exists because "is what I just deployed actually live?" was unanswerable:
// the panel showed a hardcoded "Version: 1.0.0", every build looked identical,
// and the only way to tell a stale node agent from a current one was to read its
// binary's timestamp over SSH. That matters more than it sounds — this fleet
// routinely runs agents at different revisions, because a node with a long
// export in flight is deliberately left until it finishes.
package buildinfo

import (
	"runtime/debug"
	"strings"
	"time"
)

// Values are injected at link time:
//
//	-ldflags "-X github.com/maburvm/panel/internal/shared/buildinfo.Commit=$(git rev-parse HEAD)"
//
// They are deliberately not constants: a build that was not stamped should say
// so rather than claim a version it cannot know.
var (
	// Commit is the full git revision this binary was built from.
	Commit = ""
	// BuildTime is RFC3339, in UTC.
	BuildTime = ""
	// Version is a human-facing release name, when there is one.
	Version = ""
)

// Info is the answer to "which build is this?".
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	ShortSHA  string `json:"short_sha"`
	BuildTime string `json:"build_time"`
	// Stamped is false when the build carries no revision at all — an
	// unstamped binary must not be mistaken for one built from an unknown
	// commit that happens to be current.
	Stamped bool `json:"stamped"`
	GoVersion string `json:"go_version"`
}

// Get returns this binary's build information.
//
// Falls back to the revision Go records automatically when building inside a git
// checkout. That path does not apply to the container images — their Dockerfiles
// copy source without .git, on purpose, so no host artefacts enter the build —
// but it means a locally built binary is still identifiable.
func Get() Info {
	commit := strings.TrimSpace(Commit)
	buildTime := strings.TrimSpace(BuildTime)
	goVersion := ""
	dirty := false

	if bi, ok := debug.ReadBuildInfo(); ok {
		goVersion = bi.GoVersion
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if commit == "" {
					commit = s.Value
				}
			case "vcs.time":
				if buildTime == "" {
					buildTime = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}

	info := Info{
		Version:   strings.TrimSpace(Version),
		Commit:    commit,
		BuildTime: buildTime,
		Stamped:   commit != "",
		GoVersion: goVersion,
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	info.ShortSHA = ShortSHA(commit)
	if dirty && info.ShortSHA != "" {
		// An operator comparing against `git log` needs to know the binary does
		// not correspond to that commit exactly.
		info.ShortSHA += "-dirty"
	}
	return info
}

// ShortSHA abbreviates a revision to the seven characters git itself shows, so
// what the panel displays can be pasted straight into `git show`.
func ShortSHA(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// Now returns a build timestamp for stamping, in the format Get expects.
func Now() string { return time.Now().UTC().Format(time.RFC3339) }
