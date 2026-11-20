package jira_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// questionPrefix is orchestrator.itervoxCommentPrefix: the daemon finds its own
// question comment by this prefix of the text it reads back.
const questionPrefix = "🤖 **Agent needs your input**"

// itervoxTexts are texts Itervox writes to a tracker, built the way the code
// named in each name builds them. Comments go through
// tracker.MarkManagedComment, as at every call site. Each one must read back
// exactly, and rich holds the ADF node and mark types it must be written with.
var itervoxTexts = []struct {
	name string
	text string
	rich []string
}{
	{
		name: "input-required question (orchestrator/event_loop.go buildInputRequiredComment)",
		text: tracker.MarkManagedComment(questionPrefix + "\n\nShould I keep the old `config.yaml` keys, or move them to `.itervox/settings.jsonc`?\n\n---\n_Reply in the tracker or via the Itervox dashboard to continue._"),
		rich: []string{"strong", "code", "em"},
	},
	{
		name: "input-required question, inline input footer (buildInputRequiredComment)",
		text: tracker.MarkManagedComment(questionPrefix + "\n\nWhich database should the migration target?\n\n---\n_Reply to this comment to continue._"),
		rich: []string{"strong", "em"},
	},
	{
		name: "input-required question, truncated context (event_loop.go boundInputRequiredContext)",
		text: tracker.MarkManagedComment(questionPrefix + "\n\n_… earlier text truncated; the full text is in the Itervox dashboard._\n\nso which of the two layouts do you want?\n\n---\n_Reply in the tracker or via the Itervox dashboard to continue._"),
		rich: []string{"strong", "em"},
	},
	{
		name: "guardrail approval question (orchestrator/guardrails.go guardrailQuestion)",
		text: tracker.MarkManagedComment(questionPrefix + "\n\nAgent wants to run: `git push --force origin main`\n\nThis command is in guardrails.approval_commands. Reply `approve` to run it once; any other reply rejects it and is passed to the agent as the reason.\n\n---\n_Reply in the tracker or via the Itervox dashboard to continue._"),
		rich: []string{"strong", "code", "em"},
	},
	{
		name: "input-required question with a comment key (tracker.MarkCommentKey)",
		text: tracker.MarkCommentKey(tracker.MarkManagedComment(questionPrefix+"\n\nShip it?\n\n---\n_Reply to this comment to continue._"), "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"),
		rich: []string{"strong", "em"},
	},
	{
		name: "session summary (orchestrator/logging.go formatSessionComment)",
		text: tracker.MarkManagedComment("## Itervox Agent Session — KAN-7\n\n" +
			"I read `internal/tracker/jira/adf.go` and found the cause.\n\n" +
			"- `textToADF` wrote plain paragraphs\n- headings showed as `##`\n\n" +
			"```go\nfunc textToADF(text string) map[string]any {\n\treturn nil\n}\n```\n\n" +
			"1. Ran `go test ./internal/tracker/jira/...`\n2. All tests pass\n\n"),
		rich: []string{"heading", "code", "bulletList", "codeBlock", "orderedList"},
	},
	{
		name: "pull request link (orchestrator/worker.go)",
		text: tracker.MarkManagedComment("🔗 Pull request created: https://github.com/vnovick/itervox/pull/42"),
	},
	{
		name: "review verdict (orchestrator/review_feedback.go formatReviewVerdictComment)",
		text: tracker.MarkManagedComment("**Review by `reviewer` (claude): ❌ changes requested**\n\n" +
			"- The empty case has no test\n- `normalizeComment` drops the author name\n\n" +
			"**Line comments**\n\n- `internal/tracker/jira/adf.go:42` Handle a nil map"),
		rich: []string{"strong", "code", "bulletList"},
	},
	{
		name: "review verdict, reviewer changed the branch (formatReviewVerdictComment)",
		text: tracker.MarkManagedComment("**Review by `reviewer`: ❌ no verdict recorded** (counts as a block)\n\n" +
			"⚠️ This reviewer changed the branch (new commit 1a2b3c4). Reviewers are read-only, so its verdict counts as a block. Check those changes before merging."),
		rich: []string{"strong", "code"},
	},
	{
		name: "review outcome (orchestrator/review_feedback.go formatReviewOutcomeComment)",
		text: tracker.MarkManagedComment("**Review result: ❌ changes requested** (1 approve / 1 block, quorum `majority`)\n" +
			"- `reviewer`: approve\n- `security`: block\n\n" +
			"The reasons and line comments are in the reviewer comments above. Move the issue back to an active state to have the implementer address them."),
		rich: []string{"strong", "code"},
	},
	{
		name: "rate-limit switch with the retries-exhausted text (automation_rate_limited.go commentRateLimitedSwitch)",
		text: tracker.MarkManagedComment("🤖 Itervox: rate-limit auto-switch\n\n" +
			"Profile **default** exhausted retries on backend `claude` (consumed 1200 input + 300 output tokens). Re-dispatching this issue under profile **fallback** (backend override: `codex`).\n\n" +
			"Itervox: maximum retries exhausted (3 attempts). Last error:\n\nrate limit: 429 Too Many Requests\n\nRetries have stopped for this run. Itervox may move the issue to failed state, pause it, or let a matching automation recover it."),
		rich: []string{"strong", "code"},
	},
	{
		name: "rate-limit budget exhausted (automation_rate_limited.go commentRateLimitCapExhausted)",
		text: tracker.MarkManagedComment("🤖 Itervox: rate-limit auto-switch budget exhausted\n\n" +
			"Profile **default** hit a vendor rate-limit, but this issue has already burned through its **3 switches in the last 6 hours** budget. No further auto-switches will fire until the rolling window opens. Triage manually or raise the cap in /settings."),
		rich: []string{"strong"},
	},
	{
		name: "max retries exhausted (orchestrator/event_loop.go maxRetriesExhaustedBody)",
		text: tracker.MarkManagedComment("Itervox: maximum retries exhausted (3 attempts). Last error:\n\n" +
			"exit status 1: --- FAIL: TestNormalize (0.01s)\n    normalize_test.go:46: got \"\", want \"Hello\"\n\n" +
			"Retries have stopped for this run. Itervox may move the issue to failed state, pause it, or let a matching automation recover it."),
	},
	{
		name: "backend fallback (orchestrator/backend_gate.go)",
		text: tracker.MarkManagedComment("🤖 Itervox: backend fallback\n\n" +
			"Backend `claude` is limited (backend_fallback: claude@local limited until 2026-11-20T10:00:00Z). This issue now runs profile **default** on `codex`; it switches back per `agent.backend_fallback.switch_back`."),
		rich: []string{"strong", "code"},
	},
	{
		name: "review findings posted on the issue (server/comment_pr.go RenderCommentPRMarkdown)",
		text: tracker.MarkManagedComment("## 🤖 Itervox review\n\nTwo problems in the adapter.\n\n### Findings (2)\n\n" +
			"- ❌ **error** · `internal/tracker/jira/adf.go:12` — nil map write\n" +
			"- ⚠️ **warning** · `internal/tracker/jira/writes.go` — unused import\n"),
		rich: []string{"heading", "bulletList", "strong", "code"},
	},
	{
		name: "dashboard reply mirrored to the tracker (event_loop.go EventProvideInput)",
		text: tracker.MarkManagedComment("Keep the old keys and add a migration note."),
	},
	{
		name: "verify-skill issue description (orchestrator/verify_skill_issue.go verifySkillIssueBody)",
		text: "Itervox's readiness check found no project verification skill or script.\n\n" +
			"Write one, so every agent run can prove its change works the same way:\n\n" +
			"1. Find how this repository is built, linted and tested (CI config, Makefile, package.json, README).\n" +
			"2. Add `.claude/skills/verify/SKILL.md` with frontmatter (`name: verify`, a one-line `description` that says when to use it) and the exact commands, in order, that prove a change: format, lint, type check, unit tests, build.\n" +
			"3. If the commands are long, add `scripts/verify.sh` (or a `make verify` target) that runs them and exits non-zero on the first failure, and make the skill call it.\n" +
			"4. Run it once on a clean checkout and paste the output in your handoff.\n\n" +
			"Itervox's readiness check passes once any of these exists: a `.claude/skills/*verif*/SKILL.md`, a `scripts/verify*` file, a `verify` target in the Makefile, or a `verify` script in package.json.",
		rich: []string{"orderedList", "code"},
	},
	{
		// The reader trims the trailing newline of a whole document, as before;
		// comments never have one, because MarkManagedComment trims them.
		name: "lesson enforcement issue description (memory/lessons_enforce.go EnforcementIssueBody)",
		text: "Itervox suggested enforcing a lesson that keeps coming back. Implement the enforcement below; do not just repeat the lesson.\n\n" +
			"## Suggested enforcement (lint)\n\nAdd a lint rule that fails on a bare `exec.Command(\"git\", ...)`.\n\n" +
			"## Lesson\n\nRun git only through `gitexec`.\n\n" +
			"## Evidence\n\n- Memory entry: `mem-123` (project)\n- Trigger: correction, seen 3 times\n- First seen on: KAN-4\n",
		rich: []string{"heading", "bulletList", "code"},
	},
	{
		name: "New task issue description (orchestrator/task.go taskIssueBody)",
		text: "Created with Itervox (New task). Its instructions are kept by Itervox and shown in the issue's detail on the dashboard.",
	},
}

