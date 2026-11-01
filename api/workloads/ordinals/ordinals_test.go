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

package ordinals

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	for _, value := range []string{"-1", "1,,3", "1,", ",1", "3-1", "1-", "+2", "1-2-3", "a", "2147483648", "0-99999999999999999999"} {
		if _, err := Parse(value); err == nil {
			t.Errorf("Parse(%q) accepted invalid input", value)
		}
	}
	r, err := Parse("8-10,3,1,8,9-12,2")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.intervals) != 2 {
		t.Fatalf("not merged: %+v", r)
	}
	for n := 0; n < 15; n++ {
		if r.Contains(n) != (n >= 1 && n <= 3 || n >= 8 && n <= 12) {
			t.Fatalf("Contains(%d)", n)
		}
	}
	giant, err := Parse("0-2147483645")
	if err != nil {
		t.Fatal(err)
	}
	got, err := giant.Members(2)
	if err != nil || !reflect.DeepEqual(got, []int{2147483646, 2147483647}) {
		t.Fatalf("giant range: %v %v", got, err)
	}
	if _, err := giant.Members(3); err == nil {
		t.Fatal("expected ordinal exhaustion")
	}
}

func TestMembership(t *testing.T) {
	cases := []struct {
		name   string
		old    []int
		n      int
		e      string
		want   []int
		record string
	}{
		{"initial sparse", nil, 3, "1,3", []int{0, 2, 4}, "1,3"},
		{"simultaneous count and exclusions", []int{0, 1, 2, 3, 4}, 3, "1,3", []int{0, 2, 4}, "1,3"},
		{"only required exclusions", []int{0, 1, 2, 3, 4}, 4, "1,3", []int{0, 1, 2, 4}, "3"},
		{"fallback highest", []int{0, 1, 2, 3, 4}, 2, "1,9", []int{0, 2}, "1"},
		{"fixed count changed exclusions", []int{0, 2, 3}, 3, "0-10", []int{0, 2, 3}, "1"},
		{"fixed count cleared exclusions", []int{0, 2, 3}, 3, "", []int{0, 2, 3}, "1"},
		{"reuse gap on later scale out", []int{0, 2, 3}, 4, "", []int{0, 1, 2, 3}, ""},
		{"exclude gap on scale out", []int{0, 2}, 3, "1", []int{0, 2, 3}, "1"},
		{"zero", []int{0, 2, 4}, 0, "1,3", []int{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Parse(tc.e)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Select(tc.old, tc.n, e)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			record := Record(got)
			if record != tc.record {
				t.Fatalf("record %q want %q", record, tc.record)
			}
			skips, err := Parse(record)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := skips.Members(tc.n)
			if err != nil || !reflect.DeepEqual(got, recovered) {
				t.Fatalf("recovery %v %v", recovered, err)
			}
		})
	}
}

// Check every small membership/exclusion combination, including references
// outside membership and all possible scaling targets.
func TestMembershipProperties(t *testing.T) {
	for mask := 0; mask < 256; mask++ {
		var old []int
		for i := 0; i < 8; i++ {
			if mask&(1<<i) != 0 {
				old = append(old, i)
			}
		}
		for emask := 0; emask < 256; emask++ {
			var excluded []int
			for i := 0; i < 8; i++ {
				if emask&(1<<i) != 0 {
					excluded = append(excluded, i)
				}
			}
			// Use the normalized public parser, including adjacent entries.
			var value string
			for _, i := range excluded {
				if value != "" {
					value += ","
				}
				value += string(rune('0' + i))
			}
			e, err := Parse(value)
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n <= 9; n++ {
				got, err := Select(old, n, e)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != n {
					t.Fatal("wrong count")
				}
				for j, i := range got {
					if j > 0 && got[j-1] >= i {
						t.Fatal("not sorted unique")
					}
				}
				skips, _ := Parse(Record(got))
				recovered, err := skips.Members(n)
				if err != nil || !reflect.DeepEqual(got, recovered) {
					t.Fatalf("roundtrip %v %v", got, recovered)
				}
				if n >= len(old) {
					for _, i := range old {
						found := false
						for _, j := range got {
							found = found || i == j
						}
						if !found {
							t.Fatal("lost existing member")
						}
					}
				}
			}
		}
	}
}
