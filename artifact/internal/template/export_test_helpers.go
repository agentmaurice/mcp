package template

// SchemaCostForTest and DataWorkForTest expose the budget measures to tests
// of other packages; they are not part of the renderer API.
func SchemaCostForTest(schema any) (int, error) {
	root, _ := schema.(map[string]any)
	return schemaCost(root)
}

func DataWorkForTest(data any) int { return dataWork(data, 1<<40) }
