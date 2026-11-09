package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	maxCaseProfiles   = 8
	maxProfileNodes   = 20000
	maxProfileSamples = 32768
	maxProfileDepth   = 256
	maxProfileStacks  = 6
	maxStackFrames    = 4
)

type timeoutCPUProfile struct {
	Process        string                `json:"process"`
	Samples        int                   `json:"samples"`
	IdleSamples    int                   `json:"idle_samples"`
	ProjectSamples int                   `json:"project_samples"`
	Stacks         []timeoutSampledStack `json:"stacks"`
	workerRuntime  bool
}

type timeoutSampledStack struct {
	Samples int                 `json:"samples"`
	Frames  []timeoutStackFrame `json:"frames"`
}

// Line and column are one-based positions in V8's executing script. Vite
// transformations can shift them relative to the checked-in file.
type timeoutStackFrame struct {
	Kind     string `json:"kind"`
	File     string `json:"file"`
	Function string `json:"function"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
}

type v8Profile struct {
	Nodes      []v8ProfileNode
	Samples    []int
	TimeDeltas []int64
	StartTime  float64
	EndTime    float64
}

type v8ProfileNode struct {
	ID        int
	Children  []int
	CallFrame struct {
		FunctionName string
		URL          string
		LineNumber   int
		ColumnNumber int
	}
}

var timeoutProfileName = regexp.MustCompile(`^CPU\.[0-9]{8}\.[0-9]{6}\.([0-9]+)\.([0-9]+)\.[0-9]+\.cpuprofile$`)

type timeoutScratchFile struct {
	name string
	info os.FileInfo
}

func timeoutScratchEntries(root *os.Root, owned os.FileInfo) ([]timeoutScratchFile, int64, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, 0, errors.New("cannot read owned CPU profile directory")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(owned, opened) {
		return nil, 0, errors.New("isolated CPU profile directory identity changed")
	}
	// The directory contains at most two reporter files and the profile files.
	names, err := f.Readdirnames(maxCaseProfiles + 3)
	if len(names) > maxCaseProfiles+2 || (err != nil && !errors.Is(err, io.EOF)) {
		return nil, 0, errors.New("unreadable or excessive isolated CPU profile files")
	}
	slices.Sort(names)
	entries := make([]timeoutScratchFile, 0, len(names))
	var size int64
	for _, name := range names {
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || (name != "vitest.json" && name != "vitest-junit.xml" && !timeoutProfileName.MatchString(name)) {
			return nil, 0, errors.New("unexpected or nonregular file in isolated CPU profile directory")
		}
		// Saturate without overflowing for an unexpectedly large sparse file.
		size += min(info.Size(), maxRawCaseEvidence+1)
		entries = append(entries, timeoutScratchFile{name: name, info: info})
	}
	return entries, size, nil
}

func removeTimeoutScratch(parent, root *os.Root, name string, owned os.FileInfo, entries []timeoutScratchFile) error {
	current, err := parent.Lstat(name)
	if err != nil || !current.IsDir() || !os.SameFile(owned, current) {
		return errors.New("isolated CPU profile directory changed before cleanup")
	}
	for _, entry := range entries {
		current, err := root.Lstat(entry.name)
		if err != nil || !current.Mode().IsRegular() || !os.SameFile(entry.info, current) {
			return errors.New("isolated CPU profile or reporter file changed before cleanup")
		}
	}
	for _, entry := range entries {
		if err := root.Remove(entry.name); err != nil {
			return errors.New("cannot remove owned isolated CPU profile or reporter file")
		}
	}
	if err := parent.Remove(name); err != nil {
		return errors.New("cannot remove owned isolated CPU profile directory")
	}
	return nil
}

func readTimeoutProfiles(root *os.Root, entries []timeoutScratchFile, pid int, source, modules string, available *int) ([]timeoutCPUProfile, error) {
	var result []timeoutCPUProfile
	supervisor, localized := false, false
	identities := make(map[[2]string]bool)
	for _, entry := range entries {
		name := entry.name
		if name == "vitest.json" || name == "vitest-junit.xml" {
			continue
		}
		parts := timeoutProfileName.FindStringSubmatch(name)
		if parts == nil || len(result) == maxCaseProfiles {
			return result, errors.New("unexpected or excessive isolated CPU profiles")
		}
		identity := [2]string{parts[1], parts[2]}
		profilePID, pidErr := strconv.Atoi(parts[1])
		thread, threadErr := strconv.Atoi(parts[2])
		if pidErr != nil || profilePID <= 0 || strconv.Itoa(profilePID) != parts[1] || threadErr != nil || strconv.Itoa(thread) != parts[2] || identities[identity] {
			return result, errors.New("isolated CPU profile process identity is invalid or duplicated")
		}
		identities[identity] = true
		process := "worker"
		if profilePID == pid && thread == 0 {
			supervisor, process = true, "supervisor"
		}
		data, err := readDiagnosticEvidence(root, name, *available, "isolated CPU profile")
		if err != nil {
			return result, err
		}
		*available -= len(data)
		profile, err := summarizeTimeoutProfile(data, process, source, modules)
		if err != nil {
			return result, err
		}
		if process == "worker" {
			if !profile.workerRuntime {
				return result, errors.New("isolated CPU profile lacks a qualified Vitest worker runtime frame")
			}
			localized = localized || len(profile.Stacks) != 0
		}
		result = append(result, profile)
	}
	if !supervisor || !localized {
		return result, errors.New("isolated CPU profiles lack the supervisor or a worker with source-localized samples")
	}
	return result, nil
}

func relevantTimeoutStack(stack timeoutSampledStack) bool {
	return slices.ContainsFunc(stack.Frames, func(frame timeoutStackFrame) bool {
		return frame.Kind == "project" || (frame.Kind == "dependency" && !strings.HasPrefix(frame.File, "vitest/") && !strings.HasPrefix(frame.File, "@vitest/") && !strings.HasPrefix(frame.File, "vite/"))
	})
}

func summarizeTimeoutProfile(data []byte, process, source, modules string) (timeoutCPUProfile, error) {
	out := timeoutCPUProfile{Process: process, Stacks: []timeoutSampledStack{}}
	invalid := errors.New("isolated CPU profile is incomplete, malformed or exceeds sampling bounds")
	var raw v8Profile
	if json.Unmarshal(data, &raw) != nil || len(raw.Nodes) == 0 || len(raw.Nodes) > maxProfileNodes || len(raw.Samples) == 0 || len(raw.Samples) > maxProfileSamples || len(raw.TimeDeltas) != len(raw.Samples) || raw.StartTime <= 0 || raw.EndTime <= raw.StartTime {
		return out, invalid
	}
	nodes, parents := make(map[int]v8ProfileNode, len(raw.Nodes)), make(map[int]int, len(raw.Nodes))
	for _, n := range raw.Nodes {
		if n.ID <= 0 || nodes[n.ID].ID != 0 {
			return out, invalid
		}
		nodes[n.ID] = n
		for _, child := range n.Children {
			if child == n.ID || child <= 0 || parents[child] != 0 {
				return out, invalid
			}
			parents[child] = n.ID
		}
	}
	root := raw.Nodes[0].ID
	if parents[root] != 0 || len(parents) != len(nodes)-1 {
		return out, invalid
	}
	for id, node := range nodes {
		if (id != root && parents[id] == 0) || (id == root && node.CallFrame.FunctionName != "(root)") {
			return out, invalid
		}
	}
	type sampleStack struct {
		frames  [maxStackFrames]timeoutStackFrame
		project bool
	}
	resolved := make(map[int]sampleStack)
	stackCounts := make(map[[maxStackFrames]timeoutStackFrame]int)
	for i, id := range raw.Samples {
		if nodes[id].ID == 0 || raw.TimeDeltas[i] < 0 {
			return out, invalid
		}
		out.Samples++
		if nodes[id].CallFrame.FunctionName == "(idle)" && nodes[id].CallFrame.URL == "" {
			out.IdleSamples++
			continue
		}
		stack, ok := resolved[id]
		if !ok {
			frameCount := 0
			for cursor, depth := id, 0; cursor != root; cursor, depth = parents[cursor], depth+1 {
				if depth == maxProfileDepth || nodes[cursor].ID == 0 {
					return out, invalid
				}
				frame := localizeTimeoutFrame(nodes[cursor], source, modules)
				if frame.Kind == "" {
					continue
				}
				// The pinned Vitest chunks below are worker runtime code. A
				// frame must appear on a sampled ancestry, not merely in the
				// node table. Async project stacks can be separate samples.
				if frame.Kind == "dependency" && (strings.HasPrefix(frame.File, "vitest/dist/chunks/base.") || strings.HasPrefix(frame.File, "vitest/dist/chunks/test.")) {
					out.workerRuntime = true
				}
				stack.project = stack.project || frame.Kind == "project"
				if frameCount < len(stack.frames) {
					stack.frames[frameCount] = frame
					frameCount++
				}
			}
			resolved[id] = stack
		}
		if stack.project {
			out.ProjectSamples++
		}
		if stack.frames[0].Kind != "" {
			stackCounts[stack.frames]++
		}
	}
	for stack, count := range stackCounts {
		var frames []timeoutStackFrame
		for _, frame := range stack {
			if frame.Kind != "" {
				frames = append(frames, frame)
			}
		}
		stack := timeoutSampledStack{Samples: count, Frames: frames}
		if process == "supervisor" || relevantTimeoutStack(stack) {
			out.Stacks = append(out.Stacks, stack)
		}
	}
	slices.SortFunc(out.Stacks, func(a, b timeoutSampledStack) int {
		if a.Samples != b.Samples {
			return b.Samples - a.Samples
		}
		return slices.CompareFunc(a.Frames, b.Frames, func(x, y timeoutStackFrame) int {
			return cmp.Or(
				strings.Compare(x.Kind, y.Kind),
				strings.Compare(x.File, y.File),
				strings.Compare(x.Function, y.Function),
				cmp.Compare(x.Line, y.Line),
				cmp.Compare(x.Column, y.Column),
			)
		})
	})
	if len(out.Stacks) > maxProfileStacks {
		out.Stacks = out.Stacks[:maxProfileStacks]
	}
	return out, nil
}

func localizeTimeoutFrame(node v8ProfileNode, source, modules string) timeoutStackFrame {
	frame := node.CallFrame
	if frame.URL == "" || frame.LineNumber < 0 || frame.ColumnNumber < 0 || frame.LineNumber > 1_000_000 || frame.ColumnNumber > 1_000_000 {
		return timeoutStackFrame{}
	}
	path := frame.URL
	if strings.HasPrefix(path, "file:") {
		u, err := url.Parse(path)
		if err != nil || u.Host != "" || u.Scheme != "file" {
			return timeoutStackFrame{}
		}
		path = u.Path
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return timeoutStackFrame{}
	}
	kind := "dependency"
	file, ok := localProfilePath(path, modules)
	if !ok {
		file, ok = localProfilePath(path, filepath.Join(source, "frontend", "node_modules"))
	}
	if !ok {
		kind = "project"
		file, ok = localProfilePath(path, source)
	}
	if !ok || len(file) > 384 || strings.ContainsAny(file, "\x00\r\n") {
		return timeoutStackFrame{}
	}
	return timeoutStackFrame{
		Kind:     kind,
		File:     file,
		Function: clip(frame.FunctionName, 128),
		Line:     frame.LineNumber + 1,
		Column:   frame.ColumnNumber + 1,
	}
}

func localProfilePath(path, root string) (string, bool) {
	relative, ok := strings.CutPrefix(path, root+string(filepath.Separator))
	return filepath.ToSlash(relative), ok && relative != ""
}
