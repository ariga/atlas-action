package git

import (
	"regexp"
	"strings"
)

type Conflict struct {
	Base     string
	Incoming string
}

var conflictRegex = regexp.MustCompile(`(?ms)^<<<<<<< .+?\n(.*?)\n=======\n(.*?)\n>>>>>>> .+?$`)

// ParseConflicts parses the git conflicts from the input.
func ParseConflicts(input string) []Conflict {
	matches := conflictRegex.FindAllStringSubmatch(input, -1)
	conflicts := make([]Conflict, 0, len(matches))
	for _, m := range matches {
		baseContent := strings.TrimSpace(m[1])
		incomingContent := strings.TrimSpace(m[2])
		conflicts = append(conflicts, Conflict{
			Base:     baseContent,
			Incoming: incomingContent,
		})
	}
	return conflicts
}

var filenameRegex = regexp.MustCompile(`^\S+\.sql`)

// getFileNames extracts the filenames from the given branch content.
func getFileNames(content string) []string {
	var filenames []string
	for _, line := range strings.Split(content, "\n") {
		if matches := filenameRegex.FindString(line); matches != "" {
			filenames = append(filenames, matches)
		}
	}
	return filenames
}

// FilesOnlyInBase returns filenames that appear only in the base branch.
func (c *Conflict) FilesOnlyInBase() []string {
	baseFiles := getFileNames(c.Base)
	incomingFilesSet := make(map[string]struct{})
	for _, file := range getFileNames(c.Incoming) {
		incomingFilesSet[file] = struct{}{}
	}
	var onlyInBase []string
	for _, file := range baseFiles {
		if _, exists := incomingFilesSet[file]; !exists {
			onlyInBase = append(onlyInBase, file)
		}
	}
	return onlyInBase
}
