package runner

// AssignmentValidator preflights adapter input contracts without creating a
// workspace, native turn or external effect.
type AssignmentValidator interface {
	ValidateAssignment(Assignment) error
}
