// Package github reads tasks from GitHub issues and publishes results as pull requests, through the gh
// CLI, so it reuses the operator's existing gh authentication.
//
// The JSON fields it reads were checked against gh 2.100.0; see testdata/README.md.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stritech-oss/loomlc/internal/proc"
	"github.com/stritech-oss/loomlc/internal/sink"
	"github.com/stritech-oss/loomlc/internal/task"
)

const (
	defaultCmd = "gh"
	// listLimit replaces gh's default of 30, which would hide older tasks.
	listLimit = "200"
	// maxTitle and maxBody bound the untrusted text a task can carry into a prompt.
	maxTitle = 256
	maxBody  = 64 << 10
	// maxFeedbackItems and maxFeedbackBody bound the untrusted text a feedback run collects.
	maxFeedbackItems = 100
	maxFeedbackBody  = 16 << 10
	// maxStderr bounds how much of gh's error output an error message keeps.
	maxStderr = 4 << 10
	// maxCommand bounds the command an error message echoes back.
	maxCommand = 1 << 10
	truncated  = "\n…truncated by loomlc…"
)

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	prURL       = regexp.MustCompile(`https://\S+/pull/(\d+)`)
)

// botLogins are accounts whose comments are never review feedback. GitHub reports some of them without the
// [bot] suffix, so the suffix alone isn't enough.
var botLogins = []string{"dependabot", "github-actions", "renovate", "codecov", "copilot"}

// Labels are the labels that track a task through a run.
type Labels struct {
	InProgress string
	Done       string
	Failed     string
}

// FeedbackLabels are the labels that drive feedback runs on an open output.
type FeedbackLabels struct {
	// Ready is what a reviewer adds to ask loomlc to act on their feedback.
	Ready string
	// InProgress is what loomlc adds while it works on that feedback.
	InProgress string
}

// Options configure a Forge.
type Options struct {
	// SourceName and SinkName are the configured names, used in refs and messages.
	SourceName string
	SinkName   string
	// Repo is the GitHub repository as owner/name.
	Repo string
	// Cmd is the gh executable. Empty means "gh".
	Cmd string
	// Env is gh's environment. It needs whatever gh uses to authenticate, such as HOME or GH_TOKEN.
	Env []string
	// Trigger is the label that marks a task ready to pick up.
	Trigger string
	Labels  Labels
	// OutputLabel marks the pull requests loomlc opens.
	OutputLabel string
	Feedback    FeedbackLabels
	// SinceLastReply limits collected feedback to items posted after loomlc's last reply.
	SinceLastReply bool
	// SelfLogin is the account loomlc posts as, used to tell its own replies from anyone else's. Empty
	// asks gh which account it is authenticated as, which a GitHub App installation token can't answer.
	//
	// When loomlc runs as the operator, this is the operator's own account, so a reply marker the
	// operator typed themselves counts as loomlc's. Giving loomlc its own account separates the two.
	SelfLogin string
}

// Forge reads tasks from GitHub issues and publishes results as pull requests.
type Forge struct {
	opts   Options
	runner proc.Runner
	env    []string

	mu        sync.Mutex
	selfLogin string // the account gh posts as, asked for once and kept
}

