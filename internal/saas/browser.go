package saas

import (
	_ "embed"
	"fmt"
)

// BrowserDriver is part of the installed fixed producer identity, not source
// selected by the candidate or a repository browser-test loader.
//
//go:embed browser.mjs
var BrowserDriver []byte

//go:embed container.mjs
var ContainerDriver []byte

//go:embed control.mjs
var ControlDriver []byte

func DriverDigest() string {
	return Digest([]byte(fmt.Sprintf("browser.mjs:%s\ncontainer.mjs:%s\ncontrol.mjs:%s\n", Digest(BrowserDriver), Digest(ContainerDriver), Digest(ControlDriver))))
}

//go:embed contract.md
var Contract string
