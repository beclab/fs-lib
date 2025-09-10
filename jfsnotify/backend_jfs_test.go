package jfsnotify

import (
	"os"
	"testing"
)

func TestPackageMsg(t *testing.T) {
	n := make([]byte, 255)
	copy(n, []byte("search3monitor-q7qmz/search3monitor"))
	data := PackageMsg(MSG_CLEAR, n)

	// Write data to binary file
	err := os.WriteFile("/opt/golang.data", data, 0644)
	if err != nil {
		t.Fatalf("Failed to write data to file: %v", err)
	}
}
