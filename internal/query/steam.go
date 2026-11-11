package query

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var (
	vdfPath = regexp.MustCompile(`(?m)^\s*"path"\s+"(.*)"\s*$`)
	acfName = regexp.MustCompile(`(?m)^\s*"name"\s+"(.*)"\s*$`)
)

// SteamNames returns a lookup of Steam app names for the user whose home
// directory is home. It reads Steam's library list once, on first use,
// and each asked-for app's manifest (steamapps/appmanifest_<id>.acf).
func SteamNames(home string) func(appID string) string {
	libraries := sync.OnceValue(func() []string {
		var libs []string
		for _, steam := range []string{
			filepath.Join(home, ".local/share/Steam"),
			filepath.Join(home, ".steam/steam"),
			filepath.Join(home, ".var/app/com.valvesoftware.Steam/.local/share/Steam"),
		} {
			libs = append(libs, steam)
			b, err := os.ReadFile(filepath.Join(steam, "steamapps", "libraryfolders.vdf"))
			if err != nil {
				continue
			}
			for _, m := range vdfPath.FindAllSubmatch(b, -1) {
				libs = append(libs, string(m[1]))
			}
		}
		return libs
	})
	return func(appID string) string {
		for _, c := range appID {
			if c < '0' || c > '9' {
				return "" // not an id: never build a path from it
			}
		}
		for _, lib := range libraries() {
			b, err := os.ReadFile(filepath.Join(lib, "steamapps", "appmanifest_"+appID+".acf"))
			if err != nil {
				continue
			}
			if m := acfName.FindSubmatch(b); m != nil {
				return string(m[1])
			}
		}
		return ""
	}
}
