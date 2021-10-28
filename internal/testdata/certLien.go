// +build !CUSTOM

package testdata

import (
	"path"
	"runtime"
)

func init() {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		panic("Failed to get current frame")
	}

	CertPath = path.Dir(filename)
}

