package numfmt

// ValidationWeak is a throwaway function that checks the sharded mutate-diff job.
// Its test runs it but asserts nothing, so its mutants must escape.
func ValidationWeak(a, b int) int {
	return a + b
}
