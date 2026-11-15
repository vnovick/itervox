package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/mcpserver"
)

// The Claude Code plugin (plugins/itervox) tells an agent which itervox
// commands, flags and settings to use. These tests keep it honest: every
// `itervox <command> --flag` a skill or the plugin's MCP config names must
// exist in this CLI, every setting key must be one Itervox reads, and the
// plugin's version must be the newest release.

const pluginDir = "plugins/itervox"

// pluginSkills returns each skill's directory name and contents.
func pluginSkills(t *testing.T) map[string]string {
	t.Helper()
	dirs, err := os.ReadDir(filepath.Join("..", "..", pluginDir, "skills"))
	require.NoError(t, err)
	out := map[string]string{}
	for _, d := range dirs {
		if d.IsDir() {
			out[d.Name()] = repoFile(t, filepath.Join(pluginDir, "skills", d.Name(), "SKILL.md"))
		}
	}
	require.NotEmpty(t, out)
	return out
}

// cliModel is what the itervox CLI accepts, read from this package's source:
// the commands main dispatches on os.Args[1], and per command the flags its
// handlers register (or match literally, as "--json") anywhere in the
// functions they reach.
type cliModel struct {
	flags map[string]map[string]bool // command → flag names, without dashes
	words map[string]map[string]bool // command → string literals it reaches (nested subcommands)
	bare  map[string]bool            // flags of the daemon itself (`itervox --workflow …`)
}

var (
	flagLiteralRe = regexp.MustCompile(`^--?([a-z][a-z0-9-]*)$`)
	flagRegFuncs  = map[string]int{ // flag.FlagSet method → index of the name argument
		"String": 0, "Bool": 0, "Int": 0, "Int64": 0, "Uint": 0, "Float64": 0, "Duration": 0, "Func": 0, "BoolFunc": 0,
		"StringVar": 1, "BoolVar": 1, "IntVar": 1, "Int64Var": 1, "UintVar": 1, "Float64Var": 1, "DurationVar": 1, "Var": 1, "TextVar": 1,
	}
)

func loadCLIModel(t *testing.T) cliModel {
	t.Helper()
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var files []*ast.File
	consts := map[string]string{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		require.NoError(t, err, p)
		files = append(files, f)
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, s := range g.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if v, ok := stringLit(vs.Values[i]); ok {
							consts[n.Name] = v
						}
					}
				}
			}
		}
	}
	nameOf := func(e ast.Expr) (string, bool) {
		if v, ok := stringLit(e); ok {
			return v, true
		}
		if id, ok := e.(*ast.Ident); ok {
			v, ok := consts[id.Name]
			return v, ok
		}
		return "", false
	}

	// Per function: the flags it registers, the string literals in it, and
	// every identifier it mentions (calls and function values alike).
	type fn struct {
		flags, words, refs map[string]bool
	}
	funcs := map[string]*fn{}
	scan := func(body ast.Node) *fn {
		f := &fn{flags: map[string]bool{}, words: map[string]bool{}, refs: map[string]bool{}}
		ast.Inspect(body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
					if i, ok := flagRegFuncs[sel.Sel.Name]; ok && i < len(x.Args) {
						if name, ok := nameOf(x.Args[i]); ok {
							f.flags[name] = true
						}
					}
				}
			case *ast.BasicLit:
				if v, ok := stringLit(x); ok {
					f.words[v] = true
					if m := flagLiteralRe.FindStringSubmatch(v); m != nil {
						f.flags[m[1]] = true
					}
				}
			case *ast.Ident:
				f.refs[x.Name] = true
			case *ast.SelectorExpr:
				f.refs[x.Sel.Name] = true
			}
			return true
		})
		return f
	}
	var mainFn *ast.FuncDecl
	for _, file := range files {
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			got := scan(fd.Body)
			if prev, ok := funcs[fd.Name.Name]; ok { // methods and per-OS files share names: union
				for _, m := range []struct{ to, from map[string]bool }{{prev.flags, got.flags}, {prev.words, got.words}, {prev.refs, got.refs}} {
					for k := range m.from {
						m.to[k] = true
					}
				}
			} else {
				funcs[fd.Name.Name] = got
			}
			if fd.Name.Name == "main" && fd.Recv == nil {
				mainFn = fd
			}
		}
	}
	require.NotNil(t, mainFn, "func main")

	// closure collects what every function reachable from root registers.
	closure := func(root *fn) (flags, words map[string]bool) {
		flags, words = map[string]bool{}, map[string]bool{}
		seen := map[string]bool{}
		queue := []*fn{root}
		for len(queue) > 0 {
			f := queue[0]
			queue = queue[1:]
			for k := range f.flags {
				flags[k] = true
			}
			for k := range f.words {
				words[k] = true
			}
			for r := range f.refs {
				if g, ok := funcs[r]; ok && !seen[r] && r != "main" {
					seen[r] = true
					queue = append(queue, g)
				}
			}
		}
		return flags, words
	}

	m := cliModel{flags: map[string]map[string]bool{}, words: map[string]map[string]bool{}, bare: funcs["main"].flags}
	ast.Inspect(mainFn.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		ix, ok := sw.Tag.(*ast.IndexExpr)
		if !ok || exprString(ix) != "os.Args[1]" {
			return true
		}
		for _, s := range sw.Body.List {
			cc := s.(*ast.CaseClause)
			body := &ast.BlockStmt{List: cc.Body}
			flags, words := closure(scan(body))
			for _, e := range cc.List {
				if name, ok := stringLit(e); ok {
					m.flags[name], m.words[name] = flags, words
				}
			}
		}
		return false
	})
	require.Contains(t, m.flags, "mcp", "the os.Args[1] switch in main")
	return m
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.IndexExpr:
		return exprString(x.X) + "[" + exprString(x.Index) + "]"
	case *ast.BasicLit:
		return x.Value
	}
	return "?"
}