// everyConstruct uses every construct textToADF writes, nested marks
// included, so the live test sees Jira accept and store each one.
const everyConstruct = "# Every construct\n\n" +
	"## Heading with `code` and a [link](https://example.com/a)\n\n" +
	"**bold**, _italic_, **bold _and italic_**, **bold with `code` inside**, `code`, [a link](https://example.com/docs) and [`a code link`](https://example.com/c).\n\n" +
	"- one\n- **two**\n\n" +
	"3. three\n4. four\n\n" +
	"> quoted _text_\n> second line\n\n" +
	"```go\nfmt.Println(\"x\")\n\nreturn\n```\n\n" +
	"<!-- itervox:managed -->"

// adfTypes returns every node and mark type in an ADF tree, in tree order.
func adfTypes(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if t, ok := x["type"].(string); ok {
				out = append(out, t)
			}
			walk(x["marks"])
			walk(x["content"])
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func TestMarkdownCorpusRoundTrips(t *testing.T) {
	for _, tc := range itervoxTexts {
		t.Run(tc.name, func(t *testing.T) {
			doc := jira.TextToADF(tc.text)
			got := jira.ADFToText(doc)

			assert.Equal(t, strings.TrimRight(tc.text, " \t\r\n"), got, "the text must read back exactly")
			types := adfTypes(doc)
			for _, want := range tc.rich {
				assert.Contains(t, types, want, "written as rich text")
			}
			if strings.HasPrefix(tc.text, questionPrefix) {
				assert.True(t, strings.HasPrefix(got, questionPrefix), "the daemon still finds its question")
			}
			if key := "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"; tracker.CommentHasKey(tc.text, key) {
				assert.True(t, tracker.CommentHasKey(got, key), "the comment key survives")
			}
			if tracker.IsManagedComment(domain.Comment{Body: tc.text}) {
				assert.True(t, tracker.IsManagedComment(domain.Comment{Body: got}), "the managed marker survives")
			}
			if strings.Contains(tc.text, "\n---\n") {
				assert.Contains(t, got, "\n---\n", "the question footer split still works")
			}
		})
	}
}

// Each text, posted through the adapter to a fake that keeps comment
// properties the way the live site does, reads back exactly as it was
// written: the markers travel as properties and come back in place, and no
// marker is stored as text. The keyed sample carries its key marker in the
// text, as the live test posts it.
func TestMarkdownCorpusRoundTripsThroughTheAdapter(t *testing.T) {
	samples := append(slices.Clone(itervoxTexts), struct {
		name string
		text string
		rich []string
	}{name: "every construct", text: everyConstruct})
	for _, tc := range samples {
		t.Run(tc.name, func(t *testing.T) {
			f := &keyedFake{}
			c, seen := newJiraServer(t, f.handler)

			created, err := c.CreateComment(t.Context(), "PROJ-1", tc.text)
			require.NoError(t, err)
			d, err := c.FetchIssueDetail(t.Context(), "PROJ-1")
			require.NoError(t, err)

			require.Len(t, d.Comments, 1)
			assert.Equal(t, strings.TrimRight(tc.text, " \t\r\n"), d.Comments[0].Body, "the text must read back exactly")
			assert.Equal(t, d.Comments[0].Body, created.Body, "the created comment is what the next read gives")
			assert.NotContains(t, jira.ADFToText(seen()[0].jsonBody(t)["body"]), "<!-- itervox:", "no marker is stored as text")
		})
	}
}

func TestEveryConstructRoundTrips(t *testing.T) {
	doc := jira.TextToADF(everyConstruct)

	assert.Equal(t, everyConstruct, jira.ADFToText(doc))
	types := adfTypes(doc)
	for _, want := range []string{"heading", "strong", "em", "code", "link", "codeBlock", "bulletList", "orderedList", "blockquote", "hardBreak"} {
		assert.Contains(t, types, want)
	}
}

// The question's first block is rich: the prefix is a real bold mark that
// reads back as the exact prefix.
func TestQuestionPrefixIsWrittenBold(t *testing.T) {
	doc := jira.TextToADF(itervoxTexts[0].text)

	first := doc["content"].([]any)[0]
	b, err := json.Marshal(first)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"paragraph","content":[
		{"type":"text","text":"🤖 "},
		{"type":"text","text":"Agent needs your input","marks":[{"type":"strong"}]}]}`, string(b))
}

func TestMarkdownWritesRichNodes(t *testing.T) {
	cases := []struct {
		name, md, adf string
	}{
		{"heading level 1", "# Title", `{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Title"}]}`},
		{"heading level 6", "###### Small", `{"type":"heading","attrs":{"level":6},"content":[{"type":"text","text":"Small"}]}`},
		{"bold", "a **b** c", `{"type":"paragraph","content":[{"type":"text","text":"a "},
			{"type":"text","text":"b","marks":[{"type":"strong"}]},{"type":"text","text":" c"}]}`},
		{"italic with underscores", "_it_", `{"type":"paragraph","content":[{"type":"text","text":"it","marks":[{"type":"em"}]}]}`},
		{"inline code", "run `go test`", `{"type":"paragraph","content":[{"type":"text","text":"run "},
			{"type":"text","text":"go test","marks":[{"type":"code"}]}]}`},
		{"link", "see [the run](https://ci.example.com/run/42).", `{"type":"paragraph","content":[{"type":"text","text":"see "},
			{"type":"text","text":"the run","marks":[{"type":"link","attrs":{"href":"https://ci.example.com/run/42"}}]},
			{"type":"text","text":"."}]}`},
		{"code inside a link", "[`main.go`](https://example.com/main.go)", `{"type":"paragraph","content":[
			{"type":"text","text":"main.go","marks":[{"type":"link","attrs":{"href":"https://example.com/main.go"}},{"type":"code"}]}]}`},
		{"italic inside bold", "**a _b_**", `{"type":"paragraph","content":[
			{"type":"text","text":"a ","marks":[{"type":"strong"}]},
			{"type":"text","text":"b","marks":[{"type":"strong"},{"type":"em"}]}]}`},
		// ADF lets code combine only with link, so code inside bold is a code
		// node between two bold nodes. It reads back as the same Markdown.
		{"code inside bold", "**a `b` c**", `{"type":"paragraph","content":[
			{"type":"text","text":"a ","marks":[{"type":"strong"}]},
			{"type":"text","text":"b","marks":[{"type":"code"}]},
			{"type":"text","text":" c","marks":[{"type":"strong"}]}]}`},
		{"lines of one block", "one\n**two**", `{"type":"paragraph","content":[{"type":"text","text":"one"},{"type":"hardBreak"},
			{"type":"text","text":"two","marks":[{"type":"strong"}]}]}`},
		{"fenced code with a language", "```go\nx := 1\n\ny := 2\n```", `{"type":"codeBlock","attrs":{"language":"go"},
			"content":[{"type":"text","text":"x := 1\n\ny := 2"}]}`},
		{"fenced code without a language", "```\nplain\n```", `{"type":"codeBlock","content":[{"type":"text","text":"plain"}]}`},
		{"Markdown inside a code fence stays literal", "```\n**not bold** _not em_ # not a heading\n- not a list\n```",
			`{"type":"codeBlock","content":[{"type":"text","text":"**not bold** _not em_ # not a heading\n- not a list"}]}`},
		{"bullet list", "- a\n- **b**", `{"type":"bulletList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"b","marks":[{"type":"strong"}]}]}]}]}`},
		{"numbered list", "1. a\n2. b", `{"type":"orderedList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]}`},
		{"numbered list from 3", "3. c\n4. d", `{"type":"orderedList","attrs":{"order":3},"content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"c"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"d"}]}]}]}`},
		{"block quote", "> a\n> _b_", `{"type":"blockquote","content":[{"type":"paragraph","content":[
			{"type":"text","text":"a"},{"type":"hardBreak"},{"type":"text","text":"b","marks":[{"type":"em"}]}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := jira.TextToADF(tc.md)

			content := doc["content"].([]any)
			require.Len(t, content, 1)
			b, err := json.Marshal(content[0])
			require.NoError(t, err)
			assert.JSONEq(t, tc.adf, string(b))
			assert.Equal(t, tc.md, jira.ADFToText(doc), "reads back as the same Markdown")
		})
	}
}

// A block that is not one of the supported constructs, or whose rich form
// would not read back exactly, is written as plain text, as before.
func TestMarkdownFallsBackToPlainText(t *testing.T) {
	cases := map[string]string{
		"unbalanced bold":                        "**bold without an end",
		"hash without a space":                   "#tag and #42",
		"seven hashes":                           "####### too deep",
		"nested list":                            "- a\n  - b",
		"star bullets":                           "* a\n* b",
		"heading followed by a line":             "## Title\nmore text",
		"fence that never closes":                "```go\nx := 1",
		"fence followed directly by text":        "```\ncode\n```\ntext",
		"fence with a space before the language": "``` go\ncode\n```",
		"raw HTML":                               "<details><summary>Log</summary>\n<b>bold</b>\n</details>",
		"numbers not in sequence":                "1. a\n3. b",
		"adjacent bold spans":                    "**a****b**",
		"bold that opens with code":              "**`x`** y",
		"underscores inside words":               "snake_case_name and file_name.go",
		"underscore opening inside a word":       "a_b c_ d",
		"underscore closing inside a word":       "_foo_bar",
		"link to a script":                       "[run](javascript:alert(1))",
		"link to a non-web address":              "[file](ftp://example.com/x)",
		"link without a host":                    "[x](https:///path)",
		"link whose text is its address":         "[https://x.example](https://x.example)",
		"empty list item":                        "- \n- b",
		"quote with an empty line":               "> a\n>\n> b",
		"empty code fence":                       "```\n```",
		"fence closed twice":                     "```\na\n```\nb\n```",
		"empty heading":                          "# ",
		"empty numbered item":                    "1. \n2. b",
		"number of ten digits":                   "1234567890. x",
		"double backticks":                       "``code``",
		"bold with spaces inside the stars":      "a ** b ** c",
		"link with parentheses in the address":   "[Go](https://en.wikipedia.org/wiki/Go_(programming_language))",
		"link with a space in the address":       "[x](https://example.com/a b)",
		"managed marker":                         "<!-- itervox:managed -->",
	}
	for name, md := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := json.Marshal(jira.TextToADF(md))
			require.NoError(t, err)
			plain, err := json.Marshal(jira.PlainTextToADF(md))
			require.NoError(t, err)

			if name == "bold that opens with code" {
				// Only the code span is rich; the stars stay text.
				assert.NotContains(t, string(got), `"strong"`)
				assert.Contains(t, string(got), `"code"`)
			} else {
				assert.JSONEq(t, string(plain), string(got), "written exactly as plain text")
			}
			// The reader trims the trailing space of "# ", as before.
			assert.Equal(t, strings.TrimRight(md, " "), jira.ADFToText(jira.TextToADF(md)), "and read back exactly")
		})
	}
}

