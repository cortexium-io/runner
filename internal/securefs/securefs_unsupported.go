//go:build !darwin && !linux

package securefs

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrChanged = errors.New("secure filesystem object changed")
var errUnsupported = errors.New("secure filesystem operations require macOS or Linux")

type Directory struct{}
type FileState struct{ Exists bool }

func ValidateOwnedRegularFile(FileState, uint32) error {
	return errors.New("owned regular-file provenance validation is unsupported on this platform")
}

type PinnedFile struct{}
type ArtifactFile struct {
	Name    string
	Content []byte
	Mutable bool
}
type ArtifactSet struct{}
type PathDiscovery struct{}

func EnsurePrivateDir(string) error            { return errUnsupported }
func ValidatePrivateDir(string) error          { return errUnsupported }
func OpenDir(string) (*Directory, error)       { return nil, errUnsupported }
func AbsolutePath(path string) (string, error) { return filepath.Abs(path) }
func ReadFile(string, int64) ([]byte, os.FileMode, FileState, error) {
	return nil, 0, FileState{}, errUnsupported
}
func WriteFileExclusive(string, []byte, os.FileMode) error { return errUnsupported }
func LinkFile(string, string) error                        { return errUnsupported }
func RemoveFile(string) error                              { return errUnsupported }
func (d *Directory) Close() error                          { return nil }
func (d *Directory) Verify() error                         { return errUnsupported }
func (d *Directory) VerifyIdentity() error                 { return errUnsupported }
func (d *Directory) VerifyEmpty() error                    { return errUnsupported }
func (d *Directory) VerifyFile(string, FileState) error    { return errUnsupported }
func (d *Directory) HashPath(string) ([]byte, error)       { return nil, errUnsupported }
func (d *Directory) HashPathWithBudget(string, *SnapshotBudget) ([]byte, error) {
	return nil, errUnsupported
}
func (d *Directory) DiscoverFiles([]string, []string) (*PathDiscovery, error) {
	return nil, errUnsupported
}
func (d *Directory) DiscoverFilesWithBudget([]string, []string, *SnapshotBudget) (*PathDiscovery, error) {
	return nil, errUnsupported
}
func (d *Directory) OpenDir(string) (*Directory, error)   { return nil, errUnsupported }
func (d *Directory) OpenFile(string) (*PinnedFile, error) { return nil, errUnsupported }
func (d *Directory) ReadFile(string, int64) ([]byte, os.FileMode, FileState, error) {
	return nil, 0, FileState{}, errUnsupported
}
func (d *Directory) ReplaceFile(string, []byte, os.FileMode, FileState) error { return errUnsupported }
func (f *PinnedFile) Close() error                                            { return nil }
func (f *PinnedFile) ReadAll(int64) ([]byte, error)                           { return nil, errUnsupported }
func (f *PinnedFile) ReadAllMutable(int64) ([]byte, error)                    { return nil, errUnsupported }
func (f *PinnedFile) Verify() error                                           { return errUnsupported }
func NewArtifactSet(string, []ArtifactFile) (*ArtifactSet, error)             { return nil, errUnsupported }
func (a *ArtifactSet) Path(string) string                                     { return "" }
func (a *ArtifactSet) ReadMutable(string, int64) ([]byte, error)              { return nil, errUnsupported }
func (a *ArtifactSet) VerifyImmutable(string) error                           { return errUnsupported }
func (a *ArtifactSet) Close() error                                           { return nil }
func (d *PathDiscovery) Paths() []string                                      { return nil }
func (d *PathDiscovery) Digests() map[string][]byte                           { return nil }
func (d *PathDiscovery) Verify() error                                        { return errUnsupported }
func (d *PathDiscovery) Close() error                                         { return nil }