var (
	fenceRe      = regexp.MustCompile("(?ms)^```[^\n]*\n(.*?)^```")
	inlineCodeRe = regexp.MustCompile("`([^`\n]+)`")
	shellSplitRe = regexp.MustCompile("&&|\\|\\||;|\\s\\|\\s|\\$\\(|[()'\"`]") // a bare | is not a pipe: <linear|github>
	envPrefixRe  = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)+`)
	commandRe    = regexp.MustCompile(`^[a-z-][a-z0-9-]*$`)
	flagTokenRe  = regexp.MustCompile(`^-{1,2}([a-z][a-z0-9-]*)(?:=.*)?$`)
	wordTokenRe  = regexp.MustCompile(`^[a-z][a-z-]*$`)
)

// itervoxInvocations returns every shell command in the Markdown's code (fenced
// blocks and inline spans) that starts with `itervox`, as its words. A
// command starts a shell segment (after &&, ;, " | ", a quote, $( …), after any
// VAR=value prefixes, so `gcloud compute ssh itervox` or `sudo -u itervox`
// are not invocations.
func itervoxInvocations(md string) [][]string {
	var code []string
	for _, m := range fenceRe.FindAllStringSubmatch(md, -1) {
		code = append(code, strings.Split(m[1], "\n")...)
	}
	prose := fenceRe.ReplaceAllString(md, "")
	for _, m := range inlineCodeRe.FindAllStringSubmatch(prose, -1) {
		code = append(code, m[1])
	}
	var out [][]string
	for _, line := range code {
		for _, seg := range shellSplitRe.Split(line, -1) {
			seg = strings.TrimSpace(seg)
			seg = strings.TrimSpace(strings.TrimPrefix(seg, "$ "))
			seg = envPrefixRe.ReplaceAllString(seg, "")
			words := strings.Fields(seg)
			if len(words) == 0 || words[0] != "itervox" {
				continue
			}
			var inv []string
			for _, w := range words {
				if strings.HasPrefix(w, "#") { // a shell comment
					break
				}
				inv = append(inv, strings.Trim(w, "[]"))
			}
			out = append(out, inv)
		}
	}
	return out
}

// checkInvocation reports what in one `itervox …` invocation the CLI lacks.
func (m cliModel) checkInvocation(inv []string) []string {
	var problems []string
	args := inv[1:]
	known := m.bare
	cmd := "itervox"
	if len(args) > 0 {
		if f, ok := m.flags[args[0]]; ok {
			cmd, known, args = "itervox "+args[0], f, args[1:]
			if len(args) > 0 && wordTokenRe.MatchString(args[0]) && !m.words[strings.TrimPrefix(cmd, "itervox ")][args[0]] {
				problems = append(problems, cmd+" has no subcommand "+args[0])
			}
		} else if !strings.HasPrefix(args[0], "-") {
			if commandRe.MatchString(args[0]) {
				problems = append(problems, "no command itervox "+args[0])
			}
			return problems // a placeholder such as <command>
		}
	}
	for _, a := range args {
		if f := flagTokenRe.FindStringSubmatch(a); f != nil && !known[f[1]] {
			problems = append(problems, cmd+" has no flag "+a)
		}
	}
	return problems
}

// TestPluginSkillsNameRealCommandsAndFlags: every `itervox <command> --flag`
// in a plugin skill exists in this CLI (commands from main's os.Args[1]
// switch, flags from the FlagSets their handlers reach), the skills' front
// matter parses with the directory's name, the MCP config runs `itervox mcp`
// with real flags and a viewer default, and the operate skill names only
// real MCP tools.
func TestPluginSkillsNameRealCommandsAndFlags(t *testing.T) {
	cli := loadCLIModel(t)

	// The extractor itself: these must and must not be read as invocations.
	require.Equal(t, [][]string{{"itervox", "doctor", "--deploy", "--workflow", "WORKFLOW.md"}},
		itervoxInvocations("```bash\nsudo -H -u itervox bash -c 'cd /srv/itervox/x && itervox doctor --deploy --workflow WORKFLOW.md'\ngcloud compute ssh itervox --zone z\n```\n"))
	require.Equal(t, [][]string{{"itervox", "run", "--issue", "12", "--approve"}}, itervoxInvocations("`itervox run --issue 12 --approve \"vercel --prod\"`"))
	require.NotEmpty(t, cli.checkInvocation([]string{"itervox", "doctor", "--no-such-flag"}))
	require.NotEmpty(t, cli.checkInvocation([]string{"itervox", "no-such-command"}))
	require.NotEmpty(t, cli.checkInvocation([]string{"itervox", "config", "shwo"}))
	require.Empty(t, cli.checkInvocation([]string{"itervox", "mcp", "--token-scope", "operator"}))

	skills := pluginSkills(t)
	for _, want := range []string{"setup", "issue", "doctor", "ci", "deploy", "local-models", "automations", "operate"} {
		assert.Contains(t, skills, want)
	}
	checked := 0
	for name, skill := range skills {
		require.True(t, strings.HasPrefix(skill, "---\n"), name)
		end := strings.Index(skill[4:], "\n---\n")
		require.Positive(t, end, name)
		var meta struct{ Name, Description string }
		require.NoError(t, yaml.Unmarshal([]byte(skill[4:4+end]), &meta), name)
		assert.Equal(t, name, meta.Name, "front matter name of %s", name)
		assert.NotEmpty(t, meta.Description, name)
		assert.LessOrEqual(t, len(meta.Description), 1024, "%s description", name)

		for _, inv := range itervoxInvocations(skill) {
			checked++
			for _, p := range cli.checkInvocation(inv) {
				assert.Failf(t, "skill names an itervox command or flag the CLI does not have", "%s: %s (in `%s`)", name, p, strings.Join(inv, " "))
			}
		}
	}
	assert.Greater(t, checked, 40, "the skills' itervox invocations were found")

	// The MCP server the plugin bundles is `itervox mcp`, least privilege by
	// default.
	var mcp struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(repoFile(t, pluginDir+"/.mcp.json")), &mcp))
	srv, ok := mcp.MCPServers["itervox"]
	require.True(t, ok, ".mcp.json declares the itervox server")
	require.Equal(t, "itervox", srv.Command, ".mcp.json command")
	require.NotEmpty(t, srv.Args)
	require.Equal(t, "mcp", srv.Args[0], ".mcp.json runs `itervox mcp`")
	assert.Empty(t, cli.checkInvocation(append([]string{srv.Command}, srv.Args...)), "the MCP args are real flags")
	i := slices.Index(srv.Args, "--token-scope")
	require.GreaterOrEqual(t, i, 0)
	require.Less(t, i+1, len(srv.Args))
	scope := srv.Args[i+1]
	if strings.HasPrefix(scope, "${user_config.") {
		var manifest struct {
			UserConfig map[string]struct {
				Default any
				Options []string
			} `json:"userConfig"`
		}
		require.NoError(t, json.Unmarshal([]byte(repoFile(t, pluginDir+"/.claude-plugin/plugin.json")), &manifest))
		opt, ok := manifest.UserConfig[strings.TrimSuffix(strings.TrimPrefix(scope, "${user_config."), "}")]
		require.True(t, ok, "plugin.json declares %s", scope)
		scope, _ = opt.Default.(string)
		for _, o := range opt.Options {
			assert.Contains(t, []string{"viewer", "operator"}, o, "itervox mcp --token-scope takes viewer or operator")
		}
	}
	assert.Equal(t, "viewer", scope, "the plugin's MCP server defaults to the viewer scope")

	// The operate skill names the MCP tools that exist.
	operate := skills["operate"]
	for _, tool := range mcpserver.ToolNames("operator") {
		assert.Contains(t, operate, "`"+tool+"`", "itervox-operate covers %s", tool)
	}
	assert.Contains(t, operate, "Guardrail approvals stay human-only")
}

