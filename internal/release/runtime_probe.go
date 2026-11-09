package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const runtimeProbeOutputLimit = 4 << 20

type nativeProbeChoice struct {
	Game    string `json:"game"`
	Family  string `json:"family"`
	Size    int    `json:"size"`
	Rows    int    `json:"rows"`
	Columns int    `json:"columns"`
	Maximum int    `json:"maximum"`
	Band    string `json:"band"`
	Density string `json:"density"`
}

func (c *nativeProbeChoice) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 8 {
		return errors.New("runtime choice fields are incomplete")
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("runtime choice field is null")
		}
	}
	type choice nativeProbeChoice
	var decoded choice
	if err := decodeProbeJSON(data, &decoded); err != nil {
		return err
	}
	*c = nativeProbeChoice(decoded)
	return nil
}

type nativeProbeObservation struct {
	Choice       *nativeProbeChoice `json:"choice"`
	Milliseconds *int64             `json:"milliseconds"`
	Reopened     *bool              `json:"identicalReopen"`
	Error        string             `json:"error,omitempty"`
	Path         string             `json:"path,omitempty"`
	Geometry     *struct {
		RowTextUnits int `json:"rowTextUnits"`
		ColumnClues  int `json:"columnClues"`
	} `json:"nonogramGeometry,omitempty"`
}

type nativeProbeReport struct {
	Status       string                   `json:"status"`
	PublicRoutes *[]string                `json:"publicRoutes"`
	Choices      *int                     `json:"choices"`
	Passed       *int                     `json:"passed"`
	Maximum      *int64                   `json:"maxMs"`
	P95          *int64                   `json:"p95Ms"`
	Observations []nativeProbeObservation `json:"observations"`
	Retired      *bool                    `json:"retiredEndpoint404"`
	Foreign      *bool                    `json:"foreignOrigin403"`
}

func decodeProbeJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("runtime report has trailing data")
	}
	return nil
}