// What a person writes in Jira's editor reads as Markdown, and nodes the
// reader does not know give their text or nothing, never a panic.
func TestADFToTextReadsRichTextAPersonWrote(t *testing.T) {
	text := func(s string, marks ...any) map[string]any {
		n := map[string]any{"type": "text", "text": s}
		if len(marks) > 0 {
			n["marks"] = marks
		}
		return n
	}
	mark := func(t string) map[string]any { return map[string]any{"type": t} }
	para := func(c ...any) map[string]any { return map[string]any{"type": "paragraph", "content": c} }
	item := func(c ...any) map[string]any { return map[string]any{"type": "listItem", "content": c} }
	doc := map[string]any{"type": "doc", "version": 1, "content": []any{
		map[string]any{"type": "heading", "attrs": map[string]any{"level": float64(3)}, "content": []any{text("Plan")}},
		// The editor splits styled text into nodes: bold, bold and italic, bold.
		para(text("bold ", mark("strong")), text("both", mark("strong"), mark("em")), text(" end", mark("strong")),
			text(" struck", mark("strike")), text(" "), text("red", map[string]any{"type": "textColor", "attrs": map[string]any{"color": "#ff0000"}})),
		map[string]any{"type": "bulletList", "content": []any{
			item(para(text("outer")), map[string]any{"type": "bulletList", "content": []any{item(para(text("inner")))}}),
		}},
		map[string]any{"type": "orderedList", "attrs": map[string]any{"order": float64(3)}, "content": []any{item(para(text("third"))), item(para(text("fourth")))}},
		map[string]any{"type": "blockquote", "content": []any{para(text("quoted")), para(text("again"))}},
		map[string]any{"type": "codeBlock", "attrs": map[string]any{"language": "java"}, "content": []any{text("int x;")}},
		map[string]any{"type": "rule"},
		map[string]any{"type": "table", "content": []any{
			map[string]any{"type": "tableRow", "content": []any{
				map[string]any{"type": "tableHeader", "content": []any{para(text("Name"))}},
				map[string]any{"type": "tableCell", "content": []any{para(text("Ann"))}},
			}},
		}},
		map[string]any{"type": "mediaSingle", "content": []any{map[string]any{"type": "media", "attrs": map[string]any{"id": "abc", "type": "file"}}}},
		para(map[string]any{"type": "status", "attrs": map[string]any{"text": "DONE"}}, text("after status")),
		map[string]any{"type": "panel", "content": []any{para(text("in a panel"))}},
	}}

	want := "### Plan\n\n" +
		"**bold _both_ end** struck red\n\n" +
		"- outer\n  - inner\n\n" +
		"3. third\n4. fourth\n\n" +
		"> quoted\n>\n> again\n\n" +
		"```java\nint x;\n```\n\n" +
		"---\n\n" +
		"Name\n\nAnn\n\n" +
		"after status\n\n" +
		"in a panel"
	assert.Equal(t, want, jira.ADFToText(doc))
}

