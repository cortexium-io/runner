//go:build darwin

package securefs

import "strings"

func platformAbsolutePath(path string) string {
	for _, alias := range []string{"/var", "/tmp", "/etc"} {
		if path == alias || strings.HasPrefix(path, alias+"/") {
			return "/private" + path
		}
	}
	return path
}
