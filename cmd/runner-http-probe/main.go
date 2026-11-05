// runner-http-probe verifies the advertised Jack’s Break catalogue against a disposable candidate service.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

type choice struct {
	Game    string `json:"game"`
	Family  string `json:"family"`
	Size    int    `json:"size"`
	Rows    int    `json:"rows"`
	Columns int    `json:"columns"`
	Maximum int    `json:"maximum"`
	Band    string `json:"band"`
	Density string `json:"density"`
}

type observation struct {
	Choice       choice `json:"choice"`
	Milliseconds int64  `json:"milliseconds"`
	Reopened     bool   `json:"identicalReopen"`
	Error        string `json:"error,omitempty"`
	Path         string `json:"path,omitempty"`
}

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "disposable candidate origin")
	pathsRaw := flag.String("paths-json", "[]", "retained dated routes")
	flag.Parse()
	var paths []string
	if json.Unmarshal([]byte(*pathsRaw), &paths) != nil || len(paths) > 12 {
		fatal(errors.New("invalid paths"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 12 * time.Second}
	request := func(method, path string, body any, index, want int, origin string) ([]byte, error) {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r, err := http.NewRequestWithContext(ctx, method, *url+path, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		r.Host = "rehearsal.example.test"
		r.Header.Set("X-Forwarded-Proto", "https")
		// Independent synthetic clients keep this sequential catalogue check within
		// unchanged per-client limits. No production/proxy configuration is widened.
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.18.%d.%d", index/250, index%250+1))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", origin)
		}
		response, err := client.Do(r)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		out, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		if response.StatusCode != want {
			return out, fmt.Errorf("%s HTTP %d, wanted %d", path, response.StatusCode, want)
		}
		if method == "POST" && want == 200 && !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
			return out, errors.New("cacheable admission")
		}
		return out, nil
	}
	origin := "https://rehearsal.example.test"
	for _, path := range append([]string{"/api/health", "/api/ready"}, paths...) {
		if _, err := request("GET", path, nil, 500, 200, origin); err != nil {
			fatal(err)
		}
	}
	raw, err := request("POST", "/api/games/choices", map[string]any{}, 500, 200, origin)
	if err != nil {
		fatal(err)
	}
	var catalogue struct {
		Status  string
		Choices []choice
	}
	if json.Unmarshal(raw, &catalogue) != nil || catalogue.Status != "success" || len(catalogue.Choices) == 0 || len(catalogue.Choices) > 1000 {
		fatal(errors.New("empty or oversized supported catalogue"))
	}
	observations := []observation{}
	durations := []int64{}
	passed := 0
	for i, c := range catalogue.Choices {
		o := observation{Choice: c}
		started := time.Now()
		inner := map[string]any{"kind": "next", "family": c.Family, "size": c.Size, "band": c.Band, "identities": []any{}}
		classic := c.Game == "sudoku" || c.Game == "suguru"
		if classic {
			inner = map[string]any{"kind": "next", "game": c.Game, "family": c.Family, "known": []any{}, "identities": []any{}}
			if c.Game == "sudoku" {
				inner["difficulty"] = c.Band
			} else {
				inner["options"] = map[string]any{"rows": c.Rows, "columns": c.Columns, "maximum": c.Maximum, "profile": c.Band}
			}
		} else if c.Density != "" {
			inner["density"] = c.Density
		}
		raw, err := request("POST", "/api/games/next", map[string]any{"game": c.Game, "request": inner}, i, 200, origin)
		var reply map[string]json.RawMessage
		if err == nil {
			err = json.Unmarshal(raw, &reply)
			if err == nil && string(reply["status"]) != `"success"` {
				err = fmt.Errorf("native result %s", reply["status"])
			}
		}
		var original json.RawMessage
		var identity map[string]any
		if err == nil {
			if classic {
				original = reply["publication"]
				var p struct{ Puzzle map[string]any }
				err = json.Unmarshal(original, &p)
				if err == nil {
					identity = map[string]any{"game": p.Puzzle["game"], "id": p.Puzzle["id"], "contentRevision": p.Puzzle["contentRevision"]}
				}
			} else {
				var result map[string]json.RawMessage
				err = json.Unmarshal(reply["result"], &result)
				original = result["publication"]
				var p struct{ Identity map[string]any }
				if err == nil {
					err = json.Unmarshal(original, &p)
					identity = p.Identity
				}
			}
		}
		o.Milliseconds = time.Since(started).Milliseconds()
		durations = append(durations, o.Milliseconds)
		if err == nil {
			endpoint := c.Game
			if classic {
				endpoint = "admit"
			}
			raw, err = request("POST", "/api/games/"+endpoint, map[string]any{"kind": "reopen", "identity": identity}, i, 200, origin)
			if err == nil {
				var reopened map[string]json.RawMessage
				err = json.Unmarshal(raw, &reopened)
				var actual json.RawMessage
				if classic {
					actual = reopened["publication"]
				} else {
					var result map[string]json.RawMessage
					if err == nil {
						err = json.Unmarshal(reopened["result"], &result)
						actual = result["publication"]
					}
				}
				var a, b any
				if err == nil {
					err = json.Unmarshal(original, &a)
				}
				if err == nil {
					err = json.Unmarshal(actual, &b)
				}
				if err == nil && !reflect.DeepEqual(a, b) {
					err = errors.New("reopen changed publication")
				}
			}
		}
		if err == nil {
			o.Path, err = publicationPath(c.Game, identity)
		}
		if err != nil {
			o.Error = err.Error()
		} else {
			o.Reopened = true
			passed++
		}
		observations = append(observations, o)
	}
	if _, err := request("POST", "/api/games/ready", map[string]any{}, 501, 404, origin); err != nil {
		fatal(err)
	}
	if _, err := request("POST", "/api/games/choices", map[string]any{}, 501, 403, "https://foreign.example.test"); err != nil {
		fatal(err)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	status := "passed"
	if passed != len(observations) {
		status = "failed"
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status":             status,
		"publicRoutes":       paths,
		"choices":            len(observations),
		"passed":             passed,
		"maxMs":              durations[len(durations)-1],
		"p95Ms":              durations[(len(durations)*95-1)/100],
		"observations":       observations,
		"retiredEndpoint404": true,
		"foreignOrigin403":   true,
	})
	if status != "passed" {
		os.Exit(1)
	}
}

// The public classic routes omit their internal identity prefix. All other
// games use the returned native ID verbatim; never invent a sample identity.
func publicationPath(game string, identity map[string]any) (string, error) {
	id, ok := identity["id"].(string)
	if !ok || identity["game"] != game || identity["contentRevision"] != "1" {
		return "", errors.New("publication identity is incomplete")
	}
	if game == "sudoku" || game == "suguru" {
		prefix := game + "-generated:"
		if !strings.HasPrefix(id, prefix) {
			return "", errors.New("classic publication identity prefix differs")
		}
		id = strings.TrimPrefix(id, prefix)
	}
	if len(id) > 128 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(id) {
		return "", errors.New("publication identity cannot form a canonical route")
	}
	return "/" + game + "/" + id, nil
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "private release HTTP:", err); os.Exit(1) }
