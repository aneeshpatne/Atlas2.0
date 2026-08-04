package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultMiscBackdrop = "misc"

// loadBackdrop finds a genre image under dir (case-insensitive stem match).
// Falls back to misc.* when no file matches the genre.
// Supported extensions: .png, .jpg, .jpeg, .webp.
func loadBackdrop(dir, genre string) (data []byte, path string, err error) {
	if strings.TrimSpace(dir) == "" {
		return nil, "", fmt.Errorf("backdrop dir is empty")
	}
	index, err := indexBackdrops(dir)
	if err != nil {
		return nil, "", err
	}
	key := strings.ToLower(strings.TrimSpace(genre))
	path, ok := index[key]
	if !ok {
		path, ok = index[defaultMiscBackdrop]
		if !ok {
			return nil, "", fmt.Errorf("no backdrop for genre %q and no %s fallback in %s", genre, defaultMiscBackdrop, dir)
		}
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("read backdrop %s: %w", path, err)
	}
	return data, path, nil
}

func indexBackdrops(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read backdrop dir %s: %w", dir, err)
	}
	index := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !isBackdropExt(ext) {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		if stem == "" {
			continue
		}
		// First file wins for a given stem (e.g. india.png before india.jpg).
		if _, exists := index[stem]; exists {
			continue
		}
		index[stem] = filepath.Join(dir, name)
	}
	return index, nil
}

func isBackdropExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	default:
		return false
	}
}
