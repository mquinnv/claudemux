package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func selectedGauges(gs gaugeSet, kept []int) string {
	parts := make([]string, 0, len(kept))
	for _, i := range kept {
		parts = append(parts, gs.parts[i])
	}
	return strings.Join(parts, " · ")
}

// Drop from the end while preserving the credit replacement in its original
// meter slot. Keeping indexes lets widened bars use the same selection.
func fitGaugeIndexes(gs gaugeSet, avail int) []int {
	kept := make([]int, len(gs.parts))
	for i := range kept {
		kept[i] = i
	}
	for len(kept) > 1 && lipgloss.Width(selectedGauges(gs, kept)) > avail {
		for i := len(kept) - 1; i >= 0; i-- {
			if gs.bars != nil && kept[i] == gs.creditIndex {
				continue
			}
			kept = append(kept[:i], kept[i+1:]...)
			break
		}
	}
	return kept
}

func fitGaugeLine(build func(int) gaugeSet, avail int) string {
	gs := build(defaultBarW)
	kept := fitGaugeIndexes(gs, avail)
	line := selectedGauges(gs, kept)
	barCount := 0
	for _, i := range kept {
		if gs.bars != nil {
			if gs.bars[i] {
				barCount++
			}
		} else if i < gs.barred {
			barCount++
		}
	}
	if barCount > 0 {
		if grow := (avail - lipgloss.Width(line)) / barCount; grow > 0 {
			wide := selectedGauges(build(defaultBarW+grow), kept)
			if lipgloss.Width(wide) <= avail {
				line = wide
			}
		}
	}
	return line
}
