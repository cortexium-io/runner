package release

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are simulated Docker records, not native cleanup qualification. They
// pin the runtime authority of resources created before candidate commit.
func TestCortexiumCleanupAuthenticatesPreImageAndCommittedResources(t *testing.T) {
	for _, fault := range []string{"before-image", "after-image", "foreign-anchor", "foreign-image", "wrong-engine"} {
		t.Run(fault, func(t *testing.T) {
			o, q := cortexiumPolicy(t)
			b := o.Backend.(*Live)
			q.Cortexium.Transport = ""
			work := filepath.Join(b.Config.workRoot(), q.RunID, q.Key)
			if err := os.MkdirAll(work, 0700); err != nil {
				t.Fatal(err)
			}
			j := Job{
				Request: q,
				Receipt: Receipt{Policy: o.PolicyHash},
				Worker:  filepath.Join(work, "work.json"),
			}
			if err := writeExclusive(filepath.Join(work, "resources-intent"), b.cortexiumIntent(&j)); err != nil {
				t.Fatal(err)
			}
			candidate := "sha256:" + strings.Repeat("c", 64)
			remaining := map[string]bool{"anchor": true, "builder": true, "data": true, "proof": true}
			if fault != "before-image" {
				remaining["image"], remaining["verify"] = true, true
			}
			ids := map[string]string{
				"anchor":  strings.Repeat("1", 64),
				"builder": strings.Repeat("2", 64),
				"verify":  strings.Repeat("3", 64),
			}
			var removed []string
			encode := func(v any) ([]byte, error) { return json.Marshal(v) }
			b.runCommand = func(_ context.Context, _ []byte, program string, args ...string) ([]byte, error) {
				if program != b.Config.Docker || len(args) < 3 || args[0] != "--host" || args[1] != b.Config.DockerHost {
					return nil, fmt.Errorf("foreign command %q", args)
				}
				a := args[2:]
				switch a[0] {
				case "info":
					if fault == "wrong-engine" {
						return []byte("foreign-engine"), nil
					}
					return []byte(b.Config.Engine), nil
				case "ps":
					for role, id := range ids {
						if a[len(a)-1] == "name=^/"+cortexiumName(&j, role)+"$" && remaining[role] {
							return []byte(id), nil
						}
					}
					return nil, nil
				case "image":
					switch a[1] {
					case "ls":
						if remaining["image"] {
							return []byte(candidate), nil
						}
						return nil, nil
					case "inspect":
						v := cortexiumImage{ID: a[2]}
						if a[2] == candidate {
							v.Config.Labels = cortexiumLabels(&j, "image")
							if fault == "foreign-image" {
								v.Config.Labels["cortexium.runner.run"] = "run_foreign"
							}
						} else if a[2] != b.Config.Cortexium.Runtime.Image {
							return nil, fmt.Errorf("unknown image %s", a[2])
						}
						return encode([]cortexiumImage{v})
					case "rm":
						if a[2] != candidate || !remaining["image"] {
							return nil, fmt.Errorf("foreign image removal")
						}
						remaining["image"] = false
						removed = append(removed, "image")
						return nil, nil
					}
				case "inspect":
					for role, id := range ids {
						if a[1] != id || !remaining[role] {
							continue
						}
						v := cortexiumContainer{ID: id, Image: b.Config.Cortexium.Runtime.Image}
						if role == "verify" {
							v.Image = candidate
						}
						v.Config.User = "1000:1000"
						v.Config.Labels = cortexiumLabels(&j, role)
						if fault == "foreign-anchor" && role == "anchor" {
							v.Config.Labels["cortexium.runner.attempt"] = "attempt_foreign"
						}
						for i, entry := range cortexiumProxyArgs() {
							if i%2 == 1 {
								v.Config.Env = append(v.Config.Env, entry)
							}
						}
						v.HostConfig.NetworkMode = "none"
						v.HostConfig.CapDrop = []string{"ALL"}
						v.HostConfig.SecurityOpt = []string{"no-new-privileges:true"}
						if role == "anchor" {
							v.Config.Entrypoint, v.Config.Cmd = []string{"/bin/sleep"}, []string{"infinity"}
							v.HostConfig.ReadonlyRootfs = true
							v.HostConfig.CgroupnsMode, v.HostConfig.IpcMode = "private", "private"
							v.HostConfig.Memory, v.HostConfig.NanoCPUs, v.HostConfig.PidsLimit = cortexiumResources(role)
							v.HostConfig.MemorySwap = v.HostConfig.Memory
							for _, mount := range cortexiumMounts(&j, role) {
								v.Mounts = append(v.Mounts, mount)
							}
						} else {
							v.Config.Entrypoint = []string{"/bin/true"}
							v.HostConfig.ReadonlyRootfs = role == "verify"
							v.State.Status, v.State.StartedAt = "created", "0001-01-01T00:00:00Z"
						}
						return encode([]cortexiumContainer{v})
					}
				case "rm":
					for role, id := range ids {
						if id == a[1] && remaining[role] {
							remaining[role] = false
							removed = append(removed, role)
							return nil, nil
						}
					}
				case "volume":
					for _, role := range []string{"data", "proof"} {
						name := cortexiumName(&j, role)
						switch a[1] {
						case "ls":
							if a[len(a)-1] == "name=^"+name+"$" {
								if remaining[role] {
									return []byte(name), nil
								}
								return nil, nil
							}
						case "inspect":
							if a[2] == name {
								size := "64m"
								if role == "proof" {
									size = "128m"
								}
								return encode([]any{map[string]any{
									"Name": name, "Driver": "local", "Labels": cortexiumLabels(&j, role),
									"Options": map[string]string{
										"type":   "tmpfs",
										"device": "tmpfs",
										"o":      "rw,size=" + size + ",uid=1000,gid=1000,mode=0700,nosuid,nodev,noexec",
									},
								}})
							}
						case "rm":
							if a[2] == name && remaining[role] {
								remaining[role] = false
								removed = append(removed, role)
								return nil, nil
							}
						}
					}
				}
				return nil, fmt.Errorf("unexpected cleanup command %q", a)
			}
			err := b.settleCortexiumResources(t.Context(), &j)
			valid := fault == "before-image" || fault == "after-image"
			if (err == nil) != valid {
				t.Fatal("incorrect cleanup acceptance", err)
			}
			if !valid {
				if len(removed) != 0 {
					t.Fatal("changed resources before custody refusal", removed)
				}
				return
			}
			for role, exists := range remaining {
				if exists {
					t.Fatal("owned resource retained", role)
				}
			}
			calls := len(removed)
			if err := b.settleCortexiumResources(t.Context(), &j); err != nil || len(removed) != calls {
				t.Fatal("settled rejoin repeated cleanup", err)
			}
		})
	}
}