func TestADFToTextSurvivesMalformedTrees(t *testing.T) {
	trees := []any{
		map[string]any{"type": "doc", "content": "not a list"},
		map[string]any{"type": "doc", "content": []any{nil, 42, "x", map[string]any{}}},
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": 7, "marks": "bad"}}},
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "x", "marks": []any{nil, "y", map[string]any{"type": "link", "attrs": "z"}}}}},
		map[string]any{"type": "heading", "attrs": map[string]any{"level": "two"}, "content": []any{map[string]any{"type": "text", "text": "h"}}},
		map[string]any{"type": "heading", "attrs": map[string]any{"level": float64(99)}, "content": []any{map[string]any{"type": "text", "text": "h"}}},
		map[string]any{"type": "orderedList", "attrs": map[string]any{"order": -5.5}, "content": []any{map[string]any{"type": "listItem"}}},
		map[string]any{"type": "codeBlock", "content": []any{map[string]any{"type": "hardBreak"}}},
	}
	for _, tree := range trees {
		assert.NotPanics(t, func() { _ = jira.ADFToText(tree) })
	}
	assert.Equal(t, "###### h", jira.ADFToText(trees[5]), "a level out of range is clamped")
}

// The comment path sends the rich document, not plain text.
func TestCreateCommentSendsMarkdownAsRichText(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, commentFixture("900", "2024-02-01T10:00:00.000+0000", "x"))
	})

	_, err := c.CreateComment(t.Context(), "PROJ-1", "## Done\n\n- **a**")

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 1)
	assert.JSONEq(t, `{"body":{"type":"doc","version":1,"content":[
		{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Done"}]},
		{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[
			{"type":"text","text":"a","marks":[{"type":"strong"}]}]}]}]}]}}`, reqs[0].Body)
}