// settingKeyRe matches a dotted setting key in an inline code span.
var settingKeyRe = regexp.MustCompile(`^(agent|automations|dependencies|guardrails|hooks|hub|memory|polling|server|tracker|workspace)\.[a-z0-9_]+(?:\.[a-z0-9_]+)*$`)

// TestPluginSkillsNameRealSettings: every dotted setting key a skill names
// (`agent.turn_timeout_ms`, `dependencies.stacked_prs`, …) is one Itervox
// reads: a settings-schema key (config.SettingFields), a key kept in
// WORKFLOW.md (config.FrontMatterKeptKeys), or a key inside one of them; for
// the non-layered sections (guardrails, memory) each segment must be a key
// the config package spells.
func TestPluginSkillsNameRealSettings(t *testing.T) {
	keys := slices.Clone(config.FrontMatterKeptKeys)
	for _, f := range config.SettingFields {
		keys = append(keys, f.Key)
	}
	var configSrc strings.Builder
	paths, err := filepath.Glob(filepath.Join("..", "..", "internal", "config", "*.go"))
	require.NoError(t, err)
	for _, p := range paths {
		if !strings.HasSuffix(p, "_test.go") {
			b, err := os.ReadFile(p)
			require.NoError(t, err)
			configSrc.Write(b)
		}
	}
	known := func(k string) bool {
		for _, s := range keys {
			if k == s || strings.HasPrefix(k, s+".") || strings.HasPrefix(s, k+".") {
				return true
			}
		}
		section := strings.SplitN(k, ".", 2)[0]
		if slices.Contains(config.LayeredSections, section) {
			return false // a layered key must be in the schema
		}
		for _, seg := range strings.Split(k, ".") {
			if !strings.Contains(configSrc.String(), `"`+seg+`"`) && !strings.Contains(configSrc.String(), `yaml:"`+seg) {
				return false
			}
		}
		return true
	}
	require.False(t, known("agent.no_such_setting"))
	require.True(t, known("agent.backend_fallback"))

	checked := 0
	for name, skill := range pluginSkills(t) {
		for _, m := range inlineCodeRe.FindAllStringSubmatch(skill, -1) {
			k := strings.TrimSuffix(strings.Fields(m[1] + " ")[0], ":")
			if !settingKeyRe.MatchString(k) {
				continue
			}
			checked++
			assert.True(t, known(k), "%s names %s, which Itervox does not read", name, k)
		}
	}
	assert.Greater(t, checked, 15, "the skills' setting keys were found")
}

