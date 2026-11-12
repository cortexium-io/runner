package guidesmedia

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

const SourceFormat = "runner-guides-source-v1"

type Patch struct {
	Format string  `json:"format"`
	Base   string  `json:"base"`
	Tree   string  `json:"tree"`
	Media  Binding `json:"media"`
	Code   string  `json:"code_patch"`
}

var sourceHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

func EncodePatch(base, tree string, binding Binding, code []byte, limit int) ([]byte, error) {
	if !sourceHash.MatchString(base) || !sourceHash.MatchString(tree) || binding.ValidateShape() != nil || !utf8.Valid(code) || limit < 1 || limit > 16<<20 || len(code) > limit {
		return nil, errors.New("guide source representation authority or code bound differs")
	}
	data, err := json.Marshal(Patch{Format: SourceFormat, Base: base, Tree: tree, Media: binding, Code: string(code)})
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("guide code patch and control exceed unchanged artifact bound")
	}
	return data, nil
}

func DecodePatch(data []byte, base string, binding Binding, limit int) (Patch, error) {
	var p Patch
	if limit < 1 || limit > 16<<20 || len(data) > limit || binding.ValidateShape() != nil {
		return p, errors.New("guide source representation exceeds authority or bound")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, errors.New("guide source representation contains trailing data")
	}
	if p.Format != SourceFormat || p.Base != base || !sourceHash.MatchString(p.Base) || !sourceHash.MatchString(p.Tree) || p.Media != binding {
		return p, errors.New("guide source original base or whole-set authority differs")
	}
	return p, nil
}
