package git

import (
	"regexp"
	"strings"
)

var filenameRegex = regexp.MustCompile(`\b\d+_[\w-]+\.sql\b`)

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

// FilesOnlyInBase returns filenames that appear only in the base branch and not in the incoming branch.
func FilesOnlyInBase(base, incoming string) []string {
	baseFiles := getFileNames(base)
	incomingFilesSet := make(map[string]struct{})
	for _, file := range getFileNames(incoming) {
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