// New returns a Forge that runs gh through runner.
func New(opts Options, runner proc.Runner) (*Forge, error) {
	if !repoPattern.MatchString(opts.Repo) {
		return nil, fmt.Errorf("github forge: repository %q must be written as owner/name", opts.Repo)
	}
	required := []struct{ name, value string }{
		{"source name", opts.SourceName},
		{"sink name", opts.SinkName},
		{"trigger label", opts.Trigger},
		{"in-progress label", opts.Labels.InProgress},
		{"done label", opts.Labels.Done},
		{"failed label", opts.Labels.Failed},
		{"output label", opts.OutputLabel},
		{"feedback label", opts.Feedback.Ready},
		{"feedback in-progress label", opts.Feedback.InProgress},
	}
	for _, r := range required {
		if r.value == "" {
			return nil, fmt.Errorf("github forge: %s is required", r.name)
		}
	}
	if opts.Cmd == "" {
		opts.Cmd = defaultCmd
	}
	// gh must never wait for input or print update notices: nobody is watching an unattended run.
	env := append(slices.Clone(opts.Env), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	return &Forge{opts: opts, runner: runner, env: env, selfLogin: opts.SelfLogin}, nil
}

// NextReady returns the lowest-numbered open task carrying the trigger label that isn't already claimed,
// finished, or listed in exclude.
func (f *Forge) NextReady(ctx context.Context, exclude []task.Ref) (task.Ref, bool, error) {
	var issues []issueItem
	err := f.decode(ctx, &issues,
		"issue", "list", "--repo", f.opts.Repo, "--label", f.opts.Trigger,
		"--state", "open", "--limit", listLimit, "--json", "number,labels")
	if err != nil {
		return task.Ref{}, false, fmt.Errorf("list ready tasks: %w", err)
	}

	taken := []string{f.opts.Labels.InProgress, f.opts.Labels.Done, f.opts.Labels.Failed}
	next := 0
	for _, issue := range issues {
		names := labelNames(issue.Labels)
		ref := task.Ref{Source: f.opts.SourceName, ID: strconv.Itoa(issue.Number)}
		switch {
		case slices.ContainsFunc(taken, func(l string) bool { return slices.Contains(names, l) }):
		case slices.Contains(exclude, ref):
		case next == 0 || issue.Number < next:
			next = issue.Number
		}
	}
	if next == 0 {
		return task.Ref{}, false, nil
	}
	return task.Ref{Source: f.opts.SourceName, ID: strconv.Itoa(next)}, true, nil
}

// Claim marks a task as being worked on, so other runs leave it alone.
func (f *Forge) Claim(ctx context.Context, ref task.Ref) error {
	n, err := number(ref, ref.ID)
	if err != nil {
		return err
	}
	if _, err := f.run(ctx, "", "issue", "edit", n, "--repo", f.opts.Repo, "--add-label", f.opts.Labels.InProgress); err != nil {
		return fmt.Errorf("claim %s: %w", ref, err)
	}
	return nil
}

// InProgress counts the tasks currently claimed.
func (f *Forge) InProgress(ctx context.Context) (int, error) {
	var issues []issueItem
	err := f.decode(ctx, &issues,
		"issue", "list", "--repo", f.opts.Repo, "--label", f.opts.Labels.InProgress,
		"--state", "open", "--limit", listLimit, "--json", "number")
	if err != nil {
		return 0, fmt.Errorf("count claimed tasks: %w", err)
	}
	return len(issues), nil
}

// Get returns the task, with its untrusted text bounded.
func (f *Forge) Get(ctx context.Context, ref task.Ref) (task.Task, error) {
	n, err := number(ref, ref.ID)
	if err != nil {
		return task.Task{}, err
	}
	var issue issueView
	err = f.decode(ctx, &issue,
		"issue", "view", n, "--repo", f.opts.Repo,
		"--json", "number,title,body,url,labels,closed")
	if err != nil {
		return task.Task{}, fmt.Errorf("read %s: %w", ref, err)
	}
	return task.Task{
		Ref:    task.Ref{Source: f.opts.SourceName, ID: strconv.Itoa(issue.Number)},
		Title:  bound(issue.Title, maxTitle),
		Body:   bound(issue.Body, maxBody),
		URL:    issue.URL,
		Labels: labelNames(issue.Labels),
		Closed: issue.Closed,
	}, nil
}

// Comment posts a comment on the task.
func (f *Forge) Comment(ctx context.Context, ref task.Ref, body string) error {
	n, err := number(ref, ref.ID)
	if err != nil {
		return err
	}
	if _, err := f.run(ctx, body, "issue", "comment", n, "--repo", f.opts.Repo, "--body-file", "-"); err != nil {
		return fmt.Errorf("comment on %s: %w", ref, err)
	}
	return nil
}

// Transition records how a run ended by moving the task's labels. Ready only releases the claim, so an
// interrupted task is picked up again.
func (f *Forge) Transition(ctx context.Context, ref task.Ref, status task.Status) error {
	n, err := number(ref, ref.ID)
	if err != nil {
		return err
	}
	args := []string{"issue", "edit", n, "--repo", f.opts.Repo, "--remove-label", f.opts.Labels.InProgress}
	switch status {
	case task.Done:
		args = append(args, "--add-label", f.opts.Labels.Done)
	case task.Failed:
		args = append(args, "--add-label", f.opts.Labels.Failed)
	case task.Ready:
	default:
		return fmt.Errorf("move %s to an unknown status %d", ref, status)
	}
	if _, err := f.run(ctx, "", args...); err != nil {
		return fmt.Errorf("move %s to %s: %w", ref, status, err)
	}
	return nil
}

// OpenOutputs counts the pull requests loomlc has open and awaiting review.
func (f *Forge) OpenOutputs(ctx context.Context) (int, error) {
	var prs []prItem
	err := f.decode(ctx, &prs,
		"pr", "list", "--repo", f.opts.Repo, "--label", f.opts.OutputLabel,
		"--state", "open", "--limit", listLimit, "--json", "number")
	if err != nil {
		return 0, fmt.Errorf("count open outputs: %w", err)
	}
	return len(prs), nil
}

// NextFeedback returns the lowest-numbered open pull request whose reviewer asked for changes and that isn't
// already being revised, from a fork, or listed in exclude.
func (f *Forge) NextFeedback(ctx context.Context, exclude []sink.Ref) (sink.Ref, bool, error) {
	var prs []prItem
	err := f.decode(ctx, &prs,
		"pr", "list", "--repo", f.opts.Repo, "--label", f.opts.Feedback.Ready,
		"--state", "open", "--limit", listLimit, "--json", "number,labels,isCrossRepository")
	if err != nil {
		return sink.Ref{}, false, fmt.Errorf("list outputs with feedback: %w", err)
	}

	next := 0
	for _, pr := range prs {
		ref := sink.Ref{Sink: f.opts.SinkName, ID: strconv.Itoa(pr.Number)}
		switch {
		case pr.CrossRepo:
		case slices.Contains(labelNames(pr.Labels), f.opts.Feedback.InProgress):
		case slices.Contains(exclude, ref):
		case next == 0 || pr.Number < next:
			next = pr.Number
		}
	}
	if next == 0 {
		return sink.Ref{}, false, nil
	}
	return sink.Ref{Sink: f.opts.SinkName, ID: strconv.Itoa(next)}, true, nil
}

// ClaimFeedback marks a pull request as being revised.
func (f *Forge) ClaimFeedback(ctx context.Context, ref sink.Ref) error {
	n, err := number(ref, ref.ID)
	if err != nil {
		return err
	}
	if _, err := f.run(ctx, "", "pr", "edit", n, "--repo", f.opts.Repo, "--add-label", f.opts.Feedback.InProgress); err != nil {
		return fmt.Errorf("claim feedback on %s: %w", ref, err)
	}
	return nil
}

// ReleaseFeedback clears both feedback labels, whether the revision succeeded or not, so a reviewer can ask
// again by adding the label back.
func (f *Forge) ReleaseFeedback(ctx context.Context, ref sink.Ref) error {
	n, err := number(ref, ref.ID)
	if err != nil {
		return err
	}
	if _, err := f.run(ctx, "", "pr", "edit", n, "--repo", f.opts.Repo,
		"--remove-label", f.opts.Feedback.InProgress, "--remove-label", f.opts.Feedback.Ready); err != nil {
		return fmt.Errorf("release feedback on %s: %w", ref, err)
	}
	return nil
}

// Output returns the pull request's current state.
func (f *Forge) Output(ctx context.Context, ref sink.Ref) (sink.Output, error) {
	n, err := number(ref, ref.ID)
	if err != nil {
		return sink.Output{}, err
	}
	out, err := f.view(ctx, n)
	if err != nil {
		return sink.Output{}, fmt.Errorf("read %s: %w", ref, err)
	}
	return out, nil
}

// Propose publishes a change as a pull request. When one is already open for the branch, it's reused, since
// pushing to the branch has already updated it.
func (f *Forge) Propose(ctx context.Context, change sink.Change) (sink.Output, error) {
	existing, found, err := f.openForBranch(ctx, change.Branch, change.Base)
	if err != nil {
		return sink.Output{}, fmt.Errorf("propose %s: %w", change.Branch, err)
	}
	if found {
		return existing, nil
	}

	args := []string{"pr", "create", "--repo", f.opts.Repo, "--base", change.Base, "--head", change.Branch, "--title", change.Title, "--body-file", "-"}
	for _, label := range change.Labels {
		args = append(args, "--label", label)
	}
	if change.Draft {
		args = append(args, "--draft")
	}
	stdout, err := f.run(ctx, change.Body, args...)
	if err != nil {
		return sink.Output{}, fmt.Errorf("propose %s: %w", change.Branch, err)
	}
	match := prURL.FindStringSubmatch(stdout)
	if match == nil {
		return sink.Output{}, fmt.Errorf("propose %s: gh didn't print the pull request's URL: %s", change.Branch, strings.TrimSpace(stdout))
	}
	out := sink.Output{
		Ref:    sink.Ref{Sink: f.opts.SinkName, ID: match[1]},
		URL:    match[0],
		Branch: change.Branch,
		Base:   change.Base,
		Open:   true,
		Labels: change.Labels,
	}
	if change.Reviewer != "" {
		// GitHub refuses to request a review from the pull request's own author, which is normal in a
		// single-maintainer repository, so this can't fail the run.
		if _, err := f.run(ctx, "", "pr", "edit", match[1], "--repo", f.opts.Repo, "--add-reviewer", change.Reviewer); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("couldn't request a review from %s: %v", change.Reviewer, err))
		}
	}
	return out, nil
}

