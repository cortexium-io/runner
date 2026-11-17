package execution

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/cortexium-io/runner/internal/runner"
)

func recoveryContextData(disk *runner.Disk, a runner.Assignment) ([]byte, error) {
	proof := a.RecoveryContext
	if proof == nil {
		return nil, nil
	}
	data, err := disk.Read("artifacts/"+proof.ID, runner.MaxCorrectionContext)
	if err != nil || proof.Bytes < 1 || proof.Bytes != int64(len(data)) || proof.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
		return nil, errors.New("supplemental recovery context differs from its retained identity")
	}
	return data, nil
}
