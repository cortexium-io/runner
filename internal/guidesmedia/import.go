package guidesmedia

import (
	"context"
	"errors"
	"os"
)

// Import writes only raw approved files and a bounded index to a fresh owned
// scratch directory. Runner, not this helper, registers the declared artifacts.
func Import(ctx context.Context, output, runID, attemptID, helperSHA string) (Index, error) {
	x, err := NewIndex(runID, attemptID, helperSHA)
	if err != nil {
		return x, err
	}
	info, err := os.Lstat(output)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return x, errors.New("guide import needs a fresh regular owned output directory")
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 {
		return x, errors.New("guide import output is not empty; preserve partial work")
	}
	r, err := os.OpenRoot(output)
	if err != nil {
		return x, err
	}
	defer r.Close()
	opened, err := r.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return x, errors.New("guide output changed while opening")
	}
	m, err := Approved()
	if err != nil {
		return x, err
	}
	for _, g := range m.Guides {
		for _, f := range g.Files {
			data, err := ReadApproved(ctx, f)
			if err != nil {
				return x, err
			}
			if err = writeNewRoot(r, OutputName(g.Game, f.Role), data); err != nil {
				return x, err
			}
		}
	}
	data, err := x.Encode()
	if err != nil {
		return x, err
	}
	if err = ctx.Err(); err != nil {
		return x, err
	}
	after, err := os.Lstat(output)
	if err != nil || !os.SameFile(info, after) || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 {
		return x, errors.New("guide output directory moved; preserve partial work")
	}
	return x, writeNewRoot(r, "media-index.json", data)
}

func OutputContract() map[string]string {
	outputs := map[string]string{"check": "text", "media_index": "artifact"}
	m, err := Approved()
	if err != nil {
		return nil
	}
	for _, g := range m.Guides {
		for _, f := range g.Files {
			outputs[g.Game+"_"+f.Role] = "artifact"
		}
	}
	return outputs
}
