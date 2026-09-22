//go:build !darwin && !linux

package subprocess

import "errors"

func inspectProcess(int) (ownedProcess, error) {
	return ownedProcess{}, errors.New("harness ownership requires macOS or Linux")
}
func ownedProcessesForVariable(string, string, string) ([]ownedProcess, error) {
	return nil, errors.New("harness ownership requires macOS or Linux")
}
func signalOwnedProcess(ownedProcess, bool) error {
	return errors.New("harness ownership requires macOS or Linux")
}
