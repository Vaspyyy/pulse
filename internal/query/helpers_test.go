package query

import (
	"os"
	"path/filepath"
)

func mkfile(name, content string) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return os.WriteFile(name, []byte(content), 0o644)
}
