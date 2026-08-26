package opensciskill

import (
	"fmt"
	"regexp"
	"strings"
)

type InstructionSection struct {
	ID      string `json:"id"`
	Heading string `json:"heading"`
	Level   int    `json:"level"`
	Runes   int    `json:"runes"`
	start   int
	end     int
}

var markdownHeading = regexp.MustCompile(`(?m)^(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$`)

const maxInstructionSections = 256

func instructionSections(body string) []InstructionSection {
	byteToRune := make([]int, len(body)+1)
	runeIndex := 0
	for byteIndex := range body {
		byteToRune[byteIndex] = runeIndex
		runeIndex++
	}
	byteToRune[len(body)] = len([]rune(body))
	matches := markdownHeading.FindAllStringSubmatchIndex(body, -1)
	if len(matches) > maxInstructionSections {
		matches = matches[:maxInstructionSections]
	}
	result := make([]InstructionSection, 0, len(matches))
	if len(matches) > 0 && strings.TrimSpace(body[:matches[0][0]]) != "" {
		result = append(result, InstructionSection{ID: "section-0", Heading: "Introduction", Level: 0, Runes: byteToRune[matches[0][0]], start: 0, end: byteToRune[matches[0][0]]})
	}
	for index, match := range matches {
		level := match[3] - match[2]
		start := byteToRune[match[0]]
		end := byteToRune[len(body)]
		if index+1 < len(matches) {
			end = byteToRune[matches[index+1][0]]
		}
		result = append(result, InstructionSection{ID: fmt.Sprintf("section-%d", index+1), Heading: strings.TrimSpace(body[match[4]:match[5]]), Level: level, Runes: end - start, start: start, end: end})
	}
	return result
}

func structuredInstructionChunk(name string, origin Origin, body, section string, offset, limit int) (Chunk, error) {
	if offset < 0 {
		return Chunk{}, fmt.Errorf("Skill offset must not be negative")
	}
	if limit == 0 {
		limit = DefaultReadRunes
	}
	if limit < 1 || limit > MaxReadRunes {
		return Chunk{}, fmt.Errorf("Skill read limit must be between 1 and %d runes", MaxReadRunes)
	}
	sections, bodyRunes := instructionSections(body), []rune(body)
	if section == "" && len(bodyRunes) <= limit {
		return Chunk{Name: name, Origin: origin, Mode: "full", Content: body, NextOffset: len(bodyRunes), TotalRunes: len(bodyRunes), Sections: publicSections(sections)}, nil
	}
	if section == "" && offset == 0 && len(sections) > 0 {
		return Chunk{Name: name, Origin: origin, Mode: "index", TotalRunes: len(bodyRunes), Truncated: true, Sections: publicSections(sections)}, nil
	}
	if section == "" {
		return sliceRunes(name, origin, body, offset, limit)
	}
	for _, item := range sections {
		if item.ID != section {
			continue
		}
		sectionRunes := bodyRunes[item.start:item.end]
		if offset > len(sectionRunes) {
			return Chunk{}, fmt.Errorf("Skill section offset exceeds its length")
		}
		end := min(len(sectionRunes), offset+limit)
		return Chunk{Name: name, Origin: origin, Mode: "section", Section: item.ID, Content: string(sectionRunes[offset:end]), Offset: offset, NextOffset: end, TotalRunes: len(sectionRunes), Truncated: end < len(sectionRunes), Sections: publicSections(sections)}, nil
	}
	return Chunk{}, fmt.Errorf("Skill section %q was not found", section)
}

func publicSections(values []InstructionSection) []InstructionSection {
	result := make([]InstructionSection, len(values))
	copy(result, values)
	return result
}
