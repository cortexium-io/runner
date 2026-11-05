package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

var browserGames = []string{"Sudoku", "Suguru", "Nonogram", "Binary", "Futoshiki", "Shikaku", "Hashi", "Calcudoku", "Kakuro"}

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
		Failure        string
		FailureContext struct {
			Engine, Device, Game, Geometry, Phase string
			Width, Height                         int
			Board                                 *browserBox
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
	return &sourceFailure{reason: bounded(prefix+message+metrics, 512)}
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
