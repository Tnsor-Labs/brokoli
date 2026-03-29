package processing

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type Normalizer struct {
	MaxLength        int
	PreserveCase     bool
	ReplaceSpaces    bool
	SpaceReplacement string
}

func NewNormalizer() *Normalizer {
	return &Normalizer{
		MaxLength:        64,
		PreserveCase:     false,
		ReplaceSpaces:    true,
		SpaceReplacement: "_",
	}
}

func (n *Normalizer) NormalizeColumnName(name string) string {
	name = strings.TrimSpace(name)

	if n.ReplaceSpaces {
		name = strings.ReplaceAll(name, " ", n.SpaceReplacement)
	}

	var result strings.Builder

	re := regexp.MustCompile(`[^a-zA-Z0-9_ ]`)
	if n.ReplaceSpaces {
		name = re.ReplaceAllString(name, n.SpaceReplacement)
	} else {
		name = re.ReplaceAllString(name, "_")
	}

	if len(name) > 0 && !unicode.IsLetter(rune(name[0])) && name[0] != '_' {
		result.WriteRune('_')
	}

	result.WriteString(name)

	if !n.PreserveCase {
		name = strings.ToUpper(result.String())
	} else {
		name = result.String()
	}

	if n.MaxLength > 0 && len(name) > n.MaxLength {
		name = name[:n.MaxLength]
	}

	if name == "" {
		name = "COLUMN"
	}

	return name
}

func (n *Normalizer) NormalizeColumnNames(names []string) []string {
	normalized := make([]string, len(names))

	usedNames := make(map[string]int)

	for i, name := range names {
		normalizedName := n.NormalizeColumnName(name)

		if count, exists := usedNames[normalizedName]; exists {

			normalizedName = normalizedName + "_" + fmt.Sprintf("%d", count-1)
			usedNames[normalizedName] = 1
			usedNames[normalizedName[:len(normalizedName)-2]]++
		} else {
			usedNames[normalizedName] = 1
		}

		normalized[i] = normalizedName
	}

	return normalized
}