// This validates what the authenticated probe declared. There is no independent
// retained catalogue here, so a failed report can only provide blocked facts.
func validateFailedNativeProbe(data []byte) (nativeProbeReport, error) {
	var r nativeProbeReport
	invalid := func() (nativeProbeReport, error) {
		return r, errors.New("runtime probe failed report is incomplete or inconsistent")
	}
	if len(data) == 0 || len(data) > runtimeProbeOutputLimit || decodeProbeJSON(data, &r) != nil || r.Status != "failed" || r.PublicRoutes == nil || len(*r.PublicRoutes) != 0 || r.Choices == nil || r.Passed == nil || r.Maximum == nil || r.P95 == nil || r.Retired == nil || !*r.Retired || r.Foreign == nil || !*r.Foreign || *r.Choices < 1 || *r.Choices > 1000 || len(r.Observations) != *r.Choices || *r.Passed < 0 || *r.Passed >= *r.Choices {
		return invalid()
	}
	seen := map[nativeProbeChoice]bool{}
	durations := make([]int64, 0, *r.Choices)
	passed := 0
	token := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	for _, o := range r.Observations {
		if o.Choice == nil || o.Milliseconds == nil || o.Reopened == nil {
			return invalid()
		}
		c := *o.Choice
		if !slices.ContainsFunc(browserGames, func(game string) bool { return strings.ToLower(game) == c.Game }) || !token.MatchString(c.Family) || !token.MatchString(c.Band) || (c.Density != "" && !token.MatchString(c.Density)) || c.Size < 0 || c.Size > 1000 || c.Rows < 0 || c.Rows > 1000 || c.Columns < 0 || c.Columns > 1000 || c.Maximum < 0 || c.Maximum > 1000 || seen[c] || *o.Milliseconds < 0 || *o.Milliseconds > (12*time.Minute).Milliseconds() || len(o.Error) > 4096 {
			return invalid()
		}
		seen[c] = true
		durations = append(durations, *o.Milliseconds)
		if o.Path != "" && !canonicalBrowserPath(c.Game, o.Path) {
			return invalid()
		}
		if o.Geometry != nil && (c.Game != "nonogram" || o.Geometry.RowTextUnits < 1 || o.Geometry.RowTextUnits > 1000 || o.Geometry.ColumnClues < 1 || o.Geometry.ColumnClues > c.Size) {
			return invalid()
		}
		if o.Error == "" {
			if !*o.Reopened || o.Path == "" || (c.Game == "nonogram" && o.Geometry == nil) {
				return invalid()
			}
			passed++
		} else if *o.Reopened {
			return invalid()
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	if passed != *r.Passed || *r.Maximum != durations[len(durations)-1] || *r.P95 != durations[(len(durations)*95-1)/100] {
		return invalid()
	}
	return r, nil
}

func failedNativeProbeNote(r nativeProbeReport, digest string) string {
	note := fmt.Sprintf("Runtime probe reported %d/%d passed; blocked", *r.Passed, *r.Choices)
	failures, shown := *r.Choices-*r.Passed, 0
	http := regexp.MustCompile(`^/api/games/[a-z]+ HTTP ([1-5][0-9]{2}), wanted 200$`)
	for _, o := range r.Observations {
		if o.Error == "" || shown == 3 {
			continue
		}
		c := *o.Choice
		cause := "probe error"
		if match := http.FindStringSubmatch(o.Error); len(match) == 2 {
			cause = "HTTP " + match[1]
		}
		entry := fmt.Sprintf("; %s/%s size%d %s %s %dms", c.Game, c.Family, c.Size, c.Band, cause, *o.Milliseconds)
		// Keep the digest and an explicit omission count even for long identities.
		if len(note)+len(entry) > 300 {
			break
		}
		note += entry
		shown++
	}
	if shown < failures {
		note += fmt.Sprintf("; %d further failures retained", failures-shown)
	}
	return note + "; report sha256=" + digest + "; cause/scope unestablished"
}

type runtimeProbeInspection struct {
	ID     string `json:"Id"`
	Name   string
	Image  string
	Config struct{ Labels map[string]string }
	State  struct {
		Status   string
		Running  *bool
		ExitCode *int
	}
}

func (b *Live) inspectRuntimeProbe(ctx context.Context, j *Job, name string, exit int) (runtimeProbeInspection, error) {
	data, err := b.docker(ctx, "inspect", name)
	if err != nil {
		return runtimeProbeInspection{}, err
	}
	var rows []runtimeProbeInspection
	if json.Unmarshal(data, &rows) != nil || len(rows) != 1 {
		return runtimeProbeInspection{}, errors.New("runtime probe inspection is ambiguous; retain resource")
	}
	i := rows[0]
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(i.ID) || i.Name != "/"+name || i.Image != j.Receipt.Image || i.Config.Labels["cortexium.runner.run"] != j.Request.RunID || i.Config.Labels["cortexium.runner.attempt"] != j.Request.Key || i.State.Status != "exited" || i.State.Running == nil || *i.State.Running || i.State.ExitCode == nil || *i.State.ExitCode != exit {
		return i, errors.New("runtime probe ownership or settled exit differs; retain resource")
	}
	if err = writeExclusive(filepath.Join(j.Receipt.Package, "runtime-probe-inspect.json"), data); err != nil {
		return i, err
	}
	return i, nil
}

func retainRuntimeProbeOutput(j *Job, data []byte, complete bool, exit int) error {
	retained := data[:min(len(data), runtimeProbeOutputLimit)]
	if err := writeExclusive(filepath.Join(j.Receipt.Package, "runtime-http.stdout.log"), retained); err != nil {
		return err
	}
	metadata := struct {
		Bytes          int    `json:"bytes"`
		SHA256         string `json:"sha256"`
		Complete       bool   `json:"complete"`
		CapturedBytes  int    `json:"capturedBytes"`
		CapturedSHA256 string `json:"capturedSHA256"`
		Exit           int    `json:"commandExit"`
	}{len(retained), Digest(retained), complete && len(retained) == len(data), len(data), Digest(data), exit}
	raw, _ := json.Marshal(metadata)
	return writeExclusive(filepath.Join(j.Receipt.Package, "runtime-http.stdout.json"), raw)
}

func (b *Live) runtimeProbe(ctx context.Context, j *Job, server string) (err error) {
	name := "runner-runtime-" + j.Request.Key
	raw, runErr := b.docker(ctx, "run", "--name", name, "--label", "cortexium.runner.run="+j.Request.RunID, "--label", "cortexium.runner.attempt="+j.Request.Key, "--pull=never", "--network", "container:"+server, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--memory", "128m", "--pids-limit", "32", "--entrypoint", "/probe", "--mount", "type=bind,src="+b.Config.Delivery.Probe+",dst=/probe,readonly", j.Receipt.Image)
	exit, complete := 0, runErr == nil
	var failure *commandFailure
	if errors.As(runErr, &failure) {
		exit, complete = failure.code, true
	} else if runErr != nil {
		exit = -1
	}
	if err = retainRuntimeProbeOutput(j, raw, complete, exit); err != nil {
		return errors.Join(runErr, err)
	}
	if runErr != nil && (failure == nil || failure.code != 1) {
		return runErr
	}
	i, inspectErr := b.inspectRuntimeProbe(ctx, j, name, exit)
	if inspectErr != nil {
		return errors.Join(runErr, inspectErr, &commandUncertain{program: "runtime probe settlement"})
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if cleanupErr := b.removeSettledContainer(cleanup, i.ID, j.Request.RunID, j.Receipt.Image); cleanupErr != nil {
			err = errors.Join(err, cleanupErr, &commandUncertain{program: "runtime probe cleanup"})
		}
	}()
	if len(raw) > runtimeProbeOutputLimit {
		return errors.New("runtime probe stdout truncated; retained bytes and hash are not complete report evidence")
	}
	if runErr == nil {
		// Preserve the successful path's existing acceptance contract.
		var observed struct {
			Status          string
			Choices, Passed int
		}
		if json.Unmarshal(raw, &observed) != nil || observed.Status != "passed" || observed.Choices == 0 || observed.Choices != observed.Passed {
			return errors.New("runtime catalogue proof failed")
		}
		return writeExclusive(filepath.Join(j.Receipt.Package, "runtime-http.json"), raw)
	}
	r, validateErr := validateFailedNativeProbe(raw)
	if validateErr != nil {
		return validateErr
	}
	if err = writeExclusive(filepath.Join(j.Receipt.Package, "runtime-http.json"), raw); err != nil {
		return err
	}
	return errors.New(failedNativeProbeNote(r, Digest(raw)))
}
