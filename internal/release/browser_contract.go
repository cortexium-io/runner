package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

var browserGames = []string{"Sudoku", "Suguru", "Nonogram", "Binary", "Futoshiki", "Shikaku", "Hashi", "Calcudoku", "Kakuro"}

// This comparable summary travels in the existing opaque qualification receipt.
// Status describes diagnostic evidence, not qualification acceptance.
// Full browser evidence stays in its owned package. Its encoded bound leaves
// room in the unchanged native input budget even for a wide correction receipt.
const maxBrowserFeedback = 768

type BrowserFailureFeedback struct {
	Status       string                `json:"status"`
	Kind         string                `json:"kind,omitempty"`
	Phase        string                `json:"phase,omitempty"`
	Path         string                `json:"path,omitempty"`
	Selector     string                `json:"selector,omitempty"`
	Cell         string                `json:"cell,omitempty"`
	Expected     BrowserStateFeedback  `json:"expected,omitzero"`
	Actual       BrowserStateFeedback  `json:"actual,omitzero"`
	Differences  int                   `json:"differences,omitempty"`
	Reload       BrowserReloadFeedback `json:"reload,omitzero"`
	ReportSHA256 string                `json:"report_sha256"`
	Issue        string                `json:"issue,omitempty"`
}

type BrowserStateFeedback struct {
	Recorded bool                `json:"recorded"`
	Present  bool                `json:"present"`
	Identity string              `json:"identity,omitempty"`
	Text     string              `json:"text,omitempty"`
	Value    string              `json:"value,omitempty"`
	Fill     string              `json:"fill,omitempty"`
	Visible  bool                `json:"visible"`
	SHA256   string              `json:"sha256,omitempty"`
	Keys     int                 `json:"keys,omitempty"`
	Lines    BrowserLineFeedback `json:"lines,omitzero"`
}

type BrowserLineFeedback struct {
	Recorded bool `json:"recorded"`
	Count    int  `json:"count"`
}

type BrowserReloadFeedback struct {
	Status         string                  `json:"status"`
	DocumentStatus int                     `json:"document_status,omitempty"`
	Responses      int                     `json:"responses,omitempty"`
	Response       BrowserResponseFeedback `json:"response,omitzero"`
}

