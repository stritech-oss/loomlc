package prompt

import (
	"embed"
	"fmt"
	"path"
	"regexp"
	"strings"
	"text/template"

	"github.com/stritech-oss/loomlc/internal/sink"
	"github.com/stritech-oss/loomlc/internal/task"
)

//go:embed templates/*.tmpl
var templates embed.FS

// Mode is which flow a step is running in.
type Mode int

const (
	// Implement takes a task from a plan to a proposed change.
	Implement Mode = iota + 1
	// Feedback revises a change that a reviewer asked for changes on.
	Feedback
)

func (m Mode) String() string {
	switch m {
	case Implement:
		return "implement"
	case Feedback:
		return "feedback"
	default:
		return fmt.Sprintf("mode %d", int(m))
	}
}

// Commit is one commit already on the branch, which a change step can name as the target of a fix.
type Commit struct {
	SHA     string
	Subject string
}

// Gate is a check loomlc ran itself before a verdict step.
type Gate struct {
	Name string
	// Passed is whether the command exited zero.
	Passed bool
	// Output is what the command printed, already redacted and bounded by the caller.
	Output string
}

// Finding is one defect a verdict step reported.
type Finding struct {
	Path   string
	Line   int
	Detail string
}

// Data is what a task prompt is built from. Task, Feedback, Plan, Change, Findings and Gates all carry
// text loomlc didn't write — from a task's author, a reviewer, an earlier step, or a command's output —
// so Render fences every one of them rather than trusting the caller to have cleaned them.
type Data struct {
	Task task.Task
	// Lifecycle and Step name what's running, so an agent can describe its own situation.
	Lifecycle string
	Step      string
	// Dir is the workspace the agent works in, for messages; the provider sets the working directory.
	Dir    string
	Branch string
	Base   string
	// Plan is the plan step's answer, rendered for the steps that follow it.
	Plan string
	// Change is the change step's summary, for the verdict step that reviews it.
	Change string
	// Iteration and MaxIter are which pass through the loop this is, counting from 1.
	Iteration int
	MaxIter   int
	// Findings are the previous verdict's defects, for the change step that addresses them.
	Findings []Finding
	// Feedback is the review feedback a feedback run addresses.
	Feedback []sink.FeedbackItem
	// Commits are the commits on the branch, oldest first, which a fix names by SHA.
	Commits []Commit
	// Gates are the checks loomlc ran before this step.
	Gates []Gate
	// ProtectedPaths are paths the run must not change.
	ProtectedPaths []string
	// Commands are the commands the step's provider will let the agent run, as it would type them.
	// Empty means it can only read: an agent that knows this doesn't burn its turns on denied calls.
	Commands []string
}

// validate refuses a prompt that would mislead the agent reading it: a plan step with no task, a change
// step told to follow a plan that isn't there, or a feedback run with no feedback to address.
func (d Data) validate(mode Mode, output string) error {
	var missing []string
	need := func(name, value string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	need("task id", d.Task.Ref.ID)
	need("task title", d.Task.Title)
	need("lifecycle", d.Lifecycle)
	need("step", d.Step)
	need("dir", d.Dir)
	need("branch", d.Branch)
	need("base", d.Base)
	if output != OutputPlan {
		// A feedback run has no plan step, so it revises against the review feedback instead.
		if mode == Implement {
			need("plan", d.Plan)
		}
		if d.Iteration < 1 || d.MaxIter < 1 || d.Iteration > d.MaxIter {
			missing = append(missing, fmt.Sprintf("a pass between 1 and MaxIter, not %d of %d", d.Iteration, d.MaxIter))
		}
	}
	if mode == Feedback && len(d.Feedback) == 0 {
		missing = append(missing, "the feedback to address")
	}
	if len(missing) > 0 {
		return fmt.Errorf("prompt: a %q step in %s mode needs %s", output, mode, strings.Join(missing, ", "))
	}
	return nil
}

// TaskText is the task in its author's own words, which a template fences as untrusted.
func (d Data) TaskText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n", d.Task.Title)
	if len(d.Task.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(d.Task.Labels, ", "))
	}
	fmt.Fprintf(&b, "\n%s", d.Task.Body)
	return b.String()
}

