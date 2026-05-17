package obsidian

import (
	"fmt"
	"regexp"
	"strings"
)

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