type BrowserResponseFeedback struct {
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
	Status  int    `json:"status,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

type browserBox struct{ X, Y, Width, Height float64 }
type browserGeometry struct {
	Engine, Key, Path string
	Width             int
	Board             browserBox
	Edit              *browserEdit
}
type browserPaint struct {
	Grid, First, Last int
	Cells             []int
	Scroll            []float64
}
type browserEdit struct {
	Input     struct{ Touch, Keyboard int }
	TouchDrag string
	Paint     *browserPaint
}
type browserJourney struct {
	Engine, Device, Game, Path string
	States                     []string
	Edit                       browserEdit
	ResumeInput                struct{ Touch, Keyboard int }
	Bounds                     struct {
		Viewport struct{ Width, Height float64 }
		Board    browserBox
	}
}

func browserAssertionFailure(data []byte) *sourceFailure {
	var receipt struct {
		Failure           string
		FailureDiagnostic json.RawMessage
		FailureContext    struct {
			Engine, Device, Game, Geometry, Phase, Path string
			Width, Height                               int
			Board                                       *browserBox
		}
	}
	if json.Unmarshal(data, &receipt) != nil || !strings.HasPrefix(receipt.Failure, "AssertionError") {
		return nil
	}
	c := receipt.FailureContext
	bounded := func(s string, n int) string {
		s = strings.Join(strings.Fields(s), " ")
		if len(s) > n {
			s = strings.ToValidUTF8(s[:n], "")
		}
		return s
	}
	parts := []string{}
	for _, value := range []string{c.Engine, c.Device, c.Game, c.Geometry} {
		if value != "" {
			parts = append(parts, bounded(value, 60))
		}
	}
	if c.Width > 0 && c.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", c.Width, c.Height))
	}
	if c.Phase != "" {
		parts = append(parts, bounded(c.Phase, 80))
	}
	prefix := "candidate browser obligation failed: "
	if len(parts) > 0 {
		prefix += strings.Join(parts, "/") + ": "
	}
	metrics := ""
	if c.Board != nil {
		metrics = fmt.Sprintf("; board %.0fx%.0f at %.0f,%.0f", c.Board.Width, c.Board.Height, c.Board.X, c.Board.Y)
	}
	message := bounded(strings.SplitN(receipt.Failure, "\n", 2)[0], max(0, 512-len(prefix)-len(metrics)))
	return &sourceFailure{reason: bounded(prefix+message+metrics, 512), browser: browserFailureFeedback(data, receipt.FailureDiagnostic, c.Path)}
}

func browserFailureFeedback(report, raw json.RawMessage, path string) BrowserFailureFeedback {
	sum := sha256.Sum256(report)
	f := BrowserFailureFeedback{Status: "missing", ReportSHA256: hex.EncodeToString(sum[:])}
	var d struct {
		Status, Kind, Phase, Path, Selector, Cell string
		Expected, Actual                          json.RawMessage
		Differences                               int
		Reload                                    struct {
			Status, Path   string
			DocumentStatus int
			Responses      []BrowserResponseFeedback
		}
	}
	if len(raw) == 0 || string(raw) == "null" {
		f.Issue = "diagnostic not collected"
		return f
	}
	if json.Unmarshal(raw, &d) != nil {
		f.Issue = "invalid diagnostic"
		return f
	}
	f.Status = "complete"
	if d.Status != "complete" {
		f.Status = "partial"
	}
	if d.Status == "truncated" {
		f.Status = "truncated"
	}
	clip := func(value string, limit int) string {
		if len(value) > limit {
			f.Status = "truncated"
			return strings.ToValidUTF8(value[:limit], "")
		}
		return value
	}
	publicPath := func(value string) string {
		if !regexp.MustCompile(`^/[A-Za-z0-9/_-]*$`).MatchString(value) {
			if value != "" && f.Status == "complete" {
				f.Status = "partial"
			}
			return ""
		}
		return clip(value, 80)
	}
	switch d.Kind {
	case "visible-state", "saved-state", "reload":
		f.Kind = d.Kind
	default:
		f.Status, f.Issue = "partial", "unsupported diagnostic kind"
	}
	if d.Phase == "reload" || d.Phase == "home-resume" {
		f.Phase = d.Phase
	}
	if d.Path != "" {
		path = d.Path
	} else if d.Reload.Path != "" {
		path = d.Reload.Path
	}
	f.Path, f.Selector, f.Cell = publicPath(path), clip(d.Selector, 80), clip(d.Cell, 24)
	if f.Path == "" && f.Status == "complete" {
		f.Status = "partial"
	}
	state := func(raw json.RawMessage) BrowserStateFeedback {
		if len(raw) == 0 {
			if f.Status == "complete" {
				f.Status = "partial"
			}
			return BrowserStateFeedback{}
		}
		if string(raw) == "null" {
			return BrowserStateFeedback{Recorded: true}
		}
		var s struct {
			Identity, Text, Fill *string
			Value                json.RawMessage
			Visible, Present     *bool
			Lines                *int
		}
		if json.Unmarshal(raw, &s) != nil || s.Lines == nil && (s.Present == nil || *s.Present) && (s.Visible == nil || s.Identity == nil || s.Text == nil || s.Fill == nil || len(s.Value) == 0) {
			if f.Status == "complete" {
				f.Status = "partial"
			}
			return BrowserStateFeedback{}
		}
		text := func(value *string, limit int) string {
			if value == nil {
				return ""
			}
			return clip(*value, limit)
		}
		present, visible := true, false
		if s.Present != nil {
			present = *s.Present
		}
		if s.Visible != nil {
			visible = *s.Visible
		}
		if s.Lines != nil {
			if s.Visible == nil || *s.Lines < 0 || *s.Lines > 4096 {
				if f.Status == "complete" {
					f.Status = "partial"
				}
				return BrowserStateFeedback{}
			}
			return BrowserStateFeedback{Recorded: true, Present: present, Identity: text(s.Identity, 64), Visible: visible, Lines: BrowserLineFeedback{Recorded: true, Count: *s.Lines}}
		}
		var value *string
		if len(s.Value) > 0 && json.Unmarshal(s.Value, &value) != nil {
			if f.Status == "complete" {
				f.Status = "partial"
			}
			return BrowserStateFeedback{}
		}
		return BrowserStateFeedback{
			Recorded: true,
			Present:  present,
			Identity: text(s.Identity, 64),
			Text:     text(s.Text, 24),
			Value:    text(value, 16),
			Fill:     text(s.Fill, 24),
			Visible:  visible,
		}
	}
	if d.Kind == "visible-state" {
		f.Expected, f.Actual = state(d.Expected), state(d.Actual)
		if f.Selector == "" && f.Status == "complete" {
			f.Status = "partial"
		}
	}
	if d.Kind == "saved-state" {
		saved := func(raw json.RawMessage) BrowserStateFeedback {
			var s struct {
				SHA256 string
				Keys   *int
			}
			if json.Unmarshal(raw, &s) != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s.SHA256) || s.Keys == nil || *s.Keys < 0 || *s.Keys > 4096 {
				if f.Status == "complete" {
					f.Status = "partial"
				}
				return BrowserStateFeedback{}
			}
			return BrowserStateFeedback{Recorded: true, SHA256: s.SHA256, Keys: *s.Keys}
		}
		f.Expected, f.Actual = saved(d.Expected), saved(d.Actual)
	}
	if d.Differences >= 0 && d.Differences <= 4096 {
		f.Differences = d.Differences
		if d.Differences > 1 {
			f.Status = "truncated"
		}
	} else if f.Status == "complete" {
		f.Status = "partial"
	}
	if d.Kind == "visible-state" && d.Differences < 1 && f.Status == "complete" {
		f.Status = "partial"
	}
	if d.Reload.Status != "" {
		switch d.Reload.Status {
		case "complete":
			f.Reload.Status = "complete"
		case "truncated":
			f.Reload.Status, f.Status = "truncated", "truncated"
		case "partial", "missing":
			f.Reload.Status = d.Reload.Status
			if f.Status != "truncated" {
				f.Status = "partial"
			}
		default:
			f.Reload.Status = "missing"
			if f.Status != "truncated" {
				f.Status = "partial"
			}
		}
		if d.Reload.DocumentStatus >= 100 && d.Reload.DocumentStatus <= 599 {
			f.Reload.DocumentStatus = d.Reload.DocumentStatus
		} else if f.Status == "complete" {
			f.Status = "partial"
		}
		// Navigation/availability responses are retained privately. Prefer the
		// POST admission; a second POST makes this public summary truncated.
		responses := []BrowserResponseFeedback{}
		for _, r := range d.Reload.Responses {
			if r.Method == "POST" && strings.HasPrefix(r.Path, "/api/") {
				responses = append(responses, r)
			}
		}
		f.Reload.Responses = len(responses)
		if len(responses) > 0 {
			r := responses[len(responses)-1]
			if r.Method == "GET" || r.Method == "POST" {
				f.Reload.Response.Method = r.Method
			}
			f.Reload.Response.Path = publicPath(r.Path)
			if r.Status >= 100 && r.Status <= 599 {
				f.Reload.Response.Status = r.Status
			} else if r.Outcome != "request-failed" && f.Status == "complete" {
				f.Status = "partial"
			}
			switch r.Outcome {
			case "http-ok", "http-error", "request-failed":
				f.Reload.Response.Outcome = r.Outcome
			}
			if len(responses) > 1 {
				f.Status = "truncated"
			}
		} else if f.Status == "complete" {
			f.Status = "partial"
		}
	} else {
		f.Reload.Status = "missing"
		if f.Status != "truncated" {
			f.Status = "partial"
		}
	}
	// JSON escaping also consumes the byte budget. Trim only this explicitly
	// marked public summary, never the source assertion or private evidence.
	fields := []*string{
		&f.Path,
		&f.Selector,
		&f.Cell,
		&f.Expected.Identity,
		&f.Expected.Text,
		&f.Expected.Value,
		&f.Expected.Fill,
		&f.Actual.Identity,
		&f.Actual.Text,
		&f.Actual.Value,
		&f.Actual.Fill,
		&f.Reload.Response.Path,
		&f.Reload.Response.Outcome,
	}
	for {
		encoded, _ := json.Marshal(f)
		if len(encoded) <= maxBrowserFeedback {
			return f
		}
		f.Status = "truncated"
		var longest *string
		for _, value := range fields {
			if longest == nil || len(*value) > len(*longest) {
				longest = value
			}
		}
		if longest == nil || len(*longest) == 0 {
			return BrowserFailureFeedback{Status: "truncated", ReportSHA256: f.ReportSHA256, Issue: "diagnostic exceeded feedback budget"}
		}
		*longest = strings.ToValidUTF8((*longest)[:len(*longest)/2], "")
	}
}

// Reject old or partial receipts even if a driver mistakenly marks them complete.
// The expected geometry is derived from native publications from this candidate,
// never from a preselected passing subset or a fixed count of catalogue rows.
func validateBrowserReceipt(data, runtime []byte) error {
	var native struct {
		Status          string
		Choices, Passed int
		Observations    []struct {
			Choice struct {
				Game                         string
				Size, Rows, Columns, Maximum int
			}
			NonogramGeometry *struct{ RowTextUnits, ColumnClues int }
			Path             string
			IdenticalReopen  bool
			Error            string
		}
	}
	if json.Unmarshal(runtime, &native) != nil || native.Status != "passed" || native.Choices < 1 || native.Choices != native.Passed || len(native.Observations) != native.Choices {
		return errors.New("browser contract requires complete native catalogue proof")
	}
	geometry := map[string]string{}
	largeShikakuKey, largeShikakuSize := "", -1
	games := map[string]bool{}
	for _, row := range native.Observations {
		c := row.Choice
		if !row.IdenticalReopen || row.Error != "" || !canonicalBrowserPath(c.Game, row.Path) || c.Size < 0 || c.Size > 30 || c.Rows < 0 || c.Rows > 30 || c.Columns < 0 || c.Columns > 30 {
			return errors.New("browser catalogue lacks a valid reopened publication path")
		}
		key := fmt.Sprintf("%s:%d:%d:%d", c.Game, c.Size, c.Rows, c.Columns)
		if c.Game == "suguru" {
			if c.Maximum < 4 || c.Maximum > 6 {
				return errors.New("browser catalogue lacks Suguru control maximum")
			}
			key += fmt.Sprintf(":max:%d", c.Maximum)
		}
		if c.Game == "nonogram" {
			g := row.NonogramGeometry
			if g == nil || g.RowTextUnits < 1 || g.RowTextUnits > c.Size*3 || g.ColumnClues < 1 || g.ColumnClues > (c.Size+1)/2 {
				return errors.New("browser catalogue lacks validated Nonogram clue footprint")
			}
			key += fmt.Sprintf(":clues:%d:%d", g.RowTextUnits, g.ColumnClues)
		}
		if _, ok := geometry[key]; !ok {
			geometry[key] = row.Path
			if c.Game == "shikaku" && c.Size > largeShikakuSize {
				largeShikakuKey, largeShikakuSize = key, c.Size
			}
		}
		games[c.Game] = true
	}
	if len(games) != len(browserGames) {
		return errors.New("browser catalogue omits an interaction family")
	}
	var receipt struct {
		Contract           int
		Complete, Headless bool
		MockedResponses    bool
		Failure            string
		Engines            []string
		Journeys           []browserJourney
		Geometry           []browserGeometry
	}
	if json.Unmarshal(data, &receipt) != nil || receipt.Contract != 2 || !receipt.Complete || !receipt.Headless || receipt.MockedResponses || receipt.Failure != "" || len(receipt.Engines) != 2 || receipt.Engines[0] != "chromium" || receipt.Engines[1] != "webkit" || len(receipt.Journeys) != 36 || len(receipt.Geometry) != len(geometry)*6 {
		return errors.New("Chromium/WebKit desktop/mobile browser contract is incomplete")
	}
	seen := map[string]bool{}
	for _, row := range receipt.Journeys {
		key := row.Engine + ":" + row.Device + ":" + row.Game
		if seen[key] || (row.Engine != "chromium" && row.Engine != "webkit") || (row.Device != "desktop" && row.Device != "mobile") || !canonicalBrowserPath(lowerBrowserGame(row.Game), row.Path) {
			return errors.New("browser journey identity is invalid or duplicated")
		}
		seen[key] = true
		for _, state := range []string{"valid primary presets and secondary options", "New enters usable initial play viewport", "actual edit, Undo, Redo, saved visible state survives reload", "home resume restores saved visible edit in usable viewport", "print preview"} {
			found := false
			for _, actual := range row.States {
				found = found || actual == state
			}
			if !found {
				return errors.New("browser journey omitted a required action proof")
			}
		}
		if row.Bounds.Board.Width <= 0 || row.Bounds.Board.Height <= 0 {
			return errors.New("browser journey lacks actual board geometry")
		}
		if row.Game == "Shikaku" && !validBrowserPaint(row.Edit.Paint, 5, 0, 6) {
			return errors.New("Shikaku journey lacks exact visible rectangle/no-page-pan proof")
		}
		if row.Device == "mobile" {
			if row.ResumeInput.Touch < 1 || row.ResumeInput.Keyboard != 0 {
				return errors.New("mobile home resume lacks native touch proof")
			}
			if row.Bounds.Viewport.Width != 320 || row.Edit.Input.Touch < 1 || row.Edit.Input.Keyboard != 0 || !browserBoxFits(row.Bounds.Board, 320, 568) {
				return errors.New("mobile journey lacks touch-only edit and initial viewport proof")
			}
			if row.Game == "Shikaku" && ((row.Engine == "chromium" && row.Edit.TouchDrag != "native-verified") || (row.Engine == "webkit" && row.Edit.TouchDrag != "native-taps-verified; WebKit drag API unavailable")) {
				return errors.New("Shikaku lacks the configured native touch proof")
			}
		}
	}
	seen = map[string]bool{}
	for _, row := range receipt.Geometry {
		key := fmt.Sprintf("%s:%d:%s", row.Engine, row.Width, row.Key)
		heights := map[int]float64{320: 568, 360: 640, 390: 844}
		if seen[key] || (row.Engine != "chromium" && row.Engine != "webkit") || heights[row.Width] == 0 || geometry[row.Key] == "" || row.Path != geometry[row.Key] || !browserBoxFits(row.Board, float64(row.Width), heights[row.Width]) {
			return errors.New("browser geometry proof is incomplete, duplicated or not source-bound")
		}
		seen[key] = true
		if row.Key == largeShikakuKey {
			if row.Edit == nil || row.Edit.Paint == nil || row.Edit.Input.Touch < 1 || row.Edit.Input.Keyboard != 0 || row.Edit.Paint.Last != row.Edit.Paint.First+largeShikakuSize+1 || !validBrowserPaint(row.Edit.Paint, largeShikakuSize, row.Edit.Paint.First, row.Edit.Paint.Last) {
				return errors.New("large Shikaku geometry lacks exact touch paint/no-page-pan proof")
			}
		}
	}
	return nil
}

func validBrowserPaint(p *browserPaint, grid, first, last int) bool {
	if p == nil || grid < 2 || grid > 30 || p.Grid != grid || p.First != first || p.Last != last || first < 0 || last < 0 || first >= grid*grid || last >= grid*grid || len(p.Scroll) != 4 || math.Abs(p.Scroll[0]-p.Scroll[2]) > 1 || math.Abs(p.Scroll[1]-p.Scroll[3]) > 1 {
		return false
	}
	expected := []int{}
	for row := min(first/grid, last/grid); row <= max(first/grid, last/grid); row++ {
		for col := min(first%grid, last%grid); col <= max(first%grid, last%grid); col++ {
			expected = append(expected, row*grid+col)
		}
	}
	return len(expected) == 4 && slices.Equal(p.Cells, expected)
}

func browserBoxFits(b browserBox, width, height float64) bool {
	return b.Width > 0 && b.Height > 0 && b.X >= -1 && b.Y >= -1 && b.X+b.Width <= width+1 && b.Y+b.Height <= height+1
}

func lowerBrowserGame(game string) string {
	for _, name := range browserGames {
		if name == game {
			// The configured names are ASCII with only an initial capital.
			return string(name[0]+('a'-'A')) + name[1:]
		}
	}
	return ""
}

func canonicalBrowserPath(game, path string) bool {
	known := false
	for _, name := range browserGames {
		known = known || lowerBrowserGame(name) == game
	}
	return known && len(path) <= 160 && regexp.MustCompile(`^/`+game+`/[A-Za-z0-9_-]+$`).MatchString(path)
}
