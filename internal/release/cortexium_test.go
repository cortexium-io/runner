package release

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/saas"
)

func cortexiumPolicy(t *testing.T) (*Operator, Request) {
	t.Helper()
	h := strings.Repeat("a", 64)
	c := Config{
		Project:    "cortexium-local",
		StateDir:   filepath.Join(t.TempDir(), "private"),
		Docker:     "/fixed/docker",
		DockerHost: "ssh://fixture",
		Engine:     "fixture-engine",
		Cortexium: &CortexiumConfig{
			Runtime: saas.ContainerRuntime{
				Image:        "sha256:" + h,
				DriverSHA256: h,
				ChromeSHA256: h,
				Node:         "26.13.1",
				Playwright:   "1.65.0",
			},
			Timeout:      "18m",
			StorageLimit: 1 << 30,
			MinFree:      1 << 30,
		},
	}
	o, err := loadCortexium(c)
	if err != nil {
		t.Fatal(err)
	}
	q := Request{
		Project: c.Project,
		RunID:   "run_fixture",
		Key:     "attempt_fixture",
		Action:  "cortexium-browser",
		Source:  strings.Repeat("b", 40),
		Cortexium: &CortexiumRequest{
			Version:         1,
			Deadline:        time.Now().Add(19 * time.Minute).UnixNano(),
			PolicySHA256:    o.PolicyHash,
			Transport:       "payload",
			ArtifactID:      "attempt_checks/result",
			Bytes:           1,
			SHA256:          h,
			PatchID:         "attempt_join/changes.patch",
			PatchSHA256:     h,
			AuthorityID:     "attempt_authority/result",
			AuthoritySHA256: h,
			ApprovalSHA256:  Digest([]byte(`"APPROVE"`)),
		},
	}
	return o, q
}

type unreadBody struct{ reads int }

func (b *unreadBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }

func TestCortexiumRefusesForeignControlBeforeStagingOrReading(t *testing.T) {
	for _, fault := range []string{
		"project",
		"action",
		"source",
		"key",
		"policy",
		"previous",
		"candidate",
		"recovery",
		"retry",
		"continuation",
		"transport",
		"deadline",
	} {
		t.Run(fault, func(t *testing.T) {
			o, q := cortexiumPolicy(t)
			switch fault {
			case "project":
				q.Project = "jacks-break"
			case "action":
				q.Action = "qualify"
			case "source":
				q.Source = "refs/heads/main"
			case "key":
				q.Key = "../foreign"
			case "policy":
				q.Cortexium.PolicySHA256 = strings.Repeat("c", 64)
			case "previous":
				q.Previous = json.RawMessage(`{}`)
			case "candidate":
				q.Candidate = "foreign"
			case "recovery":
				q.Recovery = json.RawMessage(`{}`)
			case "retry":
				q.Retry = json.RawMessage(`{}`)
			case "continuation":
				q.SourceContinuation = true
			case "transport":
				q.Cortexium.Transport = "execute"
			case "deadline":
				q.Cortexium.Deadline = time.Now().Add(-time.Minute).UnixNano()
			}
			body := &unreadBody{}
			if _, err := o.CallInput(t.Context(), q, body); err == nil {
				t.Fatal("foreign scope admitted")
			}
			if body.reads != 0 {
				t.Fatal("read body before scope/admission", body.reads)
			}
			if _, err := os.Stat(o.Directory); !os.IsNotExist(err) {
				t.Fatal("invalid control created private staging", err)
			}
		})
	}
}

func TestCortexiumStorageRefusalPrecedesPayloadAndPerOperationFiles(t *testing.T) {
	o, q := cortexiumPolicy(t)
	if err := os.Mkdir(o.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(o.Directory, "retained"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	f.Close() // sparse, counted logical bytes, no disk filler
	body := &unreadBody{}
	if _, err = o.CallInput(t.Context(), q, body); err == nil {
		t.Fatal("full managed state admitted payload")
	}
	if body.reads != 0 {
		t.Fatal("oversized state consumed payload")
	}
	entries, err := os.ReadDir(o.Directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("refused admission created files", entries, err)
	}
}

func TestOperatorFramePreservesLegacyBoundAndExactBuildBody(t *testing.T) {
	_, q := cortexiumPolicy(t)
	header, _ := json.Marshal(q)
	body := []byte{0, 1, '\n', 255}
	got, remaining, err := ReadRequest(bytes.NewReader(append(append(header, '\n'), body...)))
	if err != nil || got.Key != q.Key {
		t.Fatal(err)
	}
	b, err := io.ReadAll(remaining)
	if err != nil || !bytes.Equal(b, body) {
		t.Fatal("buffered payload lost", b, err)
	}
	for _, tail := range [][]byte{[]byte(` {}`), bytes.Repeat([]byte(" "), 32<<10)} {
		if _, _, err = ReadRequest(bytes.NewReader(append([]byte(`{"key":"ordinary"}`), tail...))); err == nil {
			t.Fatal("legacy trailing/oversize data accepted")
		}
	}
	if _, _, err = ReadRequest(strings.NewReader("{\n \"key\":\"ordinary\"\n}\n")); err != nil {
		t.Fatal("legacy pretty JSON refused", err)
	}
}

func TestCortexiumPolicyCannotAcquireLegacyProductionAuthority(t *testing.T) {
	for _, fault := range []string{"engine", "source", "delivery", "production", "runtime"} {
		t.Run(fault, func(t *testing.T) {
			o, _ := cortexiumPolicy(t)
			c := o.Backend.(*Live).Config
			switch fault {
			case "engine":
				c.Engine = ""
			case "source":
				c.Repository = "foreign/source"
			case "delivery":
				c.Delivery = &DeliveryConfig{}
			case "production":
				c.PublicURL = "https://example.invalid"
			case "runtime":
				c.Cortexium.Runtime.Image = "latest"
			}
			if _, err := loadCortexium(c); err == nil {
				t.Fatal("mixed policy accepted")
			}
		})
	}
}