// CommentOn posts a comment on a pull request.
func (f *Forge) CommentOn(ctx context.Context, ref sink.Ref, body string) error {
	n, err := number(ref, ref.ID)
	if err != nil {
		return err
	}
	if _, err := f.run(ctx, body, "pr", "comment", n, "--repo", f.opts.Repo, "--body-file", "-"); err != nil {
		return fmt.Errorf("comment on %s: %w", ref, err)
	}
	return nil
}

// Feedback returns the human review feedback on a pull request, oldest first: review summaries, pull request
// comments, and inline comments on the diff. Comments from bots and loomlc's own comments are left out, and
// with SinceLastReply so is everything up to loomlc's last reply.
func (f *Forge) Feedback(ctx context.Context, ref sink.Ref) ([]sink.FeedbackItem, error) {
	n, err := number(ref, ref.ID)
	if err != nil {
		return nil, err
	}
	var conversation prConversation
	err = f.decode(ctx, &conversation, "pr", "view", n, "--repo", f.opts.Repo, "--json", "reviews,comments")
	if err != nil {
		return nil, fmt.Errorf("read feedback on %s: %w", ref, err)
	}
	var pages [][]inlineComment
	err = f.decode(ctx, &pages, "api", "--paginate", "--slurp", fmt.Sprintf("repos/%s/pulls/%s/comments", f.opts.Repo, n))
	if err != nil {
		return nil, fmt.Errorf("read inline feedback on %s: %w", ref, err)
	}

	var entries []conversationItem
	add := func(item sink.FeedbackItem, body string) {
		entries = append(entries, conversationItem{item: item, body: body})
	}

	for _, review := range conversation.Reviews {
		add(sink.FeedbackItem{ID: review.ID, Kind: "review", Author: review.Author.Login, CreatedAt: parseTime(review.SubmittedAt)}, review.Body)
	}
	for _, comment := range conversation.Comments {
		add(sink.FeedbackItem{ID: comment.ID, Kind: "comment", Author: comment.Author.Login, CreatedAt: parseTime(comment.CreatedAt)}, comment.Body)
	}
	for _, page := range pages {
		for _, inline := range page {
			add(sink.FeedbackItem{
				ID:        strconv.FormatInt(inline.ID, 10),
				Kind:      "inline",
				Author:    inline.User.Login,
				Path:      inline.Path,
				Line:      inline.Line,
				CreatedAt: parseTime(inline.CreatedAt),
			}, inline.Body)
		}
	}

	lastReply, err := f.lastReply(ctx, entries)
	if err != nil {
		return nil, fmt.Errorf("read feedback on %s: %w", ref, err)
	}

	var items []sink.FeedbackItem
	for _, e := range entries {
		switch {
		case strings.Contains(e.body, sink.Marker), isBot(e.item.Author), strings.TrimSpace(e.body) == "":
		// An item whose timestamp GitHub didn't give, or gave in a shape loomlc can't read, is kept:
		// dropping a reviewer's comment because of that would be silent and invisible.
		case !e.item.CreatedAt.IsZero() && !lastReply.IsZero() && !e.item.CreatedAt.After(lastReply):
		default:
			e.item.Body = bound(e.body, maxFeedbackBody)
			items = append(items, e.item)
		}
	}

	slices.SortStableFunc(items, func(a, b sink.FeedbackItem) int {
		// Items with no usable timestamp sort last, so the cap below trims the oldest known feedback
		// rather than the feedback loomlc knows least about.
		switch {
		case a.CreatedAt.IsZero() && b.CreatedAt.IsZero():
			return strings.Compare(a.ID, b.ID)
		case a.CreatedAt.IsZero():
			return 1
		case b.CreatedAt.IsZero():
			return -1
		case a.CreatedAt.Equal(b.CreatedAt):
			return strings.Compare(a.ID, b.ID)
		default:
			return a.CreatedAt.Compare(b.CreatedAt)
		}
	})
	if len(items) > maxFeedbackItems {
		items = items[len(items)-maxFeedbackItems:] // keep the most recent
	}
	return items, nil
}

