package saas

import (
	"encoding/json"
	"errors"
	"path"
	"strings"
)

// Authority is an operator-staged, frozen input, never candidate-owned policy.
// The four reference manifests come from the positively pinned empty Genny
// recipe. Paths identify the actual selected app, including a declared submodule.
type Authority struct {
	Version       int    `json:"version"`
	BriefCommit   string `json:"brief_commit"`
	BriefSHA256   string `json:"brief_sha256"`
	GennyCommit   string `json:"genny_commit"`
	Base          string `json:"base"`
	AppDirectory  string `json:"app_directory"`
	Module        string `json:"module"`
	ProjectName   string `json:"project_name"`
	Brief         string `json:"brief"`
	Contract      string `json:"contract"`
	GoMod         string `json:"go_mod"`
	GoSum         string `json:"go_sum"`
	Package       string `json:"package_json"`
	Lock          string `json:"package_lock_json"`
	GoSHA256      string `json:"go_sha256"`
	GoCheckSHA256 string `json:"go_check_sha256"`
	GennySHA256   string `json:"genny_sha256"`
	NodeSHA256    string `json:"node_sha256"`
	ChromeSHA256  string `json:"chrome_sha256"`
	DriverSHA256  string `json:"driver_sha256"`
}

func DecodeAuthority(b []byte, base string) (Authority, error) {
	var a Authority
	bad := errors.New("local SaaS authority differs from the admitted base, brief, pinned recipe or runtime")
	if len(b) == 0 || len(b) > MaxBundle || strictJSON(b, &a) != nil || a.Version != 1 || a.BriefCommit != BriefCommit || a.BriefSHA256 != BriefSHA256 || a.GennyCommit != GennyCommit || !commit.MatchString(base) || a.Base != base || a.AppDirectory != "platform" || a.Module == "" || strings.ContainsAny(a.Module, " \n\r\t") || strings.Contains(a.Module, "..") || path.Clean(a.Module) != a.Module {
		return a, bad
	}
	if len(a.ProjectName) < 1 || len(a.ProjectName) > 64 || Digest([]byte(a.Brief)) != BriefSHA256 || a.Contract != Contract {
		return a, bad
	}
	for _, v := range []string{a.GoSHA256, a.GoCheckSHA256, a.GennySHA256, a.NodeSHA256, a.ChromeSHA256, a.DriverSHA256} {
		if !sha.MatchString(v) {
			return a, bad
		}
	}
	if !strings.HasPrefix(a.GoMod, "module "+a.Module+"\n") || !strings.Contains(a.GoMod, "\ngo 1.27.0\n") || !strings.Contains(a.GoMod, "\ntoolchain go1.27.3\n") || strings.Contains(a.GoMod, "replace") || strings.Contains(a.GoMod, "exclude") || len(a.GoSum) == 0 || len(a.GoSum) > 64<<10 {
		return a, bad
	}
	// These reference bytes are frozen by the owning operator. They still have
	// to be ordinary manifests, rather than an executable or malformed payload.
	var p struct {
		Private bool              `json:"private"`
		Deps    map[string]string `json:"dependencies"`
		Dev     map[string]string `json:"devDependencies"`
	}
	var lock struct {
		Version int `json:"lockfileVersion"`
	}
	if json.Unmarshal([]byte(a.Package), &p) != nil || !p.Private || p.Deps["vue"] != "3.5.45" || p.Dev["vite"] != "8.3.6" || p.Dev["vitest"] != "5.1.0" || json.Unmarshal([]byte(a.Lock), &lock) != nil || lock.Version != 3 {
		return a, bad
	}
	return a, nil
}
