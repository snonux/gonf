package file

// Managed blocks (WithBlock): a File owns the lines between the marker lines
// "# BEGIN GONF <name>" and "# END GONF <name>" and leaves every other line
// of the file alone. resolveLine (lineedit.go) applies the blocks before the
// other line edits; validateBlocks keeps their ownership disjoint at
// declaration so a single pass converges.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// blockMarkers returns the BEGIN and END marker lines of the block name. The
// format matches the Cron resource's "# BEGIN GONF Cron[name]" markers.
func blockMarkers(name string) (begin, end string) {
	return "# BEGIN GONF " + name, "# END GONF " + name
}

// applyBlock returns lines with block's region replaced by block.Lines. The
// markers match a line after trimming its surrounding whitespace, and a
// matched marker line is kept as it is, so an indented marker does not
// flip-flop. A file without either marker gets the block, markers included,
// appended at its end. Any other marker layout (a marker twice, only one of
// them, END before BEGIN) is an error: gonf cannot tell which lines it owns,
// and writing anyway could delete another tool's lines.
func applyBlock(lines []string, block resource.Block) ([]string, error) {
	begin, end := blockMarkers(block.Name)
	var begins, ends []int
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case begin:
			begins = append(begins, i)
		case end:
			ends = append(ends, i)
		}
	}
	switch {
	case len(begins) == 0 && len(ends) == 0:
		out := make([]string, 0, len(lines)+len(block.Lines)+2)
		out = append(out, lines...)
		out = append(out, begin)
		out = append(out, block.Lines...)
		return append(out, end), nil
	case len(begins) == 1 && len(ends) == 1 && begins[0] < ends[0]:
		out := make([]string, 0, len(lines)+len(block.Lines))
		out = append(out, lines[:begins[0]+1]...)
		out = append(out, block.Lines...)
		return append(out, lines[ends[0]:]...), nil
	default:
		return nil, fmt.Errorf("managed block %q: want one %q line followed by one %q line, found %d BEGIN and %d END markers%s",
			block.Name, begin, end, len(begins), len(ends), endBeforeBegin(begins, ends))
	}
}

// endBeforeBegin explains the one-each layout applyBlock still refuses.
func endBeforeBegin(begins, ends []int) string {
	if len(begins) == 1 && len(ends) == 1 {
		return " (END before BEGIN)"
	}
	return ""
}

// validateBlocks refuses WithBlock declarations whose ownership is ambiguous
// or cannot converge (see opt.WithBlock): an empty name, a name with a line
// break or surrounding whitespace (the marker match trims lines), the same
// name with two different line sets, a block line with a line break or equal
// to a marker, and a WithLine/WithoutLine or WithKeyedLine key that would
// act on a block or marker line.
func (f *File) validateBlocks(path string) error {
	markers := make(map[string]string, 2*len(f.blocks))
	for i, block := range f.blocks {
		if err := validateBlockName(block.Name); err != nil {
			return fmt.Errorf("file %s: %w", path, err)
		}
		for _, other := range f.blocks[:i] {
			if other.Name == block.Name {
				return fmt.Errorf("file %s: WithBlock %q is declared with two different line sets", path, block.Name)
			}
		}
		begin, end := blockMarkers(block.Name)
		markers[begin], markers[end] = block.Name, block.Name
	}
	for _, block := range f.blocks {
		for _, line := range block.Lines {
			if strings.ContainsAny(line, "\r\n") {
				return fmt.Errorf("file %s: WithBlock %q line %q contains a line break", path, block.Name, line)
			}
			if owner, ok := markers[strings.TrimSpace(line)]; ok {
				return fmt.Errorf("file %s: WithBlock %q line %q is a marker of block %q", path, block.Name, line, owner)
			}
		}
	}
	return f.validateBlockOverlap(path, markers)
}

// validateBlockName checks one block name for validateBlocks.
func validateBlockName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("WithBlock requires a non-empty name")
	case strings.ContainsAny(name, "\r\n"):
		return fmt.Errorf("WithBlock name %q contains a line break", name)
	case strings.TrimSpace(name) != name:
		return fmt.Errorf("WithBlock name %q has surrounding whitespace; the marker match trims it, so the block would never be found", name)
	}
	return nil
}

// validateBlockOverlap refuses the other line edits of f that would touch a
// block's lines or markers: the edit would undo the block (or the block the
// edit) on every apply, so neither would converge.
func (f *File) validateBlockOverlap(path string, markers map[string]string) error {
	owned := make(map[string]string, len(markers))
	for marker, name := range markers {
		owned[marker] = name
	}
	for _, block := range f.blocks {
		for _, line := range block.Lines {
			owned[line] = block.Name
		}
	}
	for _, line := range slices.Concat(f.addLines, f.removeLines) {
		if name, ok := owned[line]; ok {
			return fmt.Errorf("file %s: WithLine/WithoutLine %q is owned by WithBlock %q", path, line, name)
		}
		if name, ok := markers[strings.TrimSpace(line)]; ok {
			return fmt.Errorf("file %s: WithLine/WithoutLine %q is a marker of WithBlock %q", path, line, name)
		}
	}
	for _, edit := range f.keyedLines {
		for line, name := range owned {
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), edit.Key) {
				return fmt.Errorf("file %s: WithKeyedLine key %q prefixes line %q of WithBlock %q", path, edit.Key, line, name)
			}
		}
	}
	return nil
}

// cloneBlocks deep-copies blocks (each Lines slice gets its own storage),
// keeping nil and empty apart like slices.Clone.
func cloneBlocks(blocks []resource.Block) []resource.Block {
	if blocks == nil {
		return nil
	}
	out := make([]resource.Block, len(blocks))
	for i, block := range blocks {
		out[i] = resource.Block{Name: block.Name, Lines: slices.Clone(block.Lines)}
	}
	return out
}

// wireBlocks and draftBlocks convert managed blocks between the draft and
// wire types (plan imports resource, so neither can be the other). Both keep
// nil for no blocks and for an empty block's lines, so an op encodes the
// same whether a recipe passed no lines or an empty slice.
func wireBlocks(blocks []resource.Block) []plan.Block {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]plan.Block, len(blocks))
	for i, block := range blocks {
		out[i] = plan.Block{Name: block.Name, Lines: nilIfEmpty(block.Lines)}
	}
	return out
}

func draftBlocks(blocks []plan.Block) []resource.Block {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]resource.Block, len(blocks))
	for i, block := range blocks {
		out[i] = resource.Block{Name: block.Name, Lines: nilIfEmpty(block.Lines)}
	}
	return out
}

// nilIfEmpty clones lines, returning nil for an empty slice.
func nilIfEmpty(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	return slices.Clone(lines)
}