// conversationItem is one thing someone wrote on a pull request, before loomlc decides whether it counts
// as feedback.
type conversationItem struct {
	item sink.FeedbackItem
	body string
}

// lastReply returns when loomlc last replied to feedback here, or the zero time when it hasn't replied or
// when the cutoff isn't in use.
//
// The reply marker is only text in a comment body, and anyone who can comment can type it. Honouring it
// whoever wrote it would let one comment saying "looks fine" plus the marker hide every review comment
// that came before it. So it counts only from the account gh is authenticated as, which is the account
// loomlc posts under.
func (f *Forge) lastReply(ctx context.Context, entries []conversationItem) (time.Time, error) {
	var last time.Time
	if !f.opts.SinceLastReply || !slices.ContainsFunc(entries, func(e conversationItem) bool {
		return strings.Contains(e.body, sink.ReplyMarker)
	}) {
		return last, nil
	}

	self, err := f.self(ctx)
	if err != nil {
		return last, err
	}
	for _, e := range entries {
		if strings.Contains(e.body, sink.ReplyMarker) && strings.EqualFold(e.item.Author, self) && e.item.CreatedAt.After(last) {
			last = e.item.CreatedAt
		}
	}
	return last, nil
}

// self returns the login gh is authenticated as. It's asked for once, and only when something claims to
// be one of loomlc's own replies.
func (f *Forge) self(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.selfLogin != "" {
		return f.selfLogin, nil
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := f.decode(ctx, &user, "api", "user"); err != nil {
		// A GitHub App installation token can't read /user at all, so this is a normal thing to hit
		// rather than a broken setup: say which setting answers it instead.
		return "", fmt.Errorf("ask gh which account it posts as: %w; set the sink's self_login to that account to skip the question", err)
	}
	if user.Login == "" {
		return "", errors.New("gh didn't say which account it posts as")
	}
	f.selfLogin = user.Login
	return f.selfLogin, nil
}

// openForBranch returns loomlc's own open pull request for a branch, if there is one.
//
// GitHub matches a head ref name across every pull request aimed at the repository, forks included, and
// loomlc's branch names are predictable. A pull request from a fork is somebody else's: loomlc can't push
// to it, and reusing it would mean commenting on a stranger's branch, marking the task done against it,
// and never publishing the work that was actually built. So only a pull request from this repository,
// aimed at the base the change names, is ever reused.
func (f *Forge) openForBranch(ctx context.Context, branch, base string) (sink.Output, bool, error) {
	var prs []prView
	err := f.decode(ctx, &prs,
		"pr", "list", "--repo", f.opts.Repo, "--head", branch, "--state", "open", "--limit", "10",
		"--json", "number,url,state,headRefName,baseRefName,isCrossRepository,labels")
	if err != nil {
		return sink.Output{}, false, err
	}
	for _, pr := range prs {
		if pr.CrossRepo || pr.HeadRefName != branch || pr.BaseRefName != base {
			continue
		}
		return f.output(pr), true, nil
	}
	return sink.Output{}, false, nil
}

func (f *Forge) view(ctx context.Context, number string) (sink.Output, error) {
	var pr prView
	err := f.decode(ctx, &pr,
		"pr", "view", number, "--repo", f.opts.Repo,
		"--json", "number,url,state,headRefName,baseRefName,isCrossRepository,labels")
	if err != nil {
		return sink.Output{}, err
	}
	return f.output(pr), nil
}

func (f *Forge) output(pr prView) sink.Output {
	return sink.Output{
		Ref:       sink.Ref{Sink: f.opts.SinkName, ID: strconv.Itoa(pr.Number)},
		URL:       pr.URL,
		Branch:    pr.HeadRefName,
		Base:      pr.BaseRefName,
		Open:      strings.EqualFold(pr.State, "OPEN"),
		CrossRepo: pr.CrossRepo,
		Labels:    labelNames(pr.Labels),
	}
}

// run runs gh with args and returns its standard output.
func (f *Forge) run(ctx context.Context, stdin string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := proc.Cmd{Name: f.opts.Cmd, Args: args, Env: f.env, Stdout: &stdout, Stderr: &stderr}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	res, err := f.runner.Run(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("gh %s: %w", command(args), err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("gh %s: exit status %d: %s", command(args), res.ExitCode, bound(strings.TrimSpace(stderr.String()), maxStderr))
	}
	return stdout.String(), nil
}

// decode runs gh and reads its JSON output into out.
func (f *Forge) decode(ctx context.Context, out any, args ...string) error {
	stdout, err := f.run(ctx, "", args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		return fmt.Errorf("gh %s: unreadable output: %w", command(args), err)
	}
	return nil
}

// The shapes below are the parts of gh's JSON that loomlc reads; gh returns more.
type (
	label struct {
		Name string `json:"name"`
	}
	actor struct {
		Login string `json:"login"`
	}
	issueItem struct {
		Number int     `json:"number"`
		Labels []label `json:"labels"`
	}
	issueView struct {
		Number int     `json:"number"`
		Title  string  `json:"title"`
		Body   string  `json:"body"`
		URL    string  `json:"url"`
		Labels []label `json:"labels"`
		Closed bool    `json:"closed"`
	}
	prItem struct {
		Number    int     `json:"number"`
		Labels    []label `json:"labels"`
		CrossRepo bool    `json:"isCrossRepository"`
	}
	prView struct {
		Number      int     `json:"number"`
		URL         string  `json:"url"`
		State       string  `json:"state"`
		HeadRefName string  `json:"headRefName"`
		BaseRefName string  `json:"baseRefName"`
		CrossRepo   bool    `json:"isCrossRepository"`
		Labels      []label `json:"labels"`
	}
	prConversation struct {
		Reviews []struct {
			ID          string `json:"id"`
			Author      actor  `json:"author"`
			Body        string `json:"body"`
			SubmittedAt string `json:"submittedAt"`
		} `json:"reviews"`
		Comments []struct {
			ID        string `json:"id"`
			Author    actor  `json:"author"`
			Body      string `json:"body"`
			CreatedAt string `json:"createdAt"`
		} `json:"comments"`
	}
	inlineComment struct {
		ID        int64  `json:"id"`
		User      actor  `json:"user"`
		Body      string `json:"body"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		CreatedAt string `json:"created_at"`
	}
)

// number returns the issue or pull request number a ref names. GitHub numbers its issues and pull
// requests, so an id that isn't digits didn't come from this forge. Rejecting it here keeps text that
// reached loomlc as a task id, such as "--add-label", out of gh's argument list.
func number(ref fmt.Stringer, id string) (string, error) {
	if id == "" || strings.ContainsFunc(id, func(r rune) bool { return r < '0' || r > '9' }) {
		return "", fmt.Errorf("%s: a GitHub issue or pull request id must be a number, not %q", ref, id)
	}
	return id, nil
}

// valueFlags carry text loomlc didn't write, such as a title an agent chose. An error says the flag was
// there without repeating what was in it.
var valueFlags = []string{"--title", "--body", "--add-label", "--remove-label", "--label"}

// command renders args for an error message: bounded, with the values of text-carrying flags left out,
// so a failure doesn't paste an agent's title or a label's contents into every log that sees it.
func command(args []string) string {
	parts := make([]string, 0, len(args))
	skip := false
	for _, arg := range args {
		switch {
		case skip:
			parts = append(parts, "…")
			skip = false
		case slices.Contains(valueFlags, arg):
			parts = append(parts, arg)
			skip = true
		default:
			parts = append(parts, arg)
		}
	}
	return bound(strings.Join(parts, " "), maxCommand)
}

func labelNames(labels []label) []string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}

// isBot reports whether a login belongs to an automated account, whose comments aren't review feedback.
func isBot(login string) bool {
	login = strings.ToLower(login)
	return login == "" || strings.HasSuffix(login, "[bot]") || slices.Contains(botLogins, login)
}

// bound trims s to limit bytes, on a character boundary, and says so.
func bound(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8ValidCut(s, cut) {
		cut--
	}
	return s[:cut] + truncated
}

// utf8ValidCut reports whether cutting s at i leaves whole characters.
func utf8ValidCut(s string, i int) bool {
	return i == len(s) || s[i]&0xC0 != 0x80
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
