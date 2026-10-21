package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/securefs"
)

func readInput(path string, v any) error {
	b, _, state, err := securefs.ReadFile(path, 1<<20)
	if err != nil {
		return err
	}
	if err = securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return err
	}
	return config.Decode(b, v)
}
func (c CLI) quality(ctx context.Context, e *engine.Engine, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: quality prepare|preview|check|apply|show")
	}
	command := args[0]
	f := c.flags("quality " + command)
	var file, report, exceptions, preview, revision *string
	var apply *bool
	var moduleCache *string
	switch command {
	case "prepare":
		file = f.String("file", "", "quality specification")
		preview = f.String("preview", "", "displayed environment preview")
		apply = f.Bool("apply", false, "explicitly prepare the declared offline environment")
		moduleCache = f.String("module-cache", "", "explicit verified Go module cache; imports only pinned private archives")
	case "preview":
		file = f.String("file", "", "quality specification")
		report = f.String("report", "", "retained baseline report")
		exceptions = f.String("exceptions", "", "explicit scoped exception requests")
	case "check":
		file = f.String("file", "", "quality specification")
		preview = f.String("preview", "", "displayed baseline preview")
	case "apply":
		report = f.String("report", "", "retained baseline report")
		exceptions = f.String("exceptions", "", "explicit scoped exception requests")
		preview = f.String("preview", "", "displayed agreement preview")
	case "show":
		revision = f.String("revision", "", "retained revision; default is current agreement")
	default:
		return errors.New("usage: quality prepare|preview|check|apply|show")
	}
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected quality argument")
	}
	if command == "prepare" {
		if *file == "" {
			return errors.New("quality prepare requires --file")
		}
		var spec config.QualitySpec
		if err := readInput(*file, &spec); err != nil {
			return err
		}
		p, result, err := e.PrepareQuality(ctx, spec, *apply, *preview, *moduleCache)
		if err != nil {
			return err
		}
		return c.json(struct {
			Preview  any                         `json:"preparation"`
			Prepared *config.PreparedEnvironment `json:"prepared,omitempty"`
		}{p, result})
	}
	if command == "show" {
		id := *revision
		if id == "" {
			id = e.Settings.Quality.Revision()
		}
		if id == "" {
			return errors.New("quality is not configured; use preview/check/apply")
		}
		q, err := e.Store.Quality(ctx, id)
		if err != nil {
			return err
		}
		return c.json(struct {
			Revision string         `json:"revision"`
			Quality  config.Quality `json:"quality"`
		}{id, q})
	}
	var requests []config.ExceptionRequest
	if exceptions != nil && *exceptions != "" {
		if err := readInput(*exceptions, &requests); err != nil {
			return err
		}
	}
	if file != nil && *file != "" {
		if report != nil && *report != "" || len(requests) > 0 {
			return errors.New("use either a specification file or a report with exceptions")
		}
		var spec config.QualitySpec
		if err := readInput(*file, &spec); err != nil {
			return err
		}
		if command == "preview" {
			p, err := e.PreviewQuality(ctx, spec)
			if err != nil {
				return err
			}
			return c.json(p)
		}
		r, err := e.RunBaseline(ctx, spec, *preview)
		if err != nil {
			return err
		}
		return c.json(r)
	}
	if report == nil || *report == "" {
		return errors.New("supply --file for a baseline preview/check or --report for agreement preview/apply")
	}
	if command == "preview" {
		p, err := e.PreviewAgreement(ctx, *report, requests)
		if err != nil {
			return err
		}
		return c.json(p)
	}
	if err := e.ApplyAgreement(ctx, *report, requests, *preview); err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Quality agreement applied for new work. Existing tasks keep their pinned revision; use task show and adopt-quality to select adoption.")
	return nil
}
