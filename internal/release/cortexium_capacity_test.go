package release

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cortexium-io/runner/internal/saas"
	"github.com/cortexium-io/runner/testdata/saas/proof"
)

func cortexiumPayload(t *testing.T, q *Request, runtime saas.ContainerRuntime) []byte {
	t.Helper()
	h := strings.Repeat("a", 64)
	e := saas.Evidence{
		Version:            1,
		Kind:               "cortexium-local",
		Status:             "checked",
		Base:               q.Source,
		PatchSHA256:        q.Cortexium.PatchSHA256,
		AuthoritySHA256:    q.Cortexium.AuthoritySHA256,
		ChromeSHA256:       runtime.ChromeSHA256,
		DriverSHA256:       runtime.DriverSHA256,
		ModuleSHA256:       h,
		LockSHA256:         h,
		GoVersion:          "go1.27.3",
		GoTests:            1,
		VueTests:           1,
		GoVet:              true,
		GoBuild:            true,
		VueTypecheck:       true,
		VueBuild:           true,
		GeneratedOwnership: true,
		RealWorker:         "unverified",
	}
	a := saas.Authority{GoSHA256: h, GennySHA256: h, NodeSHA256: h, GoCheckSHA256: h, Runtime: runtime}
	server, err := proof.Build(&e, a)
	if err != nil {
		t.Fatal(err)
	}
	reports := map[string][]byte{}
	for _, name := range saas.RequiredReports {
		b := []byte(`{"synthetic_contract_fixture":true}`)
		reports[name] = b
		e.Reports = append(e.Reports, saas.Report{Name: name, SHA256: Digest(b), Bytes: len(b), Complete: true})
	}
	data, err := saas.PackCheckedBuild(e, server, reports)
	if err != nil {
		t.Fatal(err)
	}
	q.Cortexium.Bytes, q.Cortexium.SHA256 = int64(len(data)), Digest(data)
	return data
}
func copyCortexiumRequest(q Request, key, transport string) Request {
	p := *q.Cortexium
	p.Transport = transport
	q.Cortexium = &p
	q.Key = key
	return q
}

type heldCortexiumBody struct {
	reader           *bytes.Reader
	entered, release chan struct{}
	once             sync.Once
}

func (b *heldCortexiumBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.reader.Read(p)
}

func TestCortexiumConcurrentAdmissionReservesCompletionWithoutHoldingTransportLease(t *testing.T) {
	o, base := cortexiumPolicy(t)
	live := o.Backend.(*Live)
	data := cortexiumPayload(t, &base, live.Config.Cortexium.Runtime)
	// Fourteen tiny authenticated pending intents reserve896MiB of future local
	// work. No disk filler/sparse allocation/native worker/product is involved.
	for i := 0; i < 14; i++ {
		q := copyCortexiumRequest(base, fmt.Sprintf("pending_%02d", i), "")
		if ok, err := o.reserveCortexiumInput(q, "payload", live); err != nil || !ok {
			t.Fatal(i, ok, err)
		}
	}
	first := copyCortexiumRequest(base, "held_first", "payload")
	held := &heldCortexiumBody{
		reader:  bytes.NewReader(data),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	type result struct {
		out Response
		err error
	}
	returned := make(chan result, 1)
	go func() { out, err := o.CallInput(t.Context(), first, held); returned <- result{out, err} }()
	<-held.entered
	second := copyCortexiumRequest(base, "independent_second", "payload")
	body := &unreadBody{}
	if _, err := o.CallInput(t.Context(), second, body); err == nil {
		t.Fatal("two operations spent the same completion reserve")
	}
	if body.reads != 0 {
		t.Fatal("overbooked request consumed its body")
	}
	if _, err := os.Stat(filepath.Join(o.Directory, "work", second.RunID, second.Key)); !os.IsNotExist(err) {
		t.Fatal("quota refusal created a per-operation intent", err)
	}
	// Stop races a real valid held upload. The tombstone releases only unused
	// future cost; the late valid body must not stage bytes or launch a worker.
	stop := copyCortexiumRequest(first, first.Key, "stop")
	stopped, err := o.CallInput(t.Context(), stop, strings.NewReader(""))
	if err != nil || stopped.State != "finished" {
		t.Fatal("cancel did not settle pending upload", stopped, err)
	}
	close(held.release)
	late := <-returned
	if late.err != nil || late.out.State != "finished" {
		t.Fatal("late upload lost original cancellation", late)
	}
	if _, err := os.Stat(filepath.Join(o.Directory, "work", first.RunID, first.Key, "input.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("late upload wrote after releasing reservation", err)
	}
	if _, err := os.Stat(filepath.Join(o.Directory, "work", first.RunID, first.Key, "started")); !os.IsNotExist(err) {
		t.Fatal("cancelled upload dispatched a worker", err)
	}
	before, err := live.cortexiumCapacity()
	if err != nil {
		t.Fatal(err)
	}
	q := copyCortexiumRequest(second, second.Key, "")
	if ok, err := o.reserveCortexiumInput(q, "payload", live); err != nil || !ok {
		t.Fatal("independent request could not use settled reserve", ok, err)
	}
	after, err := live.cortexiumCapacity()
	if err != nil || after <= before {
		t.Fatal("retained history disappeared from accounting", before, after, err)
	}
	record, err := readFile(filepath.Join(o.Directory, first.Key+".json"), 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	var job Job
	if json.Unmarshal(record, &job) != nil || !job.Done || job.Worker != "" {
		t.Fatal("cancellation/history changed", job)
	}
}

func TestCortexiumReservationRefusesForeignOrLostCustody(t *testing.T) {
	for _, fault := range []string{"changed_request", "missing_intent", "foreign_file", "oversized_context"} {
		t.Run(fault, func(t *testing.T) {
			o, base := cortexiumPolicy(t)
			live := o.Backend.(*Live)
			q := copyCortexiumRequest(base, "pending", "")
			if ok, err := o.reserveCortexiumInput(q, "payload", live); err != nil || !ok {
				t.Fatal(err)
			}
			root := filepath.Join(o.Directory, "work", q.RunID, q.Key)
			switch fault {
			case "changed_request":
				q.Source = strings.Repeat("c", 40)
			case "missing_intent":
				if err := os.Rename(filepath.Join(root, "input-request.json"), filepath.Join(root, "foreign.json")); err != nil {
					t.Fatal(err)
				}
			case "foreign_file":
				if err := os.WriteFile(filepath.Join(root, "foreign"), []byte("preserve this"), 0600); err != nil {
					t.Fatal(err)
				}
			case "oversized_context":
				if err := os.Mkdir(filepath.Join(root, "image"), 0700); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(root, "image", "server"))
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Truncate(33 << 20); err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
			if _, err := live.cortexiumCapacityFor(&q); err == nil {
				t.Fatal("foreign/changed reservation accepted")
			}
		})
	}
}
