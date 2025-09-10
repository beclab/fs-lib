package jfsnotify

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestPackageMsg(t *testing.T) {
	n := make([]byte, 255)
	copy(n, []byte("search3monitor-q7qmz/search3monitor"))
	data := PackageMsg(MSG_CLEAR, n)

	// Write data to binary file
	err := os.WriteFile("/data/golang.data", data, 0644)
	if err != nil {
		t.Fatalf("Failed to write data to file: %v", err)
	}
}

// TestCompareBinaryFiles 测试比较两个二进制文件的内容是否相同
func TestCompareBinaryFiles(t *testing.T) {
	// go test -v -run TestCompareBinaryFiles
	// 测试用例：比较两个相同的文件
	file1 := "/data/rust.data"
	file2 := "/data/golang.data" // 使用同一个文件进行测试

	equal, err := CompareBinaryFiles(file1, file2)
	if err != nil {
		t.Fatalf("Failed to compare binary files: %v", err)
	}
	if !equal {
		t.Fatalf("Binary files are not equal")
	}

	t.Log("Binary files are equal")
	println("Binary files are equal")
}

// CompareBinaryFiles 比较两个二进制文件的内容是否相同
// 使用SHA256哈希来比较文件内容，适用于大文件
func CompareBinaryFiles(file1, file2 string) (bool, error) {
	// 检查文件是否存在
	if _, err := os.Stat(file1); os.IsNotExist(err) {
		return false, fmt.Errorf("file %s does not exist", file1)
	}
	if _, err := os.Stat(file2); os.IsNotExist(err) {
		return false, fmt.Errorf("file %s does not exist", file2)
	}

	// 计算第一个文件的SHA256哈希
	hash1, err := calculateFileHash(file1)
	if err != nil {
		return false, fmt.Errorf("failed to calculate hash for %s: %v", file1, err)
	}

	// 计算第二个文件的SHA256哈希
	hash2, err := calculateFileHash(file2)
	if err != nil {
		return false, fmt.Errorf("failed to calculate hash for %s: %v", file2, err)
	}

	// 比较哈希值
	return hash1 == hash2, nil
}

// calculateFileHash 计算文件的SHA256哈希值
func calculateFileHash(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