// FindingsText is the review step's findings as a numbered list, which a template fences: the details
// are an agent's words about text loomlc doesn't trust.
func (d Data) FindingsText() string {
	var b strings.Builder
	for i, f := range d.Findings {
		where := ""
		if f.Path != "" {
			where = oneLine(f.Path)
			if f.Line > 0 {
				where = fmt.Sprintf("%s:%d", where, f.Line)
			}
			where = "`" + where + "` — "
		}
		detail := strings.ReplaceAll(strings.TrimSpace(f.Detail), "\n", "\n   ")
		fmt.Fprintf(&b, "%d. %s%s\n", i+1, where, detail)
	}
	return b.String()
}

// Render builds the task prompt for a step. The prompt carries the task's own words, so the caller sends
// it on stdin, never as a command-line argument.
func Render(mode Mode, output string, d Data) (string, error) {
	name, err := templateName(mode, output)
	if err != nil {
		return "", err
	}
	if err := d.validate(mode, output); err != nil {
		return "", err
	}
	t, err := template.New(path.Base(name)).Funcs(funcs()).Option("missingkey=error").ParseFS(templates, name)
	if err != nil {
		panic("prompt: built-in template " + name + " doesn't parse: " + err.Error())
	}
	var b strings.Builder
	if err := t.Execute(&b, d); err != nil {
		return "", fmt.Errorf("prompt: render %s: %w", name, err)
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}

func templateName(mode Mode, output string) (string, error) {
	switch {
	case mode == Implement && output == OutputPlan:
		return "templates/implement_plan.md.tmpl", nil
	case mode == Implement && output == OutputChange:
		return "templates/implement_change.md.tmpl", nil
	case mode == Implement && output == OutputVerdict:
		return "templates/implement_verdict.md.tmpl", nil
	case mode == Feedback && output == OutputChange:
		return "templates/feedback_change.md.tmpl", nil
	case mode == Feedback && output == OutputVerdict:
		return "templates/feedback_verdict.md.tmpl", nil
	case mode == Feedback && output == OutputPlan:
		return "", fmt.Errorf("prompt: a feedback run has no plan step")
	default:
		return "", fmt.Errorf("prompt: no template for a %q step in %s mode", output, mode)
	}
}

// untrustedTag wraps text nobody at loomlc wrote.
const untrustedTag = "untrusted-text"

func funcs() template.FuncMap {
	return template.FuncMap{
		"untrusted": untrusted,
		"oneLine":   oneLine,
		"indent":    indent,
		// add1 numbers a list from 1, since text/template has no arithmetic.
		"add1": func(i int) int { return i + 1 },
	}
}

// fenceTag matches any spelling of the fence's own tag: either end of it, in any case, with whatever
// spacing. A model reading an XML-ish fence doesn't treat "</UNTRUSTED-TEXT>" as different from the
// lowercase tag, so neither does this.
var fenceTag = regexp.MustCompile(`(?i)<\s*/?\s*` + untrustedTag)

// untrusted fences text written by whoever filed the task, reviewed the change, or produced an earlier
// step's answer, with a label saying what it is. Any spelling of the fence's tag inside the text is
// escaped, so the text can't open or close a block of its own and have what follows read as
// instructions. The text is still shown in full: an agent that can see an injection attempt can report
// it, which it can't do if loomlc drops it.
func untrusted(label, text string) string {
	safe := fenceTag.ReplaceAllStringFunc(text, func(match string) string {
		// The backslash goes inside the angle bracket: in front of it, the tag would still read as
		// itself to anything scanning for the tag's text.
		return "<\\" + strings.TrimPrefix(match, "<")
	})
	safe = strings.TrimRight(safe, "\n")
	if strings.TrimSpace(safe) == "" {
		safe = "(empty)"
	}
	return fmt.Sprintf("<%s label=%q>\n%s\n</%s>", untrustedTag, oneLine(label), safe, untrustedTag)
}

// oneLine flattens text that goes into a heading, a list item, or the fence's own label, so it can't add
// lines of its own to the prompt. Callers use it for short forge-supplied values, such as an author's
// name or a commit subject.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// indent shifts every line of text by n spaces, so a block keeps its place in a list.
func indent(n int, text string) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}
