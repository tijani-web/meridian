package topology_test

import (
	"testing"

	"github.com/tijani-web/meridian/internal/scheduler/topology"
)

func TestDistance(t *testing.T) {
	cases := []struct {
		name     string
		a, b     map[string]string
		expected int
	}{
		{
			name: "same nvlink domain",
			a:    map[string]string{topology.LabelNVLinkDomain: "nvlink-0", topology.LabelRack: "rack-0", topology.LabelZone: "us-east-1a"},
			b:    map[string]string{topology.LabelNVLinkDomain: "nvlink-0", topology.LabelRack: "rack-0", topology.LabelZone: "us-east-1a"},
			expected: 0,
		},
		{
			name: "same rack different nvlink",
			a:    map[string]string{topology.LabelNVLinkDomain: "nvlink-0", topology.LabelRack: "rack-0", topology.LabelZone: "us-east-1a"},
			b:    map[string]string{topology.LabelNVLinkDomain: "nvlink-1", topology.LabelRack: "rack-0", topology.LabelZone: "us-east-1a"},
			expected: 1,
		},
		{
			name: "same zone different rack",
			a:    map[string]string{topology.LabelNVLinkDomain: "nvlink-0", topology.LabelRack: "rack-0", topology.LabelZone: "us-east-1a"},
			b:    map[string]string{topology.LabelNVLinkDomain: "nvlink-2", topology.LabelRack: "rack-1", topology.LabelZone: "us-east-1a"},
			expected: 2,
		},
		{
			name: "cross-zone",
			a:    map[string]string{topology.LabelNVLinkDomain: "nvlink-0", topology.LabelRack: "rack-0", topology.LabelZone: "us-east-1a"},
			b:    map[string]string{topology.LabelNVLinkDomain: "nvlink-3", topology.LabelRack: "rack-2", topology.LabelZone: "us-east-1b"},
			expected: 3,
		},
		{
			name:     "no labels at all",
			a:        map[string]string{},
			b:        map[string]string{},
			expected: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := topology.Distance(tc.a, tc.b)
			if got != tc.expected {
				t.Errorf("Distance() = %d, want %d", got, tc.expected)
			}
		})
	}
}

func TestTopoScore(t *testing.T) {
	cases := []struct{ dist int; want int64 }{
		{0, 100}, {1, 70}, {2, 30}, {3, 0}, {99, 0},
	}
	for _, tc := range cases {
		got := topology.TopoScore(tc.dist)
		if got != tc.want {
			t.Errorf("TopoScore(%d) = %d, want %d", tc.dist, got, tc.want)
		}
	}
}
