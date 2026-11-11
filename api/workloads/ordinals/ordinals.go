/*
Copyright 2026 The RBG Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package ordinals implements the interval and membership rules of KEP-499.
// It never expands exclusion ranges, including ranges near the ordinal limit.
package ordinals

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const MaxOrdinal = math.MaxInt32

type interval struct{ first, last int }

// Ranges is a normalized, immutable collection of inclusive intervals.
type Ranges struct{ intervals []interval }

// Parse accepts comma-separated nonnegative ordinals and inclusive ranges.
// Empty input means no exclusions; empty entries within a list are errors.
func Parse(value string) (Ranges, error) {
	var result Ranges
	if strings.TrimSpace(value) == "" {
		return result, nil
	}
	for _, entry := range strings.Split(value, ",") {
		parts := strings.Split(strings.TrimSpace(entry), "-")
		if len(parts) > 2 {
			return Ranges{}, fmt.Errorf("invalid ordinal entry %q", entry)
		}
		bounds := make([]int, len(parts))
		for i, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return Ranges{}, fmt.Errorf("invalid nonnegative ordinal %q", part)
			}
			n, err := strconv.ParseUint(part, 10, 31)
			if err != nil {
				return Ranges{}, fmt.Errorf("ordinal %q exceeds %d", part, MaxOrdinal)
			}
			bounds[i] = int(n)
		}
		first, last := bounds[0], bounds[len(bounds)-1]
		if first > last {
			return Ranges{}, fmt.Errorf("reversed ordinal range %q", entry)
		}
		result.intervals = append(result.intervals, interval{first, last})
	}
	sort.Slice(result.intervals, func(i, j int) bool { return result.intervals[i].first < result.intervals[j].first })
	merged := result.intervals[:0]
	for _, r := range result.intervals {
		if len(merged) == 0 || int64(r.first) > int64(merged[len(merged)-1].last)+1 {
			merged = append(merged, r)
		} else {
			merged[len(merged)-1].last = max(merged[len(merged)-1].last, r.last)
		}
	}
	result.intervals = merged
	return result, nil
}

func (r Ranges) Contains(ordinal int) bool {
	i := sort.Search(len(r.intervals), func(i int) bool { return r.intervals[i].last >= ordinal })
	return i < len(r.intervals) && r.intervals[i].first <= ordinal
}

// Position returns an allowed ordinal's zero-based position without expanding
// ranges or allocating the prefix. Excluded or out-of-range ordinals return false.
func (r Ranges) Position(ordinal int) (int, bool) {
	if ordinal < 0 || ordinal > MaxOrdinal {
		return 0, false
	}
	position := int64(ordinal)
	for _, excluded := range r.intervals {
		if excluded.first > ordinal {
			break
		}
		if excluded.last >= ordinal {
			return 0, false
		}
		position -= int64(excluded.last) - int64(excluded.first) + 1
	}
	return int(position), true
}

// Next returns the first allowed ordinal at or above start, jumping ranges.
func (r Ranges) Next(start int) (int, error) {
	if start < 0 || start > MaxOrdinal {
		return 0, fmt.Errorf("no available ordinal at or above %d", start)
	}
	i := sort.Search(len(r.intervals), func(i int) bool { return r.intervals[i].last >= start })
	if i < len(r.intervals) && r.intervals[i].first <= start {
		if r.intervals[i].last == MaxOrdinal {
			return 0, fmt.Errorf("no available ordinal at or above %d", start)
		}
		return r.intervals[i].last + 1, nil
	}
	return start, nil
}

// Members reconstructs committed membership from a replica count and skips.
func (r Ranges) Members(replicas int) ([]int, error) {
	if replicas < 0 {
		return nil, fmt.Errorf("negative replica count %d", replicas)
	}
	result := make([]int, 0, replicas)
	next := 0
	for len(result) < replicas {
		ordinal, err := r.Next(next)
		if err != nil {
			return nil, err
		}
		result = append(result, ordinal)
		next = ordinal + 1
	}
	return result, nil
}

// Select keeps membership at fixed count, prefers excluded members on scale-in,
// and allocates the lowest unused nonexcluded ordinals on scale-out.
func Select(current []int, replicas int, excluded Ranges) ([]int, error) {
	if replicas < 0 {
		return nil, fmt.Errorf("negative replica count %d", replicas)
	}
	if replicas <= len(current) {
		removed := make(map[int]bool, len(current)-replicas)
		for _, priority := range []bool{true, false} {
			for i := len(current) - 1; i >= 0 && len(removed) < len(current)-replicas; i-- {
				if excluded.Contains(current[i]) == priority {
					removed[current[i]] = true
				}
			}
		}
		result := make([]int, 0, replicas)
		for _, ordinal := range current {
			if !removed[ordinal] {
				result = append(result, ordinal)
			}
		}
		return result, nil
	}
	result := slices.Clone(current)
	used := make(map[int]bool, replicas)
	for _, ordinal := range current {
		used[ordinal] = true
	}
	for next := 0; len(result) < replicas; {
		ordinal, err := excluded.Next(next)
		if err != nil {
			return nil, err
		}
		next = ordinal + 1
		if !used[ordinal] {
			result = append(result, ordinal)
			used[ordinal] = true
		}
	}
	slices.Sort(result)
	return result, nil
}

// Record encodes holes below the last member. Its input must be sorted unique
// nonnegative membership, as returned by Members or Select.
func Record(members []int) string {
	var entries []string
	next := 0
	for _, ordinal := range members {
		if ordinal > next {
			entry := strconv.Itoa(next)
			if ordinal-1 > next {
				entry += "-" + strconv.Itoa(ordinal-1)
			}
			entries = append(entries, entry)
		}
		next = ordinal + 1
	}
	return strings.Join(entries, ",")
}