// If Jira refuses the rich document (400), the text is sent once more as
// plain paragraphs, so formatting never costs a comment or an issue.
func TestRichTextRejectedByJiraIsSentAsPlainText(t *testing.T) {
	const md = "## Done\n\n**ok**"
	plain, err := json.Marshal(jira.PlainTextToADF(md))
	require.NoError(t, err)
	rejectRich := func(ok func(w http.ResponseWriter)) func(int, recorded, http.ResponseWriter) {
		return func(_ int, r recorded, w http.ResponseWriter) {
			if r.Method == "POST" && strings.Contains(r.Body, `"heading"`) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			ok(w)
		}
	}

	t.Run("comment", func(t *testing.T) {
		c, seen := newJiraServer(t, rejectRich(func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, commentFixture("900", "2024-02-01T10:00:00.000+0000", md))
		}))

		got, err := c.CreateComment(t.Context(), "PROJ-1", md)

		require.NoError(t, err)
		require.NotNil(t, got)
		reqs := seen()
		require.Len(t, reqs, 2, "the rich post, then the plain one")
		assert.JSONEq(t, `{"body":`+string(plain)+`}`, reqs[1].Body)
	})

	t.Run("issue description", func(t *testing.T) {
		c, seen := newJiraServer(t, rejectRich(func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"id": "10050", "key": "PROJ-13", "issues": []any{issueFixture("10050", "PROJ-13", "To Do")}})
		}))

		_, err := c.CreateIssue(t.Context(), "", tracker.NewIssue{Title: "t", Body: md})

		require.NoError(t, err)
		posts := slices.DeleteFunc(seen(), func(r recorded) bool { return r.Path != "/rest/api/3/issue" })
		require.Len(t, posts, 2, "the rich create, then the plain one")
		assert.JSONEq(t, string(plain), string(mustJSON(t, posts[1].jsonBody(t)["fields"].(map[string]any)["description"])))
	})

	t.Run("the plain retry of a keyed comment keeps the key", func(t *testing.T) {
		c, seen := newJiraServer(t, rejectRich(func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, commentFixture("901", "2024-02-01T10:00:00.000+0000", md))
		}))

		got, err := c.CreateCommentWithKey(t.Context(), "PROJ-1", "key-1", md)

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.True(t, tracker.CommentHasKey(got.Body, "key-1"))
		reqs := seen()
		require.Len(t, reqs, 2, "the rich post, then the plain one")
		const props = `[{"key":"itervox.commentKey","value":{"key":"key-1"}}]`
		for i, r := range reqs {
			assert.JSONEq(t, props, string(mustJSON(t, r.jsonBody(t)["properties"])), "request %d", i)
		}
		assert.JSONEq(t, string(plain), string(mustJSON(t, reqs[1].jsonBody(t)["body"])))
	})

	t.Run("plain text is not sent twice", func(t *testing.T) {
		c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) { w.WriteHeader(http.StatusBadRequest) })

		_, err := c.CreateComment(t.Context(), "PROJ-1", "no markdown here")

		require.Error(t, err)
		assert.Len(t, seen(), 1)
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
