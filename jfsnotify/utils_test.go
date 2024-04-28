package jfsnotify

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFilepath(t *testing.T) {
	p := "/tmp/asds.txt"
	pt := strings.Split(p, string(filepath.Separator))

	for _, s := range pt {
		println(s)
	}

	println(filepath.Join(pt[:2]...))
}
