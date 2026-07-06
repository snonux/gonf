package pkg

import (
	"errors"
	"os"
)

type Ensure int

const (
	PkgPresent Ensure = iota
	PkgAbsent
	PkgLatest
)

type applyFunc func(name string, ensure Ensure) error

func Present(name string, ensure Ensure) error {
	bin, err := detect()
	if err != nil {
		return err
	}

	var applyFunc applyFunc

	switch bin {
	case "dnf":
		applyFunc = applyDNF
	}

	return applyFunc(name, ensure)
}

func detect() (string, error) {
	switch {
	case exists("/etc/fedora-release"):
		fallthrough
	case exists("/etc/rocky-release"):
		return "dnf", nil
	}
	return "", errors.New("unable to detect package manager!")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
