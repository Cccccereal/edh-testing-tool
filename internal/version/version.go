// Package version holds the build version. release.yml injects the release tag
// (commit timestamp, e.g. "v20260922-1030") at link time:
//
//	go build -ldflags "-X powerlevel/internal/version.Version=v20260922-1030"
//
// Local `go run` / `go test` builds keep the default "dev"; the frontend hides
// the update banner for "dev" builds. The same value is mirrored into the APK's
// versionName so the system app info, the server API and the UI agree.
package version

var Version = "dev"
