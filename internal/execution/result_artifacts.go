package execution

import (
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/securefs"
)

const (
	structuredResultName = "result.json"
	structuredSchemaName = "schema.json"
	piExtensionName      = "result-extension.ts"
)

type structuredResultArtifacts struct {
	set *securefs.ArtifactSet
}

func newStructuredResultArtifacts(prefix string, schema []byte) (*structuredResultArtifacts, error) {
	set, err := securefs.NewArtifactSet(prefix, []securefs.ArtifactFile{
		{Name: structuredResultName, Mutable: true},
		{Name: structuredSchemaName, Content: schema},
	})
	if err != nil {
		return nil, err
	}
	return &structuredResultArtifacts{set: set}, nil
}

func (a *structuredResultArtifacts) outputPath() string { return a.set.Path(structuredResultName) }
func (a *structuredResultArtifacts) schemaPath() string { return a.set.Path(structuredSchemaName) }

func (a *structuredResultArtifacts) readResult() (string, error) {
	if err := a.set.VerifyImmutable(structuredSchemaName); err != nil {
		return "", fmt.Errorf("verify structured-result schema: %w", err)
	}
	data, err := a.set.ReadMutable(structuredResultName, maxHarnessResultBytes)
	if err != nil {
		return "", fmt.Errorf("read structured result: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (a *structuredResultArtifacts) close() { _ = a.set.Close() }
