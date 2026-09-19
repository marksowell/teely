package teely

import "runtime/debug"

// Version is stamped by release/local build scripts with git describe.
var Version = "dev"

func DisplayVersion() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	if Version != "" {
		return Version
	}
	return "dev"
}
