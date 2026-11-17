//go:build darwin

package execution

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSourceNativeImageProfileUsesExactReadRootsWithoutModelCall(t *testing.T) {
	binary := os.Getenv("RUNNER_SOURCE_NATIVE_CODEX")
	if binary == "" {
		t.Skip("set RUNNER_SOURCE_NATIVE_CODEX for actual configured Codex sandbox filesystem proof; no model turn")
	}
	s, a := sourceSharedFixture(t)
	w, err := s.workspaces.load(a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, 24, 24))
	canvas.Set(8, 8, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&picture, canvas); err != nil {
		t.Fatal(err)
	}
	const id = "attempt_fixture/image.png"
	if err := s.disk.Write("artifacts/"+id, picture.Bytes(), 4096); err != nil {
		t.Fatal(err)
	}
	a.Inputs["image"] = runner.Value{ArtifactID: id}
	a.Setup.Profile = runner.Profile{
		Harness: "native", Model: "gpt-6.2-sol", Reasoning: "xhigh", Access: "workspace-read", MaxInput: 8 << 10, MaxOutput: 512,
		Native: &runner.NativeProfile{Harness: "codex", Binary: binary, Context: "lean", Tools: runner.CapabilitySelection{Mode: "native"}, Skills: runner.CapabilitySelection{Mode: "none"}},
	}
	if err := a.Setup.Profile.Native.Validate(); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(s.disk.Root, "native-image-proof")
	if err := os.MkdirAll(filepath.Join(control, "tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	a.OutputDir = filepath.Join(s.disk.Root, "artifacts", a.AttemptID)
	if err := os.MkdirAll(a.OutputDir, 0700); err != nil {
		t.Fatal(err)
	}
	spec := harnessSpec{Root: s.disk.Root, Directory: w.WorkDir, Control: control, Assignment: a, Binary: binary}
	args := strings.Join(harnessArgs(spec, "/schema", nil), " ")
	if !strings.Contains(args, "--strict-config") || strings.Contains(args, "features.view_image=false") || a.Setup.Profile.Native.Network {
		t.Fatal("image-capable native profile changed its declared tools/context/network")
	}
	probe := `const fs=require('node:fs');const [source,image,secret]=process.argv.slice(1);if(!fs.readFileSync(source+'/proof.html','utf8').includes('Current captured source'))throw Error('source inaccessible');if(fs.readFileSync(image).subarray(1,4).toString()!=='PNG')throw Error('declared image inaccessible');for(const f of [()=>fs.readFileSync(secret),()=>fs.writeFileSync(source+'/forbidden','no')]){let denied=false;try{f();}catch(e){denied=['EPERM','EACCES','EROFS'].includes(e.code);}if(!denied)throw Error('source/state boundary failed');}console.log('native image/source reads allowed; source writes and other coordinator state denied');`
	cmd := exec.CommandContext(t.Context(), binary,
		"-c", "permissions.runner-native.filesystem="+harnessFilesystem(spec),
		"-c", "permissions.runner-native.network.enabled=false",
		"sandbox", "-P", "runner-native", "-C", w.WorkDir, "--", sourceSharedNode(t), "-e", probe,
		w.WorkDir, filepath.Join(s.disk.Root, "artifacts", id), filepath.Join(s.disk.Root, "work", "other-state", "secret"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("actual native image filesystem configuration: %v %s", err, out)
	} else {
		t.Log(strings.TrimSpace(string(out)))
	}
}
