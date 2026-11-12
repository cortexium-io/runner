package execution

import (
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/saas"
)

func saasData(disk *runner.Disk, a runner.Assignment) (saas.Evidence, map[string][]byte, error) {
	var empty saas.Evidence
	if a.Setup.Profile.Access != "workspace-read" || a.Inputs["authority"].ArtifactID == "" || a.Inputs["evidence"].ArtifactID == "" || a.Inputs["patch"].ArtifactID == "" {
		return empty, nil, errors.New("local SaaS review requires declared authority, candidate patch and check evidence")
	}
	w, err := (&Workspaces{disk: disk}).load(a.RunID)
	if err != nil {
		return empty, nil, err
	}
	if w.Candidate == nil || w.Candidate.Base != w.Base || w.Candidate.PatchID != a.Inputs["patch"].ArtifactID || w.Released || w.CleanupIntent != nil {
		return empty, nil, errors.New("local SaaS review candidate differs from the saved workspace")
	}
	read := func(name string) ([]byte, error) {
		return disk.Read("artifacts/"+a.Inputs[name].ArtifactID, a.Limits.MaxArtifact)
	}
	authority, err := read("authority")
	if err != nil {
		return empty, nil, err
	}
	bound, err := saas.DecodeAuthority(authority, w.Base)
	if err != nil {
		return empty, nil, err
	}
	patch, err := read("patch")
	if err != nil {
		return empty, nil, err
	}
	evidence, err := read("evidence")
	if err != nil {
		return empty, nil, err
	}
	if a.Setup.Profile.Mode == "saas-source-review" {
		e, files, err := saas.DecodeCheckedBuild(evidence, w.Base, saas.Digest(patch), saas.Digest(authority))
		if err == nil {
			err = saasRuntimeBinding(e, bound)
		}
		if err == nil {
			delete(files, "app/server")
			files["saas.json"], err = json.Marshal(e)
		}
		return e, files, err
	}
	e, files, err := saas.Decode(evidence, w.Base, saas.Digest(patch), saas.Digest(authority))
	if err == nil {
		err = saasRuntimeBinding(e, bound)
	}
	return e, files, err
}

func saasRuntimeBinding(e saas.Evidence, a saas.Authority) error {
	if e.Build == nil || e.Build.Runtime != a.Runtime || e.Build.GoSHA256 != a.GoSHA256 || e.Build.GennySHA256 != a.GennySHA256 || e.Build.NodeSHA256 != a.NodeSHA256 || e.Build.GoCheckSHA256 != a.GoCheckSHA256 || e.ChromeSHA256 != a.Runtime.ChromeSHA256 || e.DriverSHA256 != a.Runtime.DriverSHA256 || e.ModuleSHA256 != saas.Digest([]byte(a.GoMod)) || e.LockSHA256 != saas.Digest([]byte(a.Lock)) {
		return errors.New("local SaaS evidence runtime/manifests differ from frozen authority")
	}
	return nil
}
