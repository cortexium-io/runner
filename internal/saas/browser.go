package saas

import _ "embed"

// BrowserDriver is part of the installed fixed producer identity, not source
// selected by the candidate or a repository browser-test loader.
//
//go:embed browser.mjs
var BrowserDriver []byte

//go:embed contract.md
var Contract string
