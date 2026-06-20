package textutil

import "strings"

func Truncate(value string, limit int, suffix string) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = strings.TrimSpace(PrefixBytes(value, limit))
	if value == "" {
		return strings.TrimSpace(suffix)
	}
	return value + suffix
}

func PrefixBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := 0
	for index := range value {
		if index > limit {
			break
		}
		cut = index
	}
	return value[:cut]
}

func SliceBytes(value string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(value) {
		end = len(value)
	}
	if start >= end {
		return ""
	}
	start = nextRuneBoundary(value, start)
	end = previousRuneBoundary(value, end)
	if start >= end {
		return ""
	}
	return value[start:end]
}

func nextRuneBoundary(value string, offset int) int {
	if offset <= 0 {
		return 0
	}
	if offset >= len(value) {
		return len(value)
	}
	for index := range value {
		if index >= offset {
			return index
		}
	}
	return len(value)
}

func previousRuneBoundary(value string, offset int) int {
	if offset <= 0 {
		return 0
	}
	if offset >= len(value) {
		return len(value)
	}
	previous := 0
	for index := range value {
		if index == offset {
			return offset
		}
		if index > offset {
			return previous
		}
		previous = index
	}
	return len(value)
}
