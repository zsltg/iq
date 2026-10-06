// Package testimage reads the container image of a test service from compose.yaml.
// The file is the one source of the test images. Each image is a version tag plus a
// digest. The Go tests that start a container call Ref, so a version change happens in
// one place. Only tests use this package.
package testimage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// composeFile is the name of the compose file in the module root.
const composeFile = "compose.yaml"

// Ref returns the image of the named service in the compose.yaml of the module. It
// looks for the module root from the working directory upward. A test runs in its
// package directory, so the root is a parent of the working directory.
func Ref(service string) (string, error) {
	if service == "" {
		return "", errors.New("testimage: empty service name")
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("testimage: get working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return ref(filepath.Join(dir, composeFile), service)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("testimage: no go.mod above the working directory")
		}
		dir = parent
	}
}

// compose holds the only part of a compose file that this package reads.
type compose struct {
	Services map[string]struct {
		Image string `yaml:"image"`
	} `yaml:"services"`
}

// ref reads the compose file at path and returns the image of the named service.
func ref(path, service string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("testimage: read %s: %w", path, err)
	}
	var c compose
	if err := yaml.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("testimage: parse %s: %w", path, err)
	}
	svc, ok := c.Services[service]
	if !ok {
		return "", fmt.Errorf("testimage: service %q is not in %s", service, path)
	}
	if svc.Image == "" {
		return "", fmt.Errorf("testimage: service %q in %s has no image", service, path)
	}
	return svc.Image, nil
}
