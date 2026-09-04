package sandboxpath

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/securefs"
)

const npmCacheDirectory = ".npm"

func NPMCachePolicyPath() string {
	return "~/" + npmCacheDirectory
}

func NPMCacheRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve npm sandbox write root: %w", err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("npm sandbox write root requires an absolute user home: %s", home)
	}
	return securefs.AbsolutePath(filepath.Join(home, npmCacheDirectory))
}
