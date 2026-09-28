package config

// ModeWarning never warns on Windows: the file has no Unix mode bits to check.
func ModeWarning() string {
	return ""
}
