package filespane

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func AvailableName(path string, sourceDir bool) (string, error) {
	name, ext := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), filepath.Ext(path)
	if sourceDir {
		name, ext = filepath.Base(path), ""
	}
	for n := 1; n < 10000; n++ {
		suffix := " copy"
		if n > 1 {
			suffix = fmt.Sprintf(" copy %d", n)
		}
		candidate := filepath.Join(filepath.Dir(path), name+suffix+ext)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("too many items with this name")
}
