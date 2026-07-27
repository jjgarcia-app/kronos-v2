package obsidian

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ExpandPath resuelve un "~" inicial al home del usuario actual. Deja el
// path sin tocar si no empieza con "~" o si no se puede resolver el home.
func ExpandPath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slug converts a string to a filename-safe lowercase slug.
func slug(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer(
		"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u",
		"ä", "a", "ë", "e", "ï", "i", "ö", "o", "ü", "u",
		"ñ", "n", "ç", "c",
	).Replace(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	return s
}

// obsName returns the base filename (without .md) for an observation.
// Format: "0042-elegimos-go-para-kronos-v2"
func obsName(id int64, title string) string {
	return fmt.Sprintf("%04d-%s", id, slug(title))
}

// wikilink returns "[[0042-elegimos-go-para-kronos-v2]]".
func wikilink(id int64, title string) string {
	return "[[" + obsName(id, title) + "]]"
}
