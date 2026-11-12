// Package proof supplies deliberately non-executable codec/control fixture bytes.
// These bytes never establish Linux execution, browser or product acceptance.
package proof

import (
	"encoding/binary"
	"github.com/cortexium-io/runner/internal/saas"
)

func Build(e *saas.Evidence, a saas.Authority) ([]byte, error) {
	b := make([]byte, 256)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint64(b[24:], 0x400080)
	binary.LittleEndian.PutUint64(b[32:], 64)
	binary.LittleEndian.PutUint16(b[52:], 64)
	binary.LittleEndian.PutUint16(b[54:], 56)
	binary.LittleEndian.PutUint16(b[56:], 1)
	binary.LittleEndian.PutUint32(b[64:], 1)
	binary.LittleEndian.PutUint32(b[68:], 5)
	binary.LittleEndian.PutUint64(b[80:], 0x400000)
	binary.LittleEndian.PutUint64(b[96:], uint64(len(b)))
	binary.LittleEndian.PutUint64(b[104:], uint64(len(b)))
	asset := []byte("simulated embedded frontend")
	copy(b[128:], asset)
	assets, front, err := saas.EmbeddedFrontend(b, map[string][]byte{"index.html": asset})
	if err != nil {
		return nil, err
	}
	e.ServerSHA256, e.FrontendSHA256 = saas.Digest(b), front
	e.Build = &saas.CheckedBuild{
		Version:       1,
		Target:        saas.LinuxTarget,
		BinaryBytes:   len(b),
		NormalBytes:   len(b),
		NormalSHA256:  saas.Digest(b),
		GoSHA256:      a.GoSHA256,
		GennySHA256:   a.GennySHA256,
		NodeSHA256:    a.NodeSHA256,
		GoCheckSHA256: a.GoCheckSHA256,
		Runtime:       a.Runtime,
		Frontend:      assets,
	}
	return b, nil
}