// TestPluginVersionMatchesLatestRelease: plugin.json's version is the newest
// dated release in CHANGELOG.md (the same rule as the deploy docs, see
// TestDeployDocsPinTheLatestRelease), the marketplace lists the plugin under
// its manifest name, and every action / binary version a skill pins is that
// release.
func TestPluginVersionMatchesLatestRelease(t *testing.T) {
	m := regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\] — \d{4}-\d{2}-\d{2}`).FindStringSubmatch(repoFile(t, "CHANGELOG.md"))
	require.NotNil(t, m)
	latest := m[1]

	var manifest struct{ Name, Version, License, Repository string }
	require.NoError(t, json.Unmarshal([]byte(repoFile(t, pluginDir+"/.claude-plugin/plugin.json")), &manifest))
	assert.Equal(t, "itervox", manifest.Name)
	assert.Equal(t, latest, manifest.Version, "plugin.json version; the latest release in CHANGELOG.md is %s", latest)
	assert.Equal(t, "Apache-2.0", manifest.License)
	assert.Contains(t, repoFile(t, "LICENSE"), "Apache License")
	assert.Equal(t, "https://github.com/vnovick/itervox", manifest.Repository)

	var market struct {
		Name    string
		Plugins []struct{ Name, Source, Version string }
	}
	require.NoError(t, json.Unmarshal([]byte(repoFile(t, ".claude-plugin/marketplace.json")), &market))
	assert.NotEmpty(t, market.Name)
	require.Len(t, market.Plugins, 1)
	assert.Equal(t, manifest.Name, market.Plugins[0].Name, "the install id is <entry name>@%s; keep it the manifest name", market.Name)
	assert.Equal(t, "./"+pluginDir, market.Plugins[0].Source)
	assert.Empty(t, market.Plugins[0].Version, "plugin.json carries the version (it wins over the entry's)")

	pinRe := regexp.MustCompile(`(?:vnovick/itervox/action@v|\bversion: )(\d+\.\d+\.\d+)`)
	pins := 0
	for name, skill := range pluginSkills(t) {
		for _, p := range pinRe.FindAllStringSubmatch(skill, -1) {
			pins++
			assert.Equal(t, latest, p[1], "%s pins %s; the latest release is %s", name, p[0], latest)
		}
	}
	assert.Positive(t, pins)
}
