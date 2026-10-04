package backend

import (
	"regexp"
	"strconv"
	"strings"
)

var citation = regexp.MustCompile(`\[(\d+(?:\s*,\s*\d+)*)\]`)

// CitedNumbers returns the source numbers an answer cites as [n] or
// [n, m], in order of first appearance.
func CitedNumbers(answer string) []int {
	var nums []int
	seen := map[int]bool{}
	for _, m := range citation.FindAllStringSubmatch(answer, -1) {
		for _, num := range strings.Split(m[1], ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(num)); err == nil && !seen[n] {
				seen[n] = true
				nums = append(nums, n)
			}
		}
	}
	return nums
}
