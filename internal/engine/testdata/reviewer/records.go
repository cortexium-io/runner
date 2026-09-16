package records

import (
	"errors"
	"strings"
)

type Record struct {
	Tenant string
	Title  string
}

func Update(records map[string]Record, tenant, id, title string) error {
	record, ok := records[id]
	if !ok || record.Tenant != tenant || strings.TrimSpace(title) == "" {
		return errors.New("invalid update")
	}
	record.Title = title
	records[id] = record
	return nil
}
