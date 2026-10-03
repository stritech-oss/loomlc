package run

import (
	"fmt"
	"regexp"
	"strings"
)

// heading matches a markdown heading of any level.
var heading = regexp.MustCompile(`(?m)^(#{1,6})\s+(.*?)\s*$`)

// Sections names the headings loomlc fills, in the repository's own words. Each entry is tried in order,
// so a template can call its description section whatever it likes.
type Sections struct {
	Description []string
	QA          []string
	Issue       []string
}

// Parts is what loomlc puts in a pull request description.
type Parts struct {
	// Description is what the change does, from the change step.
	Description string
	// QA is how it was verified, from the review step.
	QA string
	// Issue closes the task, such as "Closes #42".
	Issue string
	// Warnings go at the top, where a reader sees them before the description.
	Warnings []string
}

// Body fills a repository's pull request template. Headings it doesn't recognise are left exactly as they
// were: a checklist is for the human who merges, and a section loomlc doesn't understand isn't its to
// rewrite.
//
// A template with none of the headings is an error rather than a description quietly thrown away.
func Body(template string, sections Sections, parts Parts) (string, error) {
	if strings.TrimSpace(template) == "" {
		return "", fmt.Errorf("the pull request template is empty")
	}
	filled := map[string]bool{}
	var b strings.Builder
	writeWarnings(&b, parts.Warnings)

	for _, section := range split(template) {
		if section.heading != "" {
			b.WriteString(section.heading + "\n")
		}
		switch {
		case matches(section.title, sections.Description):
			write(&b, parts.Description)
			filled["description"] = true
		case matches(section.title, sections.QA):
			write(&b, parts.QA)
			filled["qa"] = true
		case matches(section.title, sections.Issue):
			write(&b, parts.Issue)
			filled["issue"] = true
		default:
			b.WriteString(section.body)
		}
	}
	if len(filled) == 0 {
		return "", fmt.Errorf("the pull request template has none of the headings this sink names: %s", strings.Join(append(append(sections.Description, sections.QA...), sections.Issue...), ", "))
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

// section is one heading of a template and everything under it.
type section struct {
	heading, title, body string
}

// split breaks a template into its headings. Anything before the first heading is kept as a section with
// no heading, so a template that opens with a note doesn't lose it.
func split(template string) []section {
	marks := heading.FindAllStringSubmatchIndex(template, -1)
	if len(marks) == 0 {
		return []section{{body: template}}
	}

	var sections []section
	if preamble := template[:marks[0][0]]; strings.TrimSpace(preamble) != "" {
		sections = append(sections, section{body: preamble})
	}
	for i, mark := range marks {
		end := len(template)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		sections = append(sections, section{
			heading: strings.TrimRight(template[mark[0]:mark[1]], "\n"),
			title:   template[mark[4]:mark[5]],
			body:    template[mark[1]:end],
		})
	}
	return sections
}

// matches reports whether a heading is one of the names a sink gave, ignoring case and punctuation around
// it, since "## QA" and "## QA / Testing" are the same section to a person.
func matches(title string, names []string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	for _, name := range names {
		if title == strings.ToLower(strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func write(b *strings.Builder, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		content = "_Nothing to report._"
	}
	fmt.Fprintf(b, "\n%s\n\n", content)
}

func writeWarnings(b *strings.Builder, warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(b, "> **%s**\n", strings.TrimSpace(w))
	}
	if len(warnings) > 0 {
		b.WriteString("\n")
	}
}
