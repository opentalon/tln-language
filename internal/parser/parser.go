package parser

import (
	"fmt"
	"strconv"

	"github.com/opentalon/tln-language/internal/ast"
	"github.com/opentalon/tln-language/internal/diagnostic"
	"github.com/opentalon/tln-language/internal/lexer"
)

type parser struct {
	file   string
	tokens []lexer.Token
	pos    int
	diags  diagnostic.List
}

func Parse(file string, tokens []lexer.Token) (*ast.Program, diagnostic.List) {
	p := &parser{file: file, tokens: tokens}
	return p.parseProgram(), p.diags
}

func (p *parser) parseProgram() *ast.Program {
	prog := &ast.Program{}
	// Imports must come before any block — that's how the resolver
	// knows when "the import header is done" without rescanning. We
	// loop here only as long as the next token is `import`; once the
	// first block starts, late `import` lines surface as parse errors
	// (caught in parseBlock with a targeted diagnostic).
	for p.at(lexer.TokenImport) {
		if imp, ok := p.parseImport(); ok {
			prog.Imports = append(prog.Imports, imp)
		}
	}
	for !p.at(lexer.TokenEOF) {
		b := p.parseBlock()
		if b != nil {
			prog.Blocks = append(prog.Blocks, b)
		}
	}
	return prog
}

// parseImport consumes one `import "./path"` directive. Returns the
// statement plus ok=true on success; (zero, false) on parse error so
// the caller can keep walking imports rather than fail-fast.
func (p *parser) parseImport() (ast.ImportStatement, bool) {
	tok := p.advance() // import
	if !p.at(lexer.TokenString) {
		p.errorf("expected import path string after `import`, got %q", p.peek().Value)
		return ast.ImportStatement{}, false
	}
	path := p.advance().Value
	return ast.ImportStatement{
		Pos:  ast.Pos{Line: tok.Line, Col: tok.Col},
		Path: path,
	}, true
}

// ─── Block dispatch ───────────────────────────────────────────────────────────

func (p *parser) parseBlock() ast.Block {
	switch p.peek().Type {
	case lexer.TokenImport:
		// Catch late `import` lines after a block has already started.
		// The header-only rule keeps the resolver simple and matches
		// Go's `import` placement.
		p.errorf("`import` must appear before any block in the file")
		p.synchronize()
		return nil
	case lexer.TokenDetect:
		return p.parseDetect()
	case lexer.TokenStrict, lexer.TokenRule:
		return p.parseRule()
	case lexer.TokenRecommend:
		return p.parseRecommend()
	case lexer.TokenCombine:
		return p.parseCombine()
	case lexer.TokenDefine:
		return p.parseDefine()
	case lexer.TokenWorkflow:
		return p.parseWorkflow()
	case lexer.TokenPredict:
		return p.parsePredictBlock()
	case lexer.TokenForecast:
		return p.parseForecastBlock()
	case lexer.TokenCluster:
		return p.parseClusterBlock()
	case lexer.TokenClassify:
		return p.parseClassifyBlock()
	case lexer.TokenFind:
		// `find similar ...` vs `find related ...`
		if p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenRelated {
			return p.parseRelatedBlock()
		}
		return p.parseSimilarBlock()
	case lexer.TokenOn:
		return p.parseOnBlock()
	case lexer.TokenConstraint:
		return p.parseConstraintBlock()
	case lexer.TokenEnrich:
		return p.parseEnrich()
	case lexer.TokenCollect:
		return p.parseCollect()
	case lexer.TokenStateMachine:
		return p.parseStateMachineBlock()
	case lexer.TokenThreshold:
		return p.parseThresholdBlock()
	case lexer.TokenConnector:
		return p.parseConnectorBlock()
	case lexer.TokenDerive:
		return p.parseDeriveBlock()
	case lexer.TokenModel:
		return p.parseModelBlock()
	case lexer.TokenModule:
		return p.parseModuleBlock()
	case lexer.TokenTest:
		return p.parseTestBlock()
	default:
		p.errorf("expected block keyword (detect, rule, recommend, ...), got %q", p.peek().Value)
		p.synchronize()
		return nil
	}
}

// ─── detect ───────────────────────────────────────────────────────────────────

func (p *parser) parseDetect() *ast.DetectBlock {
	tok := p.advance() // detect
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.DetectBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if !p.parseDetectClause(b) {
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseDetectClause(b *ast.DetectBlock) bool {
	switch p.peek().Type {
	case lexer.TokenFlag:
		b.Flag = p.parseFlagTarget()
	case lexer.TokenLabel:
		b.Label = p.parseLabelClause()
	case lexer.TokenPriority:
		pr := p.parsePriority()
		b.Priority = &pr
	case lexer.TokenConfidence:
		// Disambiguate the ML filter (`confidence >= N`) from the
		// provenance annotation (`confidence N`) by peeking at the
		// token right after `confidence`.
		if p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenGte {
			c := p.parseConfidenceClause()
			b.Confidence = &c
		} else {
			s := p.parseScoreAnnotation()
			b.Score = &s
		}
	case lexer.TokenSource:
		s := p.parseSourceAnnotation()
		b.Source = &s
	case lexer.TokenCalculate:
		b.Calculate = append(b.Calculate, p.parseCalculateClause())
	case lexer.TokenHaving:
		// Post-calculate filter: `having COND [and COND ...]`, may reference
		// calculate variables. Evaluated after the calculate steps. (`when`
		// in a detect block is reserved for sequence patterns.)
		p.advance() // having
		b.Having = append(b.Having, p.parseOrCondition())
	case lexer.TokenIs:
		// "is anomaly compared_to last N UNIT" — standalone anomaly clause
		b.Anomaly = p.parseAnomalyClause()
	case lexer.TokenPredict:
		b.Predict = p.parseNestedPredictClause()
	case lexer.TokenForecast:
		b.Forecast = p.parseNestedForecastClause()
	case lexer.TokenCluster:
		b.Cluster = p.parseNestedClusterClause()
	case lexer.TokenFind:
		if p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenRelated {
			b.Related = p.parseNestedRelatedClause()
		} else {
			b.Similar = p.parseNestedSimilarClause()
		}
	case lexer.TokenRecommend:
		rec := p.parseRecommend()
		b.Recommend = rec
	case lexer.TokenWhen:
		b.Pattern = p.parsePatternExpr()
	case lexer.TokenTune:
		b.Tune = p.parseTuneClause()
	case lexer.TokenRemediate:
		b.Remediate = p.parseRemediateClause()
	default:
		if p.atLoggerStatement() {
			if s := p.parseLoggerStatement(); s != nil {
				b.Loggers = append(b.Loggers, s)
			}
			return true
		}
		p.errorf("unexpected token %q inside detect block", p.peek().Value)
		return false
	}
	return true
}

// parseRemediateClause parses `remediate [mode] { [requires role "R"]
// [batch "B"] tool "s" "t" { ... } ... }`. Mode is auto | propose (default)
// | approve | queue.
func (p *parser) parseRemediateClause() *ast.RemediateClause {
	tok := p.advance() // remediate
	rc := &ast.RemediateClause{Pos: ast.Pos{Line: tok.Line, Col: tok.Col}, Mode: "propose"}
	if p.at(lexer.TokenIdent) {
		switch p.peek().Value {
		case "auto", "propose", "approve", "queue":
			rc.Mode = p.advance().Value
		default:
			p.errorf("unknown remediate mode %q (want auto / propose / approve / queue)", p.peek().Value)
		}
	}
	if !p.expect(lexer.TokenLBrace) {
		return rc
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch {
		case p.at(lexer.TokenRequires):
			// `requires role "manager"` — approver role for approve mode.
			p.advance()
			if p.at(lexer.TokenRole) {
				p.advance()
			}
			rc.Role = p.expectString()
		case p.at(lexer.TokenIdent) && p.peek().Value == "batch":
			// `batch "weekly-cleanup"` — queue name for queue mode.
			p.advance()
			rc.Batch = p.expectString()
		default:
			if act := p.parseActionStmt(); act != nil {
				rc.Body = append(rc.Body, act)
			}
		}
	}
	p.expect(lexer.TokenRBrace)
	return rc
}

// parseActionStmt parses one statement of an imperative action body: an MCP
// call, or one of the control-flow forms (if/else, for-each, while). Shared
// by remediate today; on / workflow bodies will reuse it. On an
// unrecognised token it reports an error and advances to guarantee progress.
func (p *parser) parseActionStmt() ast.Action {
	switch {
	case p.at(lexer.TokenIf):
		return p.parseIfAction()
	case p.at(lexer.TokenWhile):
		return p.parseWhileAction()
	case p.at(lexer.TokenFor) && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenEach:
		return p.parseForEachAction()
	case p.at(lexer.TokenTool):
		if call := p.parseMCPCall(); call != nil {
			return &ast.MCPAction{Call: call}
		}
		return nil
	default:
		p.errorf("unexpected token %q inside action body (expected tool / if / for each / while / requires role / batch)", p.peek().Value)
		p.advance()
		return nil
	}
}

// parseActionBody parses a `{ ActionStmt* }` block into an action list.
func (p *parser) parseActionBody() []ast.Action {
	if !p.expect(lexer.TokenLBrace) {
		return nil
	}
	var body []ast.Action
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if act := p.parseActionStmt(); act != nil {
			body = append(body, act)
		}
	}
	p.expect(lexer.TokenRBrace)
	return body
}

// parseIfAction parses `if <condition> { ... } [ else { ... } | else if ... ]`.
func (p *parser) parseIfAction() ast.Action {
	tok := p.advance() // if
	node := &ast.IfAction{Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	node.Cond = p.parseOrCondition()
	node.Then = p.parseActionBody()
	if p.at(lexer.TokenElse) {
		p.advance() // else
		if p.at(lexer.TokenIf) {
			// `else if` chains as a single nested IfAction.
			node.Else = []ast.Action{p.parseIfAction()}
		} else {
			node.Else = p.parseActionBody()
		}
	}
	return node
}

// parseForEachAction parses `for each <ident> in <expr> { ... }`. Unlike the
// define-block ForEachClause (whose body is conditions), this iterates an
// action body and its collection is any Expr, not just a bare identifier.
func (p *parser) parseForEachAction() ast.Action {
	tok := p.advance() // for
	p.advance()        // each
	variable := p.expectIdent()
	p.expect(lexer.TokenIn)
	over := p.parseExpr()
	body := p.parseActionBody()
	return &ast.ForEachAction{Pos: ast.Pos{Line: tok.Line, Col: tok.Col}, Variable: variable, Over: over, Body: body}
}

// parseWhileAction parses `while <condition> { ... }`. The iteration cap is
// implicit (ast.DefaultWhileMaxIter) — the runtime errors if it is hit.
func (p *parser) parseWhileAction() ast.Action {
	tok := p.advance() // while
	cond := p.parseOrCondition()
	body := p.parseActionBody()
	return &ast.WhileAction{Pos: ast.Pos{Line: tok.Line, Col: tok.Col}, Cond: cond, Body: body, MaxIter: ast.DefaultWhileMaxIter}
}

// atLoggerStatement reports whether the parser is positioned at a
// `logger.<level> "msg"` statement. Used to dispatch from any block
// body that supports per-row logging (detect, rule, recommend) or from
// an `on { ... }` action list — see parseLoggerStatement.
func (p *parser) atLoggerStatement() bool {
	return p.at(lexer.TokenIdent) && p.peek().Value == "logger"
}

// parseLoggerStatement parses the shared `logger.<level> "msg"` shape
// used both inside `on { }` blocks (as an OnAction) and inside detect /
// rule / recommend bodies (as a per-row side effect). The message
// template is pre-parsed via ast.ParseTemplate so the renderer can
// interpolate `{item.name}` / `{attr.x}` / `{count}` etc. per matched
// row — same template semantics as label / reason / suggest.
func (p *parser) parseLoggerStatement() *ast.LoggerAction {
	p.advance() // logger
	if !p.expect(lexer.TokenDot) {
		return nil
	}
	level := p.expectIdent()
	switch level {
	case "info", "warn", "error":
	default:
		p.errorf("expected logger level (info/warn/error), got %q", level)
		return nil
	}
	msg := p.expectString()
	return &ast.LoggerAction{Level: level, Message: ast.ParseTemplate(msg)}
}

// parseTuneClause parses `tune against test "NAME"`. The named test must be
// defined in a .tln.test file passed to `tln test` / `tln explain`;
// its given/expect data becomes the labeled fixture ABC tunes against.
func (p *parser) parseTuneClause() *ast.TuneClause {
	p.advance() // tune
	if !p.at(lexer.TokenAgainst) {
		p.errorf("expected 'against' after 'tune', got %q", p.peek().Value)
		return nil
	}
	p.advance()
	if !p.at(lexer.TokenTest) {
		p.errorf("expected 'test' after 'tune against', got %q", p.peek().Value)
		return nil
	}
	p.advance()
	name := p.expectString()
	return &ast.TuneClause{AgainstTest: name}
}

// ─── rule ─────────────────────────────────────────────────────────────────────

func (p *parser) parseRule() *ast.RuleBlock {
	strict := false
	if p.at(lexer.TokenStrict) {
		p.advance() // strict
		strict = true
	}
	if !p.at(lexer.TokenRule) {
		p.errorf("expected 'rule' after 'strict', got %q", p.peek().Value)
		p.synchronize()
		return nil
	}
	tok := p.advance() // rule
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.RuleBlock{Name: name, Strict: strict, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	// selector or when
	if p.at(lexer.TokenFor) {
		sel := p.parseSelector()
		b.Selector = &sel
	} else if p.at(lexer.TokenWhen) {
		b.When = p.parseWhenClause()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if !p.parseRuleClause(b) {
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseRuleClause(b *ast.RuleBlock) bool {
	switch p.peek().Type {
	case lexer.TokenEvery:
		b.Every = p.parseEveryClause()
	case lexer.TokenBefore:
		p.advance()
		s := p.expectString()
		b.Before = &s
	case lexer.TokenAfter:
		p.advance()
		s := p.expectString()
		b.After = &s
	case lexer.TokenBlock:
		p.advance() // block
		if p.at(lexer.TokenString) {
			s := p.advance().Value
			b.Block = &s
		}
		// optional inline "reason"
		if p.at(lexer.TokenReason) {
			b.Reason = p.parseLabelClause()
		}
	case lexer.TokenAllow:
		p.advance()
		s := p.expectString()
		b.Allow = &s
	case lexer.TokenRequires:
		b.Requires = p.parseRequiresClause()
	case lexer.TokenDo:
		if a := p.parseDoClause(); a != nil {
			b.Do = append(b.Do, a)
		}
	case lexer.TokenReason:
		b.Reason = p.parseLabelClause()
	case lexer.TokenPriority:
		pr := p.parsePriority()
		b.Priority = &pr
	case lexer.TokenOverrides:
		p.advance() // overrides
		b.Overrides = append(b.Overrides, p.expectString())
		for p.at(lexer.TokenComma) {
			p.advance()
			b.Overrides = append(b.Overrides, p.expectString())
		}
	case lexer.TokenConfidence:
		// On a rule block, only the provenance annotation form (`confidence N`)
		// is valid — the `>= N` filter form is meaningful only for ML
		// primitives. Reject the filter form explicitly so users see a
		// clear diagnostic rather than a silent parser quirk.
		if p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenGte {
			p.errorf("`confidence >= N` is an ML filter, only allowed inside detect/predict/classify; on rule blocks use the bare `confidence N` provenance form")
			return false
		}
		s := p.parseScoreAnnotation()
		b.Score = &s
	case lexer.TokenSource:
		s := p.parseSourceAnnotation()
		b.Source = &s
	default:
		if p.atLoggerStatement() {
			if s := p.parseLoggerStatement(); s != nil {
				b.Loggers = append(b.Loggers, s)
			}
			return true
		}
		p.errorf("unexpected token %q inside rule block", p.peek().Value)
		return false
	}
	return true
}

// ─── recommend ────────────────────────────────────────────────────────────────

func (p *parser) parseRecommend() *ast.RecommendBlock {
	tok := p.advance() // recommend
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.RecommendBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenWhen) {
		b.When = p.parseWhenClause()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenCalculate:
			b.Calculate = append(b.Calculate, p.parseCalculateClause())
		case lexer.TokenSuggest:
			// `suggest "<template>" [ with probability N
			//   [ learn from feedback within M days ] ]` —
			// the probability modifier gates firing via a seeded
			// RNG; the learn modifier turns the constant
			// probability into a Bayesian prior that observed
			// accept/reject feedback updates per-Run. See
			// docs/design/0005-mdp-feedback.md.
			p.advance() // suggest
			raw := p.expectString()
			tpl := ast.ParseTemplate(raw)
			b.Suggest = &tpl
			if p.at(lexer.TokenIdent) && p.peek().Value == "with" {
				p.advance() // with
				if !p.expect(lexer.TokenProb) {
					p.synchronize()
					continue
				}
				prob, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
				if prob < 0 || prob > 1 {
					p.errorf("recommend %q: probability must be in [0, 1], got %v", b.Name, prob)
					prob = 1
				}
				b.SuggestProbability = prob
				// Optional learn-from-feedback modifier.
				if p.at(lexer.TokenIdent) && p.peek().Value == "learn" {
					p.advance() // learn
					if p.at(lexer.TokenFrom) {
						p.advance() // from
					}
					if !p.at(lexer.TokenIdent) || p.peek().Value != "feedback" {
						p.errorf("recommend %q: expected 'feedback' after 'learn from'", b.Name)
					} else {
						p.advance() // feedback
					}
					if !p.expect(lexer.TokenWithin) {
						p.synchronize()
						continue
					}
					n, _ := strconv.Atoi(p.expectNumberStr())
					unit := p.expectDurationUnit()
					if unit != "days" {
						p.errorf("recommend %q: feedback window unit must be days, got %q", b.Name, unit)
					}
					b.FeedbackWindowDays = n
				}
			}
			continue
		case lexer.TokenPriority:
			pr := p.parsePriority()
			b.Priority = &pr
		case lexer.TokenRemediate:
			b.Remediate = p.parseRemediateClause()
		default:
			if p.atLoggerStatement() {
				if s := p.parseLoggerStatement(); s != nil {
					b.Loggers = append(b.Loggers, s)
				}
				continue
			}
			p.errorf("unexpected token %q inside recommend block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// ─── combine ──────────────────────────────────────────────────────────────────

func (p *parser) parseCombine() *ast.CombineBlock {
	tok := p.advance() // combine
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.CombineBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenMinimize, lexer.TokenMaximize:
			dir := p.advance().Value
			attr := p.parseExpr()
			b.Optimize = append(b.Optimize, ast.OptimizeClause{Direction: dir, Attr: attr})
		case lexer.TokenSelect:
			p.advance()
			n, _ := strconv.Atoi(p.expectNumberStr())
			// optional `from records`
			if p.at(lexer.TokenFrom) {
				p.advance()
				if p.at(lexer.TokenRecords) {
					p.advance()
				}
			}
			b.Select = &ast.SelectClause{Size: n}
		case lexer.TokenSubjectTo:
			tok := p.advance()
			left := p.parseExpr()
			op := p.parseCompareOp()
			right := p.parseExpr()
			b.Constraints = append(b.Constraints, ast.ConstraintClause{
				Pos:   ast.Pos{Line: tok.Line, Col: tok.Col},
				Left:  left,
				Op:    op,
				Right: right,
			})
		case lexer.TokenSeed:
			p.advance()
			n, _ := strconv.ParseInt(p.expectNumberStr(), 10, 64)
			b.Seed = &n
		case lexer.TokenSequence:
			p.advance()
			b.Sequence = true
		case lexer.TokenCoordinates:
			p.advance()
			x := p.parseExpr()
			p.expect(lexer.TokenComma)
			y := p.parseExpr()
			b.Coordinates = &ast.CoordinatesClause{X: x, Y: y}
		case lexer.TokenSolver:
			p.advance()
			if p.at(lexer.TokenLinear) {
				p.advance()
				b.Solver = "linear"
			} else {
				p.errorf("expected solver type (linear), got %q", p.peek().Value)
			}
		case lexer.TokenReturn:
			p.advance()
			b.Return = append(b.Return, p.expectIdent())
			for p.at(lexer.TokenComma) {
				p.advance()
				b.Return = append(b.Return, p.expectIdent())
			}
		case lexer.TokenLabel:
			b.Label = p.parseLabelClause()
		case lexer.TokenPriority:
			pr := p.parsePriority()
			b.Priority = &pr
		default:
			p.errorf("unexpected token %q inside combine block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parseCompareOp consumes one of ==, !=, <, <=, >, >= and returns the source
// form. Used by subject_to clauses where the LHS is an aggregate expression
// and the RHS is typically a numeric literal.
func (p *parser) parseCompareOp() string {
	switch p.peek().Type {
	case lexer.TokenEq:
		p.advance()
		return "=="
	case lexer.TokenNeq:
		p.advance()
		return "!="
	case lexer.TokenLt:
		p.advance()
		return "<"
	case lexer.TokenLte:
		p.advance()
		return "<="
	case lexer.TokenGt:
		p.advance()
		return ">"
	case lexer.TokenGte:
		p.advance()
		return ">="
	}
	p.errorf("expected comparison operator, got %q", p.peek().Value)
	return "=="
}

// ─── define ───────────────────────────────────────────────────────────────────

func (p *parser) parseDefine() *ast.DefineBlock {
	tok := p.advance() // define
	name := p.expectString()
	var params []string
	if p.at(lexer.TokenLParen) {
		p.advance()
		for !p.at(lexer.TokenRParen) && !p.at(lexer.TokenEOF) {
			params = append(params, p.expectIdent())
			if p.at(lexer.TokenComma) {
				p.advance()
			}
		}
		p.expect(lexer.TokenRParen)
	}
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.DefineBlock{Name: name, Params: params, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if p.at(lexer.TokenFor) && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenEach {
			b.ForEach = p.parseForEachClause()
		} else {
			cond := p.parseOrCondition()
			if cond != nil {
				b.Conditions = append(b.Conditions, cond)
			}
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// ─── workflow ─────────────────────────────────────────────────────────────────

func (p *parser) parseWorkflow() *ast.WorkflowBlock {
	tok := p.advance() // workflow
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.WorkflowBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if p.at(lexer.TokenStep) {
			b.Steps = append(b.Steps, p.parseWorkflowStep())
		} else {
			p.errorf("expected step inside workflow, got %q", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseWorkflowStep() ast.WorkflowStep {
	p.advance() // step
	name := p.expectString()
	var dependsOn []string
	if p.at(lexer.TokenDepends) {
		p.advance() // depends_on
		if p.at(lexer.TokenLBracket) {
			p.advance()
			for !p.at(lexer.TokenRBracket) && !p.at(lexer.TokenEOF) {
				dependsOn = append(dependsOn, p.expectString())
				if p.at(lexer.TokenComma) {
					p.advance()
				}
			}
			p.expect(lexer.TokenRBracket)
		} else {
			dependsOn = append(dependsOn, p.expectString())
		}
	}
	p.expect(lexer.TokenLBrace)
	step := ast.WorkflowStep{Name: name, DependsOn: dependsOn}
	// A step body is an MCP call plus an optional `when` guard (either order).
	// Multiple `when` lines AND together, matching how a define block's
	// conditions accumulate — so a guard can read like several plain lines.
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch {
		case p.at(lexer.TokenWhen):
			cond := p.parseWhenClause()
			if cond == nil {
				continue
			}
			if step.When == nil {
				step.When = cond
			} else {
				step.When = &ast.LogicalCondition{Op: "and", Left: step.When, Right: cond}
			}
		case p.at(lexer.TokenTool):
			step.MCPCall = p.parseMCPCall()
		default:
			p.errorf("expected `tool` call or `when` guard inside step, got %q", p.peek().Value)
			p.synchronizeInBlock()
			return step
		}
	}
	p.expect(lexer.TokenRBrace)
	return step
}

func (p *parser) parseMCPCall() *ast.MCPCall {
	p.advance() // tool
	server := p.expectString()
	tool := p.expectString()
	p.expect(lexer.TokenLBrace)
	call := &ast.MCPCall{Server: server, Tool: tool, Args: map[string]ast.Expr{}}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		// `on_error { ... }` is a resilience policy, not an arg.
		if p.at(lexer.TokenOnError) {
			call.OnError = p.parseOnErrorClause()
			continue
		}
		// Arg names are free-form and may collide with tln keywords
		// (e.g. `priority`, `status`, `type`). Consume one token as the
		// key unconditionally — this both accepts keyword-named args and
		// guarantees forward progress so a stray token can never spin the
		// loop.
		keyTok := p.advance()
		key := keyTok.Value
		if key == "" {
			p.errorf("expected tool argument name, got %q", keyTok.Value)
			continue
		}
		val := p.parseExpr()
		call.Args[key] = val
	}
	p.expect(lexer.TokenRBrace)
	return call
}

// parseOnErrorClause parses `on_error { [then] retry N times | log "msg" |
// skip | fail ... }`. `then` is optional sugar; actions run left-to-right.
func (p *parser) parseOnErrorClause() *ast.OnErrorClause {
	p.advance() // on_error
	oe := &ast.OnErrorClause{}
	if !p.expect(lexer.TokenLBrace) {
		return oe
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if p.at(lexer.TokenIdent) && p.peek().Value == "then" {
			p.advance() // optional connector
		}
		if p.at(lexer.TokenRBrace) || p.at(lexer.TokenEOF) {
			break
		}
		word := p.advance().Value
		switch word {
		case "retry":
			n, _ := strconv.Atoi(p.expectNumberStr())
			if p.at(lexer.TokenIdent) && p.peek().Value == "times" {
				p.advance()
			}
			oe.Actions = append(oe.Actions, &ast.RetryAction{Times: n})
		case "log":
			oe.Actions = append(oe.Actions, &ast.LogErrorAction{Message: ast.ParseTemplate(p.expectString())})
		case "skip":
			oe.Actions = append(oe.Actions, &ast.SkipAction{})
		case "fail":
			oe.Actions = append(oe.Actions, &ast.FailAction{})
		default:
			p.errorf("unexpected token %q inside on_error (expected retry / log / skip / fail)", word)
		}
	}
	p.expect(lexer.TokenRBrace)
	return oe
}

// ─── enrich ─────────────────────────────────────────────────────────────────

func (p *parser) parseEnrich() *ast.EnrichBlock {
	tok := p.advance() // enrich
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.EnrichBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	} else {
		p.errorf("enrich %q: expected 'for records where ...' selector", name)
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenStaleAfter:
			p.advance()
			b.StaleAfter = p.parseDuration()
		case lexer.TokenTool:
			b.Call = p.parseMCPCall()
		case lexer.TokenUpdate:
			b.Updates = append(b.Updates, p.parseUpdateClause())
		default:
			p.errorf("unexpected token %q inside enrich block (expected stale_after / tool / update)", p.peek().Value)
			p.advance()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parseUpdateClause parses `update attr "x" from result.field[.sub...]`.
func (p *parser) parseUpdateClause() ast.UpdateClause {
	p.advance() // update
	p.expect(lexer.TokenAttr)
	attr := p.expectString()
	p.expect(lexer.TokenFrom)
	var path string
	if p.at(lexer.TokenIdent) && p.peek().Value == "result" {
		p.advance()
		for p.at(lexer.TokenDot) {
			p.advance()
			seg := p.expectIdent()
			if path == "" {
				path = seg
			} else {
				path += "." + seg
			}
		}
	}
	if path == "" {
		p.errorf("update attr %q: expected 'result.<field>' after 'from', got %q", attr, p.peek().Value)
	}
	return ast.UpdateClause{Attr: attr, ResultPath: path}
}

// ─── collect ────────────────────────────────────────────────────────────────

func (p *parser) parseCollect() *ast.CollectBlock {
	tok := p.advance() // collect
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.CollectBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenSchedule:
			p.advance()
			b.Schedule = p.parseScheduleExpr()
		case lexer.TokenTool:
			b.Call = p.parseMCPCall()
		case lexer.TokenStore:
			p.parseCollectStore(b)
		default:
			p.errorf("unexpected token %q inside collect block (expected schedule / tool / store)", p.peek().Value)
			p.advance()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parseScheduleExpr reads the schedule metadata: `weekly` | `daily` |
// `hourly` | `every N hours|days` | `cron "<expr>"`. tln does not
// interpret it — it's a string a host scheduler reads.
func (p *parser) parseScheduleExpr() string {
	if p.at(lexer.TokenString) {
		return p.expectString()
	}
	if p.at(lexer.TokenEvery) {
		p.advance()
		n := p.expectNumberStr()
		unit := ""
		if p.at(lexer.TokenIdent) || p.at(lexer.TokenDays) {
			unit = p.advance().Value // hours / days / minutes / ...
		}
		return "every " + n + " " + unit
	}
	if p.at(lexer.TokenIdent) {
		word := p.advance().Value
		if word == "cron" {
			return "cron:" + p.expectString()
		}
		return word // weekly / daily / hourly
	}
	p.errorf("expected schedule (weekly/daily/hourly/every N.../cron \"...\"), got %q", p.peek().Value)
	return ""
}

// parseCollectStore parses `store results as <ident> [tag "<string>"]`.
func (p *parser) parseCollectStore(b *ast.CollectBlock) {
	p.advance() // store
	if p.at(lexer.TokenIdent) && p.peek().Value == "results" {
		p.advance()
	}
	if p.at(lexer.TokenIdent) && p.peek().Value == "as" {
		p.advance()
	}
	b.StoreAs = p.expectIdent()
	if p.at(lexer.TokenIdent) && p.peek().Value == "tag" {
		p.advance()
		b.Tag = p.expectString()
	}
}

// ─── on (reactive) ────────────────────────────────────────────────────────────

func (p *parser) parseOnBlock() *ast.OnBlock {
	tok := p.advance() // on
	b := &ast.OnBlock{Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}

	// Trigger keyword: change | assert | retract (matched as idents).
	if !p.at(lexer.TokenIdent) {
		p.errorf("expected 'change', 'assert', or 'retract' after 'on', got %q", p.peek().Value)
		p.synchronize()
		return nil
	}
	switch p.peek().Value {
	case "change":
		p.advance()
		b.Trigger = "change"
		if !p.at(lexer.TokenAttr) {
			p.errorf("expected 'attr' after 'on change', got %q", p.peek().Value)
			p.synchronize()
			return nil
		}
		p.advance() // attr
		b.Attr = p.expectString()
		if p.at(lexer.TokenIdent) && p.peek().Value == "to" {
			p.advance()
			b.ToValue = p.parseExpr()
		}
		b.Name = fmt.Sprintf("on change attr %q", b.Attr)
	case "assert":
		p.advance()
		b.Trigger = "assert"
		b.FactType = p.expectIdent()
		b.Name = fmt.Sprintf("on assert %s", b.FactType)
	case "retract":
		p.advance()
		b.Trigger = "retract"
		b.FactType = p.expectIdent()
		b.Name = fmt.Sprintf("on retract %s", b.FactType)
	default:
		p.errorf("expected 'change', 'assert', or 'retract' after 'on', got %q", p.peek().Value)
		p.synchronize()
		return nil
	}

	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}

	if p.at(lexer.TokenWhen) {
		b.When = p.parseWhenClause()
	}

	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		act := p.parseOnAction()
		if act != nil {
			b.Actions = append(b.Actions, act)
		} else {
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseOnAction() ast.OnAction {
	// logger.info|warn|error "msg" — the shared helper also handles the
	// per-block-body form used by detect / rule / recommend.
	if p.atLoggerStatement() {
		return p.parseLoggerStatement()
	}
	// recommend "Name" / detect "Name" / workflow "Name" — reference to
	// another block by name.
	switch p.peek().Type {
	case lexer.TokenRecommend:
		p.advance()
		return &ast.BlockRefAction{Kind: "recommend", Name: p.expectString()}
	case lexer.TokenDetect:
		p.advance()
		return &ast.BlockRefAction{Kind: "detect", Name: p.expectString()}
	case lexer.TokenWorkflow:
		p.advance()
		return &ast.BlockRefAction{Kind: "workflow", Name: p.expectString()}
	}
	p.errorf("unexpected token %q inside on block (expected logger / recommend / detect / workflow)", p.peek().Value)
	return nil
}

// ─── constraint ───────────────────────────────────────────────────────────────

func (p *parser) parseConstraintBlock() *ast.ConstraintBlock {
	tok := p.advance() // constraint
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.ConstraintBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}

	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	} else {
		p.errorf("constraint %q: expected 'for records where ...' selector", name)
	}

	if p.at(lexer.TokenRequire) {
		p.advance()
		b.Require = p.parseOrCondition()
	} else {
		p.errorf("constraint %q: expected 'require' clause", name)
	}

	// on_violation MODE [STRING]
	if p.at(lexer.TokenIdent) && p.peek().Value == "on_violation" {
		p.advance()
		modeTok := p.peek().Value
		switch modeTok {
		case "reject", "warn", "quarantine":
			p.advance()
			b.OnViolation.Mode = modeTok
		default:
			p.errorf("expected violation mode (reject/warn/quarantine), got %q", modeTok)
			b.OnViolation.Mode = "reject"
		}
		if p.at(lexer.TokenString) {
			b.OnViolation.Message = p.advance().Value
		}
	} else {
		p.errorf("constraint %q: expected 'on_violation' clause", name)
		b.OnViolation.Mode = "reject"
	}

	p.expect(lexer.TokenRBrace)
	return b
}

// ─── state_machine ────────────────────────────────────────────────────────────

// parseStateMachineBlock reads a finite-state machine declaration:
//
//	state_machine "Order lifecycle" {
//	  for records where type == "order"
//	  states pending, approved, shipped, delivered, cancelled
//	  initial pending
//	  state_attr "lifecycle_state"   ; optional, default ":record/state"
//	  transition pending -> approved when attr "amount" > 1000
//	  transition pending -> cancelled when older_than 7 days
//	  invariant in shipped require has "tracking_number"
//	}
//
// State names are bare identifiers; transitions name them on both
// sides of `->`. Guards reuse the same condition grammar `rule` /
// `detect` use, so any `when` expression that fits elsewhere fits
// here. Invariants attach to a state name and run while an entity
// is in that state — warning-level enforcement, not reject.
func (p *parser) parseStateMachineBlock() *ast.StateMachineBlock {
	tok := p.advance() // state_machine
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.StateMachineBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}

	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	} else {
		p.errorf("state_machine %q: expected 'for records where ...' selector", name)
	}

	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenStates:
			p.advance()
			for {
				if !p.at(lexer.TokenIdent) {
					p.errorf("state_machine %q: expected state name, got %q", name, p.peek().Value)
					break
				}
				decl := ast.StateDecl{Name: p.advance().Value}
				// Optional substate suffix: `parent / child`. The
				// lexer emits IDENT SLASH IDENT for `parent/child`
				// since `/` isn't allowed in identifiers.
				if p.at(lexer.TokenSlash) {
					p.advance() // /
					if !p.at(lexer.TokenIdent) {
						p.errorf("state_machine %q: expected child name after %q/", name, decl.Name)
					} else {
						child := p.advance().Value
						decl.Parent = decl.Name
						decl.Name = decl.Parent + "/" + child
					}
				}
				b.States = append(b.States, decl)
				if !p.at(lexer.TokenComma) {
					break
				}
				p.advance() // ,
			}
		case lexer.TokenInitial:
			p.advance()
			if p.at(lexer.TokenIdent) {
				b.Initial = p.advance().Value
			} else {
				p.errorf("state_machine %q: expected initial state name", name)
			}
		case lexer.TokenStateAttr:
			p.advance()
			b.StateAttr = p.expectString()
		case lexer.TokenTransition:
			tp := p.advance()
			t := ast.Transition{Pos: ast.Pos{Line: tp.Line, Col: tp.Col}}
			t.From = parseStateRef(p)
			if t.From == "" {
				p.errorf("transition: expected source state, got %q", p.peek().Value)
			}
			if !p.expect(lexer.TokenArrow) {
				p.synchronize()
				continue
			}
			t.To = parseStateRef(p)
			if t.To == "" {
				p.errorf("transition: expected target state, got %q", p.peek().Value)
			}
			if p.at(lexer.TokenWhen) {
				p.advance()
				t.When = p.parseOrCondition()
			}
			b.Transitions = append(b.Transitions, t)
		case lexer.TokenInvariant:
			p.advance()
			if !p.expect(lexer.TokenIn) {
				p.synchronize()
				continue
			}
			if !p.at(lexer.TokenIdent) {
				p.errorf("invariant: expected state name, got %q", p.peek().Value)
				continue
			}
			state := p.advance().Value
			if !p.expect(lexer.TokenRequire) {
				p.synchronize()
				continue
			}
			cond := p.parseOrCondition()
			b.Invariants = append(b.Invariants, ast.StateInvariant{State: state, Required: cond})
		default:
			p.errorf("state_machine %q: unexpected token %q (expected states/initial/state_attr/transition/invariant)", name, p.peek().Value)
			p.advance()
		}
	}

	p.expect(lexer.TokenRBrace)
	return b
}

// ─── threshold (cached) ───────────────────────────────────────────────────────

// parseThresholdBlock reads a host-precomputed cached threshold:
//
//	threshold "service_interval" {
//	  value 18200
//	  computed_from "47 service tickets, avg 20222 km, margin 0.9"
//	  valid_until "2025-05-13"
//	}
//
// `value` is required; `computed_from` and `valid_until` are optional
// provenance. `value` is matched as an identifier (not reserved) so it can't
// collide with attribute names elsewhere.
func (p *parser) parseThresholdBlock() *ast.ThresholdBlock {
	tok := p.advance() // threshold
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.ThresholdBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	hasValue := false
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch {
		case p.at(lexer.TokenIdent) && p.peek().Value == "value":
			p.advance()
			b.Value, _ = strconv.ParseFloat(p.expectNumberStr(), 64)
			hasValue = true
		case p.at(lexer.TokenComputedFrom):
			p.advance()
			b.ComputedFrom = p.expectString()
		case p.at(lexer.TokenValidUntil):
			p.advance()
			b.ValidUntil = p.expectString()
		default:
			p.errorf("unexpected token %q inside threshold block (expected value / computed_from / valid_until)", p.peek().Value)
			p.advance()
		}
	}
	if !hasValue {
		p.errorf("threshold %q requires a 'value' clause", name)
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parseConnectorBlock reads `connector "name" via <plugin> { key Expr … }`
// (ADR 0012): a server name bound to a backing plugin, with free-form
// plugin-specific config values (typically env "VAR" for creds/endpoints).
func (p *parser) parseConnectorBlock() *ast.ConnectorBlock {
	tok := p.advance() // connector
	name := p.expectString()
	if !p.expect(lexer.TokenVia) {
		p.synchronize()
		return nil
	}
	plugin := p.advance().Value // bare plugin name: mcp, io, …
	if plugin == "" {
		p.errorf("connector %q: expected a plugin name after 'via'", name)
	}
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.ConnectorBlock{
		Name:   name,
		Plugin: plugin,
		Config: map[string]ast.Expr{},
		Pos:    ast.Pos{Line: tok.Line, Col: tok.Col},
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		// Config keys are free-form (plugin-interpreted), so consume one token
		// as the key unconditionally — this both accepts keyword-named keys and
		// guarantees forward progress.
		key := p.advance().Value
		if key == "" {
			p.errorf("connector %q: expected a config key", name)
			continue
		}
		// `env "VAR"` is valid ONLY here, in connector config — it resolves a
		// credential/endpoint from the environment at run time. Everything else
		// is an ordinary literal (path "…", stream "stderr", …).
		if p.at(lexer.TokenEnv) {
			p.advance()
			b.Config[key] = &ast.EnvExpr{Name: p.expectString()}
		} else {
			b.Config[key] = p.parseExpr()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// ─── derive (derived predicates) ──────────────────────────────────────────────

// parseDeriveBlock reads a Datalog-style derived predicate:
//
//	derive overdue(v) {
//	  for records where type == "vehicle"
//	    and attr "km" > attr "last_service_km" + 20000
//	}
//
// The head is a predicate name + one variable (arity 1); the body is the
// existing selector grammar. The predicate is then referenceable as
// `overdue(v)` in any other block's conditions.
func (p *parser) parseDeriveBlock() *ast.DeriveBlock {
	tok := p.advance() // derive
	name := p.expectIdent()
	b := &ast.DeriveBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.expect(lexer.TokenLParen) {
		b.Var = p.expectIdent()
		p.expect(lexer.TokenRParen)
	}
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	} else {
		p.errorf("derive %q: expected 'for records where ...' body", name)
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parsePredicateCall reads `name(var)` in a condition position — a reference to
// a derived predicate. Only reached when an IDENT is immediately followed by
// `(`, which no other condition form uses.
func (p *parser) parsePredicateCall() ast.Condition {
	name := p.expectIdent()
	p.expect(lexer.TokenLParen)
	v := p.expectIdent()
	p.expect(lexer.TokenRParen)
	return &ast.PredicateCallCondition{Name: name, Var: v}
}

// ─── top-level ML blocks ──────────────────────────────────────────────────────

func (p *parser) parsePredictBlock() *ast.PredictBlock {
	tok := p.advance() // predict
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.PredictBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenFeatures:
			b.Features = p.parseFeaturesClause()
		case lexer.TokenTrainedOn:
			b.TrainedOn = p.parseTrainedOnClause()
		case lexer.TokenUsing:
			// `using model "ns.name"` — predict from a model's inline fitted
			// tree instead of training on a trained_on query.
			p.advance() // using
			if p.at(lexer.TokenModel) {
				p.advance()
			} else {
				p.errorf("expected 'model' after 'using', got %q", p.peek().Value)
			}
			b.UsingModel = p.expectString()
		case lexer.TokenLabelAttr:
			p.advance() // label_attr
			b.LabelAttr = p.expectString()
		case lexer.TokenConfidence:
			c := p.parseConfidenceClause()
			b.Confidence = &c
		case lexer.TokenLabel:
			b.Label = p.parseLabelClause()
		case lexer.TokenPriority:
			pr := p.parsePriority()
			b.Priority = &pr
		default:
			p.errorf("unexpected token %q inside predict block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseForecastBlock() *ast.ForecastBlock {
	tok := p.advance() // forecast
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.ForecastBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenSeries:
			b.Series = p.parseSeriesClause()
		case lexer.TokenPredict:
			b.Predict = p.parseForecastPredictClause()
		case lexer.TokenWhen:
			b.When = p.parseWhenClause()
		case lexer.TokenLabel:
			b.Label = p.parseLabelClause()
		case lexer.TokenPriority:
			pr := p.parsePriority()
			b.Priority = &pr
		default:
			p.errorf("unexpected token %q inside forecast block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseClusterBlock() *ast.ClusterBlock {
	tok := p.advance() // cluster
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.ClusterBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenIdent:
			if p.peek().Value == "by" {
				p.advance() // by
				b.ByAttrs = p.parseAttrList()
			} else {
				p.errorf("unexpected %q inside cluster block", p.peek().Value)
				p.synchronizeInBlock()
			}
		case lexer.TokenLabel:
			b.Label = p.parseLabelClause()
		case lexer.TokenPriority:
			pr := p.parsePriority()
			b.Priority = &pr
		default:
			p.errorf("unexpected token %q inside cluster block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseClassifyBlock() *ast.ClassifyBlock {
	tok := p.advance() // classify
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.ClassifyBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenFeatures:
			b.Features = p.parseFeaturesClause()
		case lexer.TokenTrainedOn:
			b.TrainedOn = p.parseTrainedOnClause()
		case lexer.TokenUsing:
			// `using model "ns.name"` — draw the training set from a named
			// model's inline fitted examples instead of trained_on.
			p.advance() // using
			if p.at(lexer.TokenModel) {
				p.advance()
			} else {
				p.errorf("expected 'model' after 'using', got %q", p.peek().Value)
			}
			b.UsingModel = p.expectString()
		case lexer.TokenLabelAttr:
			p.advance() // label_attr
			b.LabelAttr = p.expectString()
		case lexer.TokenConfidence:
			c := p.parseConfidenceClause()
			b.Confidence = &c
		case lexer.TokenLabel:
			b.Label = p.parseLabelClause()
		case lexer.TokenPriority:
			pr := p.parsePriority()
			b.Priority = &pr
		default:
			p.errorf("unexpected token %q inside classify block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parseModelBlock reads a `model "name" { ... }` block with inline fitted
// params. v1 supports the kNN classifier: `classify knn [k N]`, a features
// list, and a `fitted { example [...] label "..." }` set.
func (p *parser) parseModelBlock() *ast.ModelBlock {
	tok := p.advance() // model
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.ModelBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}, K: 5}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch {
		case p.at(lexer.TokenClassify):
			p.advance() // classify
			algo := p.expectIdent()
			if algo != "knn" {
				p.errorf("unsupported classify algorithm %q (supports: knn)", algo)
			}
			b.Algo = "classify_knn"
			if p.at(lexer.TokenIdent) && p.peek().Value == "k" {
				p.advance()
				n, _ := strconv.Atoi(p.expectNumberStr())
				b.K = n
			}
		case p.at(lexer.TokenPredict):
			p.advance() // predict
			algo := p.expectIdent()
			if algo != "tree" {
				p.errorf("unsupported predict algorithm %q (supports: tree)", algo)
			}
			b.Algo = "predict_decision_tree"
		case p.at(lexer.TokenFeatures):
			b.Features = p.parseFeaturesClause()
		case p.at(lexer.TokenFitted):
			// `fitted tree { node ... }` (decision tree) vs `fitted { example
			// ... }` (kNN labeled points).
			if p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenIdent && p.tokens[p.pos+1].Value == "tree" {
				b.Tree = p.parseFittedTree()
			} else {
				b.Examples = p.parseFittedClause()
			}
		case p.at(lexer.TokenComputedFrom):
			p.advance()
			b.ComputedFrom = p.expectString()
		case p.at(lexer.TokenValidUntil):
			p.advance()
			b.ValidUntil = p.expectString()
		default:
			p.errorf("unexpected token %q inside model block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parseFittedClause reads `fitted { example [n, n, ...] label "x" ... }`.
func (p *parser) parseFittedClause() []ast.FittedExample {
	p.advance() // fitted
	if !p.expect(lexer.TokenLBrace) {
		return nil
	}
	var out []ast.FittedExample
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if !p.at(lexer.TokenExample) {
			p.errorf("expected 'example' inside fitted block, got %q", p.peek().Value)
			p.synchronizeInBlock()
			break
		}
		p.advance() // example
		p.expect(lexer.TokenLBracket)
		var feats []float64
		for !p.at(lexer.TokenRBracket) && !p.at(lexer.TokenEOF) {
			n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
			feats = append(feats, n)
			if p.at(lexer.TokenComma) {
				p.advance()
			}
		}
		p.expect(lexer.TokenRBracket)
		label := ""
		if p.at(lexer.TokenLabel) {
			p.advance()
			label = p.expectString()
		} else {
			p.errorf("fitted example requires a `label \"...\"`")
		}
		out = append(out, ast.FittedExample{Features: feats, Label: label})
	}
	p.expect(lexer.TokenRBrace)
	return out
}

// parseFittedTree reads `fitted tree { node N (leaf "C" [P] | split "F" T
// left L right R) ... }` — the inline serialised decision tree.
func (p *parser) parseFittedTree() []ast.TreeNode {
	p.advance() // fitted
	p.advance() // tree
	if !p.expect(lexer.TokenLBrace) {
		return nil
	}
	var out []ast.TreeNode
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if !p.at(lexer.TokenIdent) || p.peek().Value != "node" {
			p.errorf("expected 'node' in fitted tree, got %q", p.peek().Value)
			p.synchronizeInBlock()
			break
		}
		p.advance() // node
		idx, _ := strconv.Atoi(p.expectNumberStr())
		n := ast.TreeNode{Index: idx}
		switch {
		case p.at(lexer.TokenIdent) && p.peek().Value == "leaf":
			p.advance()
			n.Leaf = true
			n.Class = p.expectString()
			if p.at(lexer.TokenNumber) {
				n.Purity, _ = strconv.ParseFloat(p.advance().Value, 64)
			} else {
				n.Purity = 1.0
			}
		case p.at(lexer.TokenIdent) && p.peek().Value == "split":
			p.advance()
			n.Feature = p.expectString()
			n.Threshold, _ = strconv.ParseFloat(p.expectNumberStr(), 64)
			if p.at(lexer.TokenIdent) && p.peek().Value == "left" {
				p.advance()
			}
			n.Left, _ = strconv.Atoi(p.expectNumberStr())
			if p.at(lexer.TokenIdent) && p.peek().Value == "right" {
				p.advance()
			}
			n.Right, _ = strconv.Atoi(p.expectNumberStr())
		default:
			p.errorf("expected 'leaf' or 'split' after node %d, got %q", idx, p.peek().Value)
		}
		out = append(out, n)
	}
	p.expect(lexer.TokenRBrace)
	return out
}

// parseModuleBlock reads `module "ns" { export <block> ... }`. Each exported
// member is namespaced under ns (see ast.ModuleBlock.QualifiedName).
func (p *parser) parseModuleBlock() *ast.ModuleBlock {
	tok := p.advance() // module
	ns := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.ModuleBlock{Namespace: ns, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if !p.at(lexer.TokenExport) {
			p.errorf("module body only contains `export <block>`, got %q", p.peek().Value)
			p.synchronizeInBlock()
			continue
		}
		p.advance() // export
		member := p.parseBlock()
		if member != nil {
			b.Members = append(b.Members, member)
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseSimilarBlock() *ast.SimilarBlock {
	tok := p.advance() // find
	// expect "similar"
	if p.peek().Type == lexer.TokenSimilar {
		p.advance()
	} else {
		p.errorf("expected 'similar' after 'find', got %q", p.peek().Value)
	}
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.SimilarBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenIdent:
			if p.peek().Value == "to" {
				p.advance()
				b.To = p.parseExpr()
			} else if p.peek().Value == "within" {
				p.advance()
				n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
				b.Within = &n
			} else if p.peek().Value == "top" {
				// `top N` — caps the vector-similar result count.
				p.advance()
				k, _ := strconv.Atoi(p.expectNumberStr())
				b.TopK = &k
			} else {
				p.errorf("unexpected %q inside find similar block", p.peek().Value)
				p.synchronizeInBlock()
			}
		case lexer.TokenUsing:
			// `using vector scope "X"` — route through the HNSW vector
			// index rather than the structured-attribute cosine path.
			p.advance()
			if !p.at(lexer.TokenIdent) || p.peek().Value != "vector" {
				p.errorf("expected 'vector' after 'using', got %q", p.peek().Value)
				p.synchronizeInBlock()
				continue
			}
			p.advance()
			if !p.at(lexer.TokenIdent) || p.peek().Value != "scope" {
				p.errorf("expected 'scope' after 'using vector', got %q", p.peek().Value)
				p.synchronizeInBlock()
				continue
			}
			p.advance()
			b.VectorScope = p.expectString()
		case lexer.TokenWithin:
			p.advance()
			n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
			b.Within = &n
		case lexer.TokenLabel:
			b.Label = p.parseLabelClause()
		case lexer.TokenPriority:
			pr := p.parsePriority()
			b.Priority = &pr
		default:
			p.errorf("unexpected token %q inside find similar block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

func (p *parser) parseRelatedBlock() *ast.RelatedBlock {
	tok := p.advance() // find
	if p.at(lexer.TokenRelated) {
		p.advance()
	} else {
		p.errorf("expected 'related' after 'find', got %q", p.peek().Value)
	}
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.RelatedBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	if p.at(lexer.TokenFor) {
		b.Selector = p.parseSelector()
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		if !p.parseRelatedBody(b) {
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parseRelatedBody consumes one clause inside a find-related block.
// Returns false on unrecognised tokens so the outer loop can recover.
func (p *parser) parseRelatedBody(b *ast.RelatedBlock) bool {
	switch p.peek().Type {
	case lexer.TokenIdent:
		switch p.peek().Value {
		case "to":
			p.advance()
			b.To = p.parseExpr()
		case "seeds":
			p.advance()
			b.Seeds = p.parseSeedList()
		case "top_k":
			p.advance()
			n, _ := strconv.Atoi(p.expectNumberStr())
			b.TopK = &n
		case "damping":
			p.advance()
			n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
			b.Damping = &n
		case "tolerance":
			p.advance()
			n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
			b.Tol = &n
		case "max_iterations":
			p.advance()
			n, _ := strconv.Atoi(p.expectNumberStr())
			b.MaxIter = &n
		default:
			p.errorf("unexpected %q inside find related block", p.peek().Value)
			return false
		}
	case lexer.TokenLabel:
		b.Label = p.parseLabelClause()
	case lexer.TokenPriority:
		pr := p.parsePriority()
		b.Priority = &pr
	default:
		p.errorf("unexpected token %q inside find related block", p.peek().Value)
		return false
	}
	return true
}

func (p *parser) parseSeedList() []ast.Expr {
	p.expect(lexer.TokenLBracket)
	var seeds []ast.Expr
	for !p.at(lexer.TokenRBracket) && !p.at(lexer.TokenEOF) {
		seeds = append(seeds, p.parseExpr())
		if p.at(lexer.TokenComma) {
			p.advance()
		}
	}
	p.expect(lexer.TokenRBracket)
	return seeds
}

func (p *parser) parseNestedRelatedClause() *ast.RelatedClause {
	p.advance() // find
	if p.at(lexer.TokenRelated) {
		p.advance()
	}
	clause := &ast.RelatedClause{}
	for {
		// Consume optional clauses by ident keyword
		if p.at(lexer.TokenIdent) {
			switch p.peek().Value {
			case "to":
				p.advance()
				clause.To = p.parseExpr()
				continue
			case "seeds":
				p.advance()
				clause.Seeds = p.parseSeedList()
				continue
			case "top_k":
				p.advance()
				n, _ := strconv.Atoi(p.expectNumberStr())
				clause.TopK = &n
				continue
			case "damping":
				p.advance()
				n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
				clause.Damping = &n
				continue
			}
		}
		break
	}
	return clause
}

// ─── Clauses ──────────────────────────────────────────────────────────────────

func (p *parser) parseSelector() ast.Selector {
	p.advance() // for
	// "records" keyword
	if p.at(lexer.TokenRecords) {
		p.advance()
	} else {
		p.errorf("expected 'records' after 'for', got %q", p.peek().Value)
	}
	p.expect(lexer.TokenWhere)
	cond := p.parseOrCondition()
	return ast.Selector{Target: "records", Conditions: []ast.Condition{cond}}
}

func (p *parser) parseFlagTarget() *ast.FlagTarget {
	p.advance() // flag
	if p.at(lexer.TokenMatching) {
		p.advance()
	}
	kind := "items"
	switch p.peek().Type {
	case lexer.TokenItems:
		kind = p.advance().Value
	case lexer.TokenRecords:
		kind = p.advance().Value
	case lexer.TokenIdent:
		kind = p.advance().Value
	}
	return &ast.FlagTarget{Kind: kind}
}

func (p *parser) parseLabelClause() *ast.Template {
	p.advance() // label / suggest / reason
	raw := p.expectString()
	t := ast.ParseTemplate(raw)
	return &t
}

func (p *parser) parsePriority() ast.Priority {
	p.advance() // priority
	switch p.peek().Type {
	case lexer.TokenLow:
		p.advance()
		return ast.PriorityLow
	case lexer.TokenMedium:
		p.advance()
		return ast.PriorityMedium
	case lexer.TokenHigh:
		p.advance()
		return ast.PriorityHigh
	case lexer.TokenCritical:
		p.advance()
		return ast.PriorityCritical
	default:
		p.errorf("expected LOW/MEDIUM/HIGH/CRITICAL, got %q", p.peek().Value)
		return ast.PriorityLow
	}
}

func (p *parser) parseConfidenceClause() float64 {
	p.advance() // confidence
	p.expect(lexer.TokenGte)
	n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
	return n
}

// parseScoreAnnotation handles the `confidence NUMBER` provenance form
// (no `>=`). The number is the rule's self-asserted confidence in [0, 1];
// the validator range-checks it. See docs/spec/v0.2.md section 12.
func (p *parser) parseScoreAnnotation() float64 {
	p.advance() // confidence
	n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
	return n
}

// parseSourceAnnotation handles `source "..."`. The string is opaque
// provenance metadata, surfaced in the explain output but not
// interpreted by the runtime.
func (p *parser) parseSourceAnnotation() string {
	p.advance() // source
	return p.expectString()
}

func (p *parser) parseCalculateClause() ast.CalculateClause {
	p.advance() // calculate
	name := p.expectIdent()
	p.expect(lexer.TokenFrom)
	from := p.expectIdent()
	calc := ast.CalculateClause{Name: name, From: from}

	// The remaining clauses — `of attr "X"`, `where COND`, a METHOD keyword
	// (average|sum|count|weighted_moving_average), and `within last N <unit>`
	// — are all optional and order-independent. Loop until a token that
	// starts none of them (i.e. the next block clause).
	for {
		switch {
		case p.at(lexer.TokenIdent) && p.peek().Value == "of":
			p.advance()
			calc.Value = p.parseExpr()
		case p.at(lexer.TokenWhere):
			p.advance()
			calc.Where = []ast.Condition{p.parseOrCondition()}
		case p.at(lexer.TokenWithin):
			p.advance()
			p.expect(lexer.TokenLast)
			n, _ := strconv.Atoi(p.expectNumberStr())
			unit := p.expectDurationUnit()
			calc.Within = &ast.Duration{Value: n, Unit: unit}
		case isCalcMethod(p):
			calc.Method = readCalcMethod(p)
			// A method may name its own window directly: `... average last
			// 7 days` — equivalent to a trailing `within last 7 days`.
			if p.at(lexer.TokenLast) {
				p.advance()
				n, _ := strconv.Atoi(p.expectNumberStr())
				unit := p.expectDurationUnit()
				calc.Within = &ast.Duration{Value: n, Unit: unit}
			}
		default:
			// Default to average only when a value column was named; a
			// bare `calculate X from Y` (no method, no `of attr`) stays
			// method-less and legacy-inert for backward compatibility.
			if calc.Method == "" && calc.Value != nil {
				calc.Method = "average"
			}
			return calc
		}
	}
}

// isCalcMethod reports whether the current token begins a calculate
// aggregation method keyword.
func isCalcMethod(p *parser) bool {
	if p.at(lexer.TokenCount) {
		return true
	}
	if p.at(lexer.TokenIdent) {
		switch p.peek().Value {
		case "average", "avg", "mean", "sum", "total", "weighted_moving_average", "wma":
			return true
		}
	}
	return false
}

// readCalcMethod consumes a method keyword and returns its normalized name:
// "average", "sum", "count", or "wma".
func readCalcMethod(p *parser) string {
	if p.at(lexer.TokenCount) {
		p.advance()
		return "count"
	}
	switch p.advance().Value {
	case "sum", "total":
		return "sum"
	case "weighted_moving_average", "wma":
		return "wma"
	default: // average, avg, mean
		return "average"
	}
}

func (p *parser) parseEveryClause() *ast.EveryClause {
	p.advance() // every
	n, _ := strconv.Atoi(p.expectNumberStr())
	unit := p.peekDurationUnitOrIdent()
	every := &ast.EveryClause{Value: n, Unit: unit}
	p.expect(lexer.TokenOn)
	p.expect(lexer.TokenAttr)
	every.OnAttr = p.expectString()
	return every
}

func (p *parser) parseRequiresClause() *ast.RequiresClause {
	p.advance() // requires
	if p.at(lexer.TokenApproval) {
		p.advance() // approval
		p.expect(lexer.TokenFrom)
		p.expect(lexer.TokenRole)
		role := p.expectString()
		return &ast.RequiresClause{Approval: &ast.ApprovalExpr{Role: role}}
	}
	what := p.expectString()
	return &ast.RequiresClause{What: what}
}

// parseDoClause parses `do <verb> [arg ...]`. The verb is any word — an IDENT
// or a keyword that happens to be one (`do block "pr.merge"`), since the verb
// vocabulary belongs to the host, not the language. Arguments run until the
// next clause keyword or the end of the rule body; tln is not
// newline-sensitive, so the clause boundary is what terminates the list.
func (p *parser) parseDoClause() *ast.DoAction {
	tok := p.advance() // do
	// The verb position is unconditional: `do block "pr.merge"` names the verb
	// `block`, which is also a clause keyword. Only the end of the body stops
	// us here — the boundary check applies to arguments, not to the verb.
	if p.at(lexer.TokenRBrace) || p.at(lexer.TokenEOF) {
		p.errorf("expected an action verb after `do`, got %q", p.peek().Value)
		return nil
	}
	// A quoted verb is the likely first mistake, and it would otherwise parse:
	// the lexer hands back the string's contents, so `do "approve" "pr"` would
	// look identical to `do approve "pr"` by the time we see it.
	if p.at(lexer.TokenString) {
		p.errorf("action verb must not be quoted — write `do %s ...`, not `do %q ...`",
			p.peek().Value, p.peek().Value)
		p.advance()
		return nil
	}
	verb := p.advance().Value
	if verb == "" {
		p.errorf("expected an action verb after `do`")
		return nil
	}
	a := &ast.DoAction{Verb: verb, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}
	for !p.atRuleClauseBoundary() {
		before := p.pos
		arg := p.parseExpr()
		// Only the forms the action resolver understands are accepted. Anything
		// else (arithmetic, list literals, negation) would resolve to nothing at
		// run time and hand the host an empty argument with no diagnostic.
		switch arg.(type) {
		case *ast.AttrExpr, *ast.LiteralExpr, *ast.IdentExpr:
			a.Args = append(a.Args, arg)
		default:
			p.errorf("unsupported argument to `do %s` — an action argument must be `attr \"name\"`, a literal, or a bare name", verb)
		}
		if p.pos == before {
			// parseExpr consumed nothing — bail rather than spin.
			p.errorf("unexpected token %q in arguments to `do %s`", p.peek().Value, verb)
			p.advance()
			break
		}
	}
	return a
}

// atRuleClauseBoundary reports whether the current token starts a new rule
// clause (or ends the body), which is what terminates a `do` argument list.
func (p *parser) atRuleClauseBoundary() bool {
	switch p.peek().Type {
	case lexer.TokenDo, lexer.TokenReason, lexer.TokenPriority,
		lexer.TokenBlock, lexer.TokenAllow, lexer.TokenRequires,
		lexer.TokenOverrides, lexer.TokenEvery, lexer.TokenBefore,
		lexer.TokenAfter, lexer.TokenConfidence, lexer.TokenSource,
		lexer.TokenRBrace, lexer.TokenEOF:
		return true
	}
	return p.atLoggerStatement()
}

func (p *parser) parseFeaturesClause() []ast.Expr {
	p.advance() // features
	p.expect(lexer.TokenLBracket)
	var features []ast.Expr
	for !p.at(lexer.TokenRBracket) && !p.at(lexer.TokenEOF) {
		features = append(features, p.parseExpr())
		if p.at(lexer.TokenComma) {
			p.advance()
		}
	}
	p.expect(lexer.TokenRBracket)
	return features
}

func (p *parser) parseTrainedOnClause() *ast.TrainedOnClause {
	p.advance() // trained_on
	p.expect(lexer.TokenRecords)
	p.expect(lexer.TokenWhere)
	cond := p.parseOrCondition()
	return &ast.TrainedOnClause{Conditions: []ast.Condition{cond}}
}

func (p *parser) parseSeriesClause() ast.SeriesClause {
	p.advance() // series
	attr := p.parseExpr()
	p.expect(lexer.TokenOver)
	p.expect(lexer.TokenLast)
	n, _ := strconv.Atoi(p.expectNumberStr())
	unit := p.expectDurationUnit()
	return ast.SeriesClause{Attr: attr, Window: ast.Duration{Value: n, Unit: unit}}
}

func (p *parser) parseForecastPredictClause() *ast.ForecastPredictClause {
	p.advance() // predict
	variable := p.expectIdent()
	// optional "value" keyword
	if p.at(lexer.TokenIdent) && p.peek().Value == "value" {
		p.advance()
	}
	cond := p.parseOrCondition()
	return &ast.ForecastPredictClause{Variable: variable, Condition: cond}
}

func (p *parser) parseAnomalyClause() *ast.AnomalyClause {
	p.advance() // is
	p.expect(lexer.TokenAnomaly)
	method := parseAnomalyMethod(p)
	p.expect(lexer.TokenComparedTo)
	p.expect(lexer.TokenLast)
	n, _ := strconv.Atoi(p.expectNumberStr())
	unit := p.expectDurationUnit()
	return &ast.AnomalyClause{Method: method, Window: ast.Duration{Value: n, Unit: unit}}
}

// parseAnomalyMethod reads an optional `using METHOD` clause and returns the
// method name. Falls back to "" (= zscore default) when the clause is absent.
// Recognized: "zscore" (explicit), "grubbs". Other identifiers are accepted
// at parse time and rejected by the validator with a helpful suggestion.
func parseAnomalyMethod(p *parser) string {
	if !p.at(lexer.TokenUsing) {
		return ""
	}
	p.advance() // using
	if !p.at(lexer.TokenIdent) {
		p.errorf("expected anomaly method (zscore, grubbs) after 'using', got %q", p.peek().Value)
		return ""
	}
	return p.advance().Value
}

func (p *parser) parseNestedPredictClause() *ast.PredictClause {
	p.advance() // predict
	clause := &ast.PredictClause{}
	if p.at(lexer.TokenFeatures) {
		clause.Features = p.parseFeaturesClause()
	}
	if p.at(lexer.TokenTrainedOn) {
		clause.TrainedOn = p.parseTrainedOnClause()
	}
	if p.at(lexer.TokenConfidence) {
		c := p.parseConfidenceClause()
		clause.Confidence = &c
	}
	return clause
}

func (p *parser) parseNestedForecastClause() *ast.ForecastClause {
	p.advance() // forecast
	clause := &ast.ForecastClause{}
	if p.at(lexer.TokenSeries) {
		clause.Series = p.parseSeriesClause()
	}
	if p.at(lexer.TokenPredict) {
		clause.Predict = p.parseForecastPredictClause()
	}
	return clause
}

func (p *parser) parseNestedClusterClause() *ast.ClusterClause {
	p.advance() // cluster
	// "by" is an ident
	if p.at(lexer.TokenIdent) && p.peek().Value == "by" {
		p.advance()
	}
	return &ast.ClusterClause{ByAttrs: p.parseAttrList()}
}

func (p *parser) parseNestedSimilarClause() *ast.SimilarClause {
	p.advance() // find
	if p.at(lexer.TokenSimilar) {
		p.advance()
	}
	// "to"
	if p.at(lexer.TokenIdent) && p.peek().Value == "to" {
		p.advance()
	}
	clause := &ast.SimilarClause{To: p.parseExpr()}
	if p.at(lexer.TokenWithin) {
		p.advance()
		n, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
		clause.Within = &n
	}
	return clause
}

func (p *parser) parsePatternExpr() *ast.PatternExpr {
	p.advance() // when
	// "N+ records same ATTR [within N UNIT]"
	n, _ := strconv.Atoi(p.expectNumberStr())
	if p.at(lexer.TokenPlus) {
		p.advance()
	}
	if p.at(lexer.TokenRecords) {
		p.advance()
	}
	var groupBy string
	if p.at(lexer.TokenSame) {
		p.advance()
		groupBy = p.expectIdent()
	}
	pattern := &ast.PatternExpr{MinCount: n, GroupBy: groupBy}
	if p.at(lexer.TokenWithin) {
		p.advance()
		n2, _ := strconv.Atoi(p.expectNumberStr())
		unit := p.expectDurationUnit()
		dur := ast.Duration{Value: n2, Unit: unit}
		pattern.Window = &dur
	}
	return pattern
}

func (p *parser) parseAttrList() []ast.Expr {
	var list []ast.Expr
	list = append(list, p.parseExpr())
	for p.at(lexer.TokenComma) {
		p.advance()
		list = append(list, p.parseExpr())
	}
	return list
}

func (p *parser) parseForEachClause() *ast.ForEachClause {
	p.advance() // for
	p.advance() // each
	variable := p.expectIdent()
	p.expect(lexer.TokenIn)
	over := p.parseExpr()
	p.expect(lexer.TokenLBrace)
	clause := &ast.ForEachClause{Variable: variable, Over: over}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		cond := p.parseOrCondition()
		if cond != nil {
			clause.Body = append(clause.Body, cond)
		}
	}
	p.expect(lexer.TokenRBrace)
	return clause
}

// ─── When clause ──────────────────────────────────────────────────────────────

func (p *parser) parseWhenClause() ast.Condition {
	p.advance() // when
	// "detect/predict/forecast/recommend NAME matches"
	if p.isBlockKeyword(p.peek().Type) {
		kind := p.advance().Value
		name := p.expectString()
		if p.at(lexer.TokenMatches) || (p.at(lexer.TokenIdent) && p.peek().Value == "matches") {
			p.advance()
		}
		return &ast.BlockMatchesCondition{Kind: kind, Name: name}
	}
	return p.parseOrCondition()
}

func (p *parser) isBlockKeyword(tt lexer.TokenType) bool {
	switch tt {
	case lexer.TokenDetect, lexer.TokenPredict, lexer.TokenForecast,
		lexer.TokenCluster, lexer.TokenClassify, lexer.TokenFind:
		return true
	}
	return false
}

// ─── Condition parsing ────────────────────────────────────────────────────────

func (p *parser) parseOrCondition() ast.Condition {
	left := p.parseAndCondition()
	for p.at(lexer.TokenOr) {
		p.advance()
		right := p.parseAndCondition()
		left = &ast.LogicalCondition{Op: "or", Left: left, Right: right}
	}
	return left
}

func (p *parser) parseAndCondition() ast.Condition {
	left := p.parseNotCondition()
	for p.at(lexer.TokenAnd) {
		p.advance()
		right := p.parseNotCondition()
		left = &ast.LogicalCondition{Op: "and", Left: left, Right: right}
	}
	return left
}

func (p *parser) parseNotCondition() ast.Condition {
	if p.at(lexer.TokenNot) {
		// could be "not X" or "not in" — "not in" is handled in parseAtomCondition
		// peek ahead: if next is "in", let parseAtomCondition handle via an Expr + "not in"
		// For standalone "not COND", consume not and recurse
		if p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type != lexer.TokenIn {
			p.advance()
			return &ast.NotCondition{Inner: p.parseAtomCondition()}
		}
	}
	return p.parseAtomCondition()
}

func (p *parser) parseAtomCondition() ast.Condition {
	switch p.peek().Type {
	case lexer.TokenHas:
		return p.parseHasCondition()
	case lexer.TokenHasOpen:
		p.advance()
		return desugarHasOpen(p.parseRecordTypeTail())
	case lexer.TokenHasExpired:
		p.advance()
		name := p.parseRecordTypeTail()
		var where ast.Condition
		if p.at(lexer.TokenWhere) {
			p.advance()
			where = p.parseOrCondition()
		}
		return desugarHasExpired(name, where)
	case lexer.TokenWas:
		// `was ( <inner> ) N <unit> ago` — the inner condition held about
		// the record N units before now (time-travel).
		p.advance() // was
		p.expect(lexer.TokenLParen)
		inner := p.parseOrCondition()
		p.expect(lexer.TokenRParen)
		delta := p.parseDuration()
		p.expect(lexer.TokenAgo)
		return &ast.AsOfCondition{Inner: inner, Delta: delta}
	case lexer.TokenEvent:
		return p.parseEventSequenceCondition()
	case lexer.TokenRecord:
		return p.parseRecordSequenceCondition()
	case lexer.TokenIs:
		// standalone "is STRING" — no subject
		p.advance()
		if p.at(lexer.TokenAnomaly) {
			// "is anomaly [using METHOD] compared_to last N UNIT"
			p.advance() // anomaly
			method := parseAnomalyMethod(p)
			p.expect(lexer.TokenComparedTo)
			p.expect(lexer.TokenLast)
			n, _ := strconv.Atoi(p.expectNumberStr())
			unit := p.expectDurationUnit()
			return &ast.AnomalyCondition{Method: method, Window: ast.Duration{Value: n, Unit: unit}}
		}
		name := p.expectString()
		return &ast.IsCondition{Name: name}
	default:
		// `name(var)` — a derived-predicate call. An IDENT immediately
		// followed by `(` matches no other condition form (aggregates and
		// category_tree use dedicated keyword tokens), so it's unambiguous —
		// except for the string builtins (`upper(...)`, `substring(...)`),
		// which are function-valued expressions used inside a comparison.
		if p.at(lexer.TokenIdent) && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenLParen {
			if !ast.IsStringBuiltin(p.peek().Value) {
				return p.parsePredicateCall()
			}
		}
		return p.parseExprCondition()
	}
}

// parseStateRef reads a state name with optional substate suffix:
//
//	pending          → "pending"
//	in_flight        → "in_flight"
//	in_flight / boarding → "in_flight/boarding"
//
// Returns "" if no ident is at the current position. Used by
// transitions where the parser doesn't have access to the closing
// brace token sets parseStateMachineBlock relies on.
func parseStateRef(p *parser) string {
	if !p.at(lexer.TokenIdent) {
		return ""
	}
	name := p.advance().Value
	if p.at(lexer.TokenSlash) {
		p.advance()
		if p.at(lexer.TokenIdent) {
			name = name + "/" + p.advance().Value
		}
	}
	return name
}

// parseEventSequenceCondition reads:
//
//	event_sequence "A" -> "B" [ -> "C" ... ] within N <unit>
//
// Steps are ordered strings; `within` is the upper bound on the
// elapsed time between the first and last step. Used by the executor
// as a small regex over an entity's event history.
func (p *parser) parseEventSequenceCondition() ast.Condition {
	p.advance() // event_sequence
	first := p.expectString()
	steps := []string{first}
	for p.at(lexer.TokenArrow) {
		p.advance()
		steps = append(steps, p.expectString())
	}
	cond := &ast.EventSequenceCondition{Steps: steps}
	if p.at(lexer.TokenWithin) {
		p.advance()
		n, _ := strconv.Atoi(p.expectNumberStr())
		unit := p.expectDurationUnit()
		cond.Window = ast.Duration{Value: n, Unit: unit}
	}
	return cond
}

// parseRecordSequenceCondition reads:
//
//	record type "A" followed_by record type "B"
//	  [followed_by record type "C" ...]
//	  [on same IDENT]    // grouping attribute (default "item")
//	  [within N <unit>]
//
// Each `record type "X"` is one ordered step. The grouping key names
// a per-record attribute (`:record/item` for `on same item`) that
// must match across all steps. `within` bounds the first→last span.
func (p *parser) parseRecordSequenceCondition() ast.Condition {
	p.advance() // record
	p.expect(lexer.TokenTypeKw)
	first := p.expectString()
	steps := []string{first}
	for p.at(lexer.TokenFollowedBy) {
		p.advance() // followed_by
		p.expect(lexer.TokenRecord)
		p.expect(lexer.TokenTypeKw)
		steps = append(steps, p.expectString())
	}
	cond := &ast.RecordSequenceCondition{Steps: steps, On: "item"}
	if p.at(lexer.TokenOn) {
		p.advance() // on
		p.expect(lexer.TokenSame)
		// Grouping key — any ident (item, person, vehicle, ...).
		if p.at(lexer.TokenIdent) {
			cond.On = p.advance().Value
		} else {
			p.errorf("expected identifier after 'on same', got %q", p.peek().Value)
		}
	}
	if p.at(lexer.TokenWithin) {
		p.advance()
		n, _ := strconv.Atoi(p.expectNumberStr())
		unit := p.expectDurationUnit()
		cond.Window = ast.Duration{Value: n, Unit: unit}
	}
	return cond
}

func (p *parser) parseHasCondition() ast.Condition {
	p.advance() // has
	return &ast.HasCondition{Type: p.parseRecordTypeTail()}
}

// parseRecordTypeTail consumes an optional `record` (or other lead-in
// idents) up to `type "X"` and returns X. Shared by `has`, `has_open`,
// and `has_expired`.
func (p *parser) parseRecordTypeTail() string {
	for !p.atEOF() && !p.at(lexer.TokenTypeKw) && p.peek().Value != "type" {
		if p.at(lexer.TokenRecord) || p.at(lexer.TokenRecords) || p.at(lexer.TokenIdent) {
			p.advance()
			continue
		}
		break
	}
	p.advance() // type
	return p.expectString()
}

// desugarHasOpen expands `has_open record type "X"` into the existing
// form `has record type "X" and attr "X.status" != "closed"`. The
// "closed" status convention is hard-coded (see #62 scope notes).
func desugarHasOpen(recType string) ast.Condition {
	return &ast.LogicalCondition{
		Op:   "and",
		Left: &ast.HasCondition{Type: recType},
		Right: &ast.CompareCondition{
			Left:  &ast.AttrExpr{Name: recType + ".status"},
			Op:    "!=",
			Right: &ast.LiteralExpr{Value: "closed"},
		},
	}
}

// desugarHasExpired expands `has_expired record type "X" [where COND]`
// into `has record type "X" and attr "X.expires_at" < today [and COND]`.
func desugarHasExpired(recType string, where ast.Condition) ast.Condition {
	c := ast.Condition(&ast.LogicalCondition{
		Op:   "and",
		Left: &ast.HasCondition{Type: recType},
		Right: &ast.CompareCondition{
			Left:  &ast.AttrExpr{Name: recType + ".expires_at"},
			Op:    "<",
			Right: &ast.TodayExpr{},
		},
	})
	if where != nil {
		c = &ast.LogicalCondition{Op: "and", Left: c, Right: where}
	}
	return c
}

func (p *parser) parseExprCondition() ast.Condition {
	expr := p.parseExpr()
	switch p.peek().Type {
	case lexer.TokenEq, lexer.TokenNeq, lexer.TokenGt, lexer.TokenGte, lexer.TokenLt, lexer.TokenLte:
		op := p.advance().Value
		right := p.parseExpr()
		return &ast.CompareCondition{Left: expr, Op: op, Right: right}

	case lexer.TokenCorrelatesWith:
		// `attr "X" correlates_with attr "Y" over last N <unit> OP <threshold>`
		p.advance() // correlates_with
		right := p.parseExpr()
		p.expect(lexer.TokenOver)
		p.expect(lexer.TokenLast)
		n, _ := strconv.Atoi(p.expectNumberStr())
		unit := p.expectDurationUnit()
		op := p.expectComparisonOp()
		thr, _ := strconv.ParseFloat(p.expectNumberStr(), 64)
		return &ast.CorrelationCondition{
			Left:      expr,
			Right:     right,
			Method:    "pearson",
			Window:    ast.Duration{Value: n, Unit: unit},
			Op:        op,
			Threshold: thr,
		}

	case lexer.TokenApproaching:
		// `attr "X" approaching within N units` desugars to
		// `attr "X" >= today and attr "X" <= today + N units`.
		p.advance() // approaching
		p.expect(lexer.TokenWithin)
		n, _ := strconv.Atoi(p.expectNumberStr())
		unit := p.expectDurationUnit()
		upper := &ast.BinaryExpr{Left: &ast.TodayExpr{}, Op: "+", Right: &ast.LiteralExpr{Value: ast.Duration{Value: n, Unit: unit}}}
		return &ast.LogicalCondition{
			Op:    "and",
			Left:  &ast.CompareCondition{Left: expr, Op: ">=", Right: &ast.TodayExpr{}},
			Right: &ast.CompareCondition{Left: expr, Op: "<=", Right: upper},
		}

	case lexer.TokenIn:
		p.advance()
		members := p.parseMembersList()
		return &ast.MembershipCondition{Expr: expr, Negated: false, Members: members}

	case lexer.TokenNot:
		p.advance() // not
		p.expect(lexer.TokenIn)
		members := p.parseMembersList()
		return &ast.MembershipCondition{Expr: expr, Negated: true, Members: members}

	case lexer.TokenContains:
		p.advance()
		val := p.expectString()
		return &ast.StringMatchCondition{Subject: expr, Op: "contains", Value: val}

	case lexer.TokenStartsWith:
		p.advance()
		val := p.expectString()
		return &ast.StringMatchCondition{Subject: expr, Op: "starts_with", Value: val}

	case lexer.TokenEndsWith:
		p.advance()
		val := p.expectString()
		return &ast.StringMatchCondition{Subject: expr, Op: "ends_with", Value: val}

	case lexer.TokenMatches:
		p.advance()
		// `matches phrase "X"` — exact-phrase FTS variant. Falls back
		// to the plain `matches "X"` term-search form when the next
		// token isn't `phrase`.
		op := "matches"
		if p.at(lexer.TokenIdent) && p.peek().Value == "phrase" {
			p.advance()
			op = "matches_phrase"
		}
		val := p.expectString()
		return &ast.StringMatchCondition{Subject: expr, Op: op, Value: val}

	case lexer.TokenIs:
		p.advance()
		if p.at(lexer.TokenAnomaly) {
			p.advance()
			method := parseAnomalyMethod(p)
			p.expect(lexer.TokenComparedTo)
			p.expect(lexer.TokenLast)
			n, _ := strconv.Atoi(p.expectNumberStr())
			unit := p.expectDurationUnit()
			return &ast.AnomalyCondition{Subject: expr, Method: method, Window: ast.Duration{Value: n, Unit: unit}}
		}
		name := p.expectString()
		return &ast.IsCondition{Subject: expr, Name: name}

	case lexer.TokenOlderThan:
		p.advance()
		dur := p.parseDuration()
		return &ast.TemporalCondition{Subject: expr, Op: "older_than", Value: dur}

	case lexer.TokenNewerThan:
		p.advance()
		dur := p.parseDuration()
		return &ast.TemporalCondition{Subject: expr, Op: "newer_than", Value: dur}

	case lexer.TokenChangedTo:
		p.advance()
		val := p.parseExpr()
		return &ast.ChangedToCondition{Attribute: exprToAttrName(expr), Value: val}

	default:
		p.errorf("expected condition operator after expression, got %q", p.peek().Value)
		return nil
	}
}

func (p *parser) parseMembersList() []ast.Expr {
	if p.at(lexer.TokenLBracket) {
		p.advance()
		var members []ast.Expr
		for !p.at(lexer.TokenRBracket) && !p.at(lexer.TokenEOF) {
			members = append(members, p.parseExpr())
			if p.at(lexer.TokenComma) {
				p.advance()
			}
		}
		p.expect(lexer.TokenRBracket)
		return members
	}
	// category_tree("Root")
	if p.at(lexer.TokenCategoryTree) {
		return []ast.Expr{p.parsePrimary()}
	}
	p.errorf("expected '[' or category_tree() in membership condition")
	return nil
}

func (p *parser) parseDuration() ast.Duration {
	n, _ := strconv.Atoi(p.expectNumberStr())
	unit := p.expectDurationUnit()
	return ast.Duration{Value: n, Unit: unit}
}

// ─── Expression parsing ───────────────────────────────────────────────────────

func (p *parser) parseExpr() ast.Expr {
	return p.parseAddSub()
}

func (p *parser) parseAddSub() ast.Expr {
	left := p.parseMulDiv()
	for p.at(lexer.TokenPlus) || p.at(lexer.TokenMinus) {
		op := p.advance().Value
		right := p.parseMulDiv()
		left = &ast.BinaryExpr{Left: left, Op: op, Right: right}
	}
	return left
}

func (p *parser) parseMulDiv() ast.Expr {
	left := p.parseUnary()
	for p.at(lexer.TokenStar) || p.at(lexer.TokenSlash) || p.at(lexer.TokenPercent) {
		op := p.advance().Value
		right := p.parseUnary()
		left = &ast.BinaryExpr{Left: left, Op: op, Right: right}
	}
	return left
}

func (p *parser) parseUnary() ast.Expr {
	if p.at(lexer.TokenMinus) {
		p.advance()
		return &ast.UnaryExpr{Op: "-", Operand: p.parsePrimary()}
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() ast.Expr {
	switch p.peek().Type {
	case lexer.TokenAttr:
		p.advance()
		name := p.expectString()
		return &ast.AttrExpr{Name: name}

	case lexer.TokenContext:
		p.advance()
		p.expect(lexer.TokenDot)
		field := p.expectIdent()
		for p.at(lexer.TokenDot) {
			p.advance()
			field += "." + p.expectIdent()
		}
		return &ast.ContextExpr{Field: field}

	case lexer.TokenStep:
		p.advance()
		p.expect(lexer.TokenLParen)
		name := p.expectString()
		p.expect(lexer.TokenRParen)
		p.expect(lexer.TokenDot)
		field := p.expectIdent()
		for p.at(lexer.TokenDot) {
			p.advance()
			// `.find(<cond>).<field>` — pick the first list element matching a
			// predicate, then read a field from it. `find` is a keyword token, so
			// match on the value + a `(` lookahead rather than expectIdent.
			if p.peek().Value == "find" && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Type == lexer.TokenLParen {
				p.advance() // find
				p.expect(lexer.TokenLParen)
				cond := p.parseOrCondition()
				p.expect(lexer.TokenRParen)
				rest := ""
				for p.at(lexer.TokenDot) {
					p.advance()
					var seg string
					if p.at(lexer.TokenNumber) {
						seg = p.peek().Value
						p.advance()
					} else {
						seg = p.expectIdent()
					}
					if rest == "" {
						rest = seg
					} else {
						rest += "." + seg
					}
				}
				return &ast.FindExpr{
					Source: &ast.StepResultExpr{StepName: name, Field: field},
					Cond:   cond,
					Field:  rest,
				}
			}
			// A segment is an identifier (`.field`) or an integer list index
			// (`.0`), so navigation can reach into a list result — e.g.
			// step("find").result.0.id picks the first match's id.
			var next string
			if p.at(lexer.TokenNumber) {
				next = p.peek().Value
				p.advance()
			} else {
				next = p.expectIdent()
			}
			if next == "map" && p.at(lexer.TokenLParen) {
				p.advance() // consume (
				mapField := p.expectIdent()
				p.expect(lexer.TokenRParen)
				return &ast.MapExpr{
					Source: &ast.StepResultExpr{StepName: name, Field: field},
					Field:  mapField,
				}
			}
			field += "." + next
		}
		return &ast.StepResultExpr{StepName: name, Field: field}

	case lexer.TokenCategoryTree:
		p.advance()
		p.expect(lexer.TokenLParen)
		root := p.expectString()
		p.expect(lexer.TokenRParen)
		return &ast.CategoryTreeExpr{Root: root}

	case lexer.TokenToday:
		p.advance()
		return &ast.TodayExpr{}

	case lexer.TokenThreshold:
		// `threshold "name"` — a reference to a cached ThresholdBlock's value.
		// Distinct from the top-level `threshold "name" { ... }` block, which
		// is dispatched by parseBlock; here we're inside an expression.
		p.advance()
		return &ast.ThresholdRefExpr{Name: p.expectString()}

	case lexer.TokenLearnedThreshold:
		p.advance()               // learned_threshold
		method := p.expectIdent() // e.g. "p95"
		if p.at(lexer.TokenIdent) && p.peek().Value == "of" {
			p.advance()
		} else {
			p.errorf("expected 'of' after learned_threshold %s, got %q", method, p.peek().Value)
		}
		subject := p.parsePrimary()
		p.expect(lexer.TokenOver)
		p.expect(lexer.TokenLast)
		n, _ := strconv.Atoi(p.expectNumberStr())
		unit := p.expectDurationUnit()
		return &ast.LearnedThresholdExpr{
			Method:  method,
			Subject: subject,
			Window:  ast.Duration{Value: n, Unit: unit},
		}

	case lexer.TokenNumber:
		val := p.advance().Value
		// `7 days` — a number immediately followed by a date-duration unit is a
		// duration literal, so `today + 7 days` evaluates as date + Duration (see
		// the BinaryExpr date-arithmetic path in the evaluator). Without this the
		// unit token is left stranded and the caller errors on it — which is what
		// made `concat(…, today + 7 days, …)` unparseable.
		if isDurationUnit(p.peek().Type) {
			n, _ := strconv.Atoi(val)
			unit := p.advance().Value
			return &ast.LiteralExpr{Value: ast.Duration{Value: n, Unit: unit}}
		}
		n, _ := strconv.ParseFloat(val, 64)
		return &ast.LiteralExpr{Value: n}

	case lexer.TokenString:
		val := p.advance().Value
		return &ast.LiteralExpr{Value: val}

	case lexer.TokenBool:
		val := p.advance().Value
		return &ast.LiteralExpr{Value: val == "true"}

	case lexer.TokenLBracket:
		p.advance()
		var elems []ast.Expr
		for !p.at(lexer.TokenRBracket) && !p.at(lexer.TokenEOF) {
			elems = append(elems, p.parseExpr())
			if p.at(lexer.TokenComma) {
				p.advance()
			}
		}
		p.expect(lexer.TokenRBracket)
		return &ast.ListExpr{Elements: elems}

	case lexer.TokenLParen:
		p.advance()
		expr := p.parseExpr()
		p.expect(lexer.TokenRParen)
		return expr

	case lexer.TokenTotal, lexer.TokenCount, lexer.TokenAvg:
		fn := p.advance().Value
		p.expect(lexer.TokenLParen)
		var arg ast.Expr
		// `count(records)` or `count()` is valid; otherwise expression required.
		if p.at(lexer.TokenRecords) || p.at(lexer.TokenRParen) {
			if p.at(lexer.TokenRecords) {
				p.advance()
			}
		} else {
			arg = p.parseExpr()
		}
		p.expect(lexer.TokenRParen)
		return &ast.AggregateExpr{Fn: fn, Arg: arg}

	// Keywords used as bare attribute names
	case lexer.TokenStatus, lexer.TokenCategory, lexer.TokenTypeKw,
		lexer.TokenIdent, lexer.TokenRecords:
		name := p.advance().Value
		// `name(args...)` — a builtin function call (upper/substring/…).
		if p.at(lexer.TokenLParen) {
			return p.parseCallExpr(name)
		}
		// handle "tool_arg STRING" → treat as attr expr
		if name == "tool_arg" && p.at(lexer.TokenString) {
			argName := p.advance().Value
			return &ast.AttrExpr{Name: argName}
		}
		return &ast.IdentExpr{Name: name}

	default:
		p.errorf("expected expression, got %q", p.peek().Value)
		// Consume the offending token so every parsePrimary call makes forward
		// progress. Without this, a loop that calls parseExpr gated only by a
		// closing delimiter (parseCallExpr's arg list, array literals) spins
		// forever on a token that can't start an expression — allocating an
		// error node each pass until the process is OOM-killed.
		if !p.atEOF() {
			p.advance()
		}
		return &ast.LiteralExpr{Value: nil}
	}
}

// parseCallExpr reads `name(arg, arg, ...)` — a builtin function call. The
// opening `(` is the current token. Args are comma-separated expressions.
func (p *parser) parseCallExpr(name string) ast.Expr {
	p.expect(lexer.TokenLParen)
	var args []ast.Expr
	for !p.at(lexer.TokenRParen) && !p.at(lexer.TokenEOF) {
		before := p.pos
		args = append(args, p.parseExpr())
		if p.at(lexer.TokenComma) {
			p.advance()
		}
		// Belt-and-suspenders: if a malformed arg consumed nothing (and wasn't a
		// comma), force progress so the loop can never spin. parsePrimary now
		// advances on error too, but guard here as well for any future
		// non-advancing sub-parser.
		if p.pos == before {
			if p.atEOF() {
				break
			}
			p.advance()
		}
	}
	p.expect(lexer.TokenRParen)
	return &ast.CallExpr{Func: name, Args: args}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (p *parser) peek() lexer.Token {
	return p.tokens[p.pos]
}

func (p *parser) at(tt lexer.TokenType) bool {
	return p.tokens[p.pos].Type == tt
}

func (p *parser) atEOF() bool {
	return p.at(lexer.TokenEOF)
}

func (p *parser) advance() lexer.Token {
	t := p.tokens[p.pos]
	if !p.atEOF() {
		p.pos++
	}
	return t
}

func (p *parser) expect(tt lexer.TokenType) bool {
	if p.at(tt) {
		p.advance()
		return true
	}
	p.errorf("expected token %d, got %q", tt, p.peek().Value)
	return false
}

func (p *parser) expectString() string {
	if p.at(lexer.TokenString) {
		return p.advance().Value
	}
	p.errorf("expected string literal, got %q", p.peek().Value)
	return ""
}

func (p *parser) expectNumberStr() string {
	if p.at(lexer.TokenNumber) {
		return p.advance().Value
	}
	p.errorf("expected number, got %q", p.peek().Value)
	return "0"
}

// expectComparisonOp consumes a comparison operator token and returns its
// textual form (">", "<=", "==", …).
func (p *parser) expectComparisonOp() string {
	switch p.peek().Type {
	case lexer.TokenGt, lexer.TokenGte, lexer.TokenLt, lexer.TokenLte, lexer.TokenEq, lexer.TokenNeq:
		return p.advance().Value
	}
	p.errorf("expected comparison operator, got %q", p.peek().Value)
	return ">"
}

func (p *parser) expectIdent() string {
	switch p.peek().Type {
	case lexer.TokenIdent:
		return p.advance().Value
	// Allow certain keywords to act as identifiers
	case lexer.TokenStatus, lexer.TokenCategory, lexer.TokenTypeKw,
		lexer.TokenFrom, lexer.TokenRecords, lexer.TokenItems,
		lexer.TokenAction:
		return p.advance().Value
	}
	p.errorf("expected identifier, got %q", p.peek().Value)
	return ""
}

// isDurationUnit reports whether a token is a date-duration unit, so a
// `<number> <unit>` sequence parses as a duration literal in an expression
// (`today + 7 days`). Only date units — km (distance) and bare idents are
// excluded to avoid mis-reading an ordinary `<number> <ident>` sequence.
func isDurationUnit(tt lexer.TokenType) bool {
	switch tt {
	case lexer.TokenDays, lexer.TokenWeeks, lexer.TokenMonths, lexer.TokenYears:
		return true
	}
	return false
}

func (p *parser) expectDurationUnit() string {
	switch p.peek().Type {
	case lexer.TokenDays:
		return p.advance().Value
	case lexer.TokenWeeks:
		return p.advance().Value
	case lexer.TokenMonths:
		return p.advance().Value
	case lexer.TokenYears:
		return p.advance().Value
	case lexer.TokenKm:
		return p.advance().Value
	case lexer.TokenIdent:
		return p.advance().Value
	}
	p.errorf("expected duration unit (days/weeks/months/years/km), got %q", p.peek().Value)
	return "days"
}

func (p *parser) peekDurationUnitOrIdent() string {
	switch p.peek().Type {
	case lexer.TokenDays, lexer.TokenWeeks, lexer.TokenMonths, lexer.TokenYears, lexer.TokenKm:
		return p.advance().Value
	case lexer.TokenIdent:
		return p.advance().Value
	}
	p.errorf("expected duration unit or identifier, got %q", p.peek().Value)
	return ""
}

func (p *parser) errorf(format string, args ...interface{}) {
	tok := p.peek()
	p.diags.AddError(p.file, tok.Line, tok.Col, fmt.Sprintf(format, args...), "")
}

// synchronize skips to the next top-level `}` to recover from a block-level error.
func (p *parser) synchronize() {
	depth := 0
	for !p.atEOF() {
		switch p.peek().Type {
		case lexer.TokenLBrace:
			depth++
			p.advance()
		case lexer.TokenRBrace:
			if depth == 0 {
				p.advance()
				return
			}
			depth--
			p.advance()
		default:
			p.advance()
		}
	}
}

// synchronizeInBlock skips to the next clause start within a block body.
// Used for unknown tokens inside a block — advances one token.
func (p *parser) synchronizeInBlock() {
	p.advance()
}

// ─── test block ────────────────────────────────────────────────────────────

func (p *parser) parseTestBlock() *ast.TestBlock {
	tok := p.advance() // test
	name := p.expectString()
	if !p.expect(lexer.TokenLBrace) {
		p.synchronize()
		return nil
	}
	b := &ast.TestBlock{Name: name, Pos: ast.Pos{Line: tok.Line, Col: tok.Col}}

	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		switch p.peek().Type {
		case lexer.TokenGiven:
			p.advance() // given
			p.expect(lexer.TokenLBrace)
			for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
				b.Given = append(b.Given, p.parseTestDatum())
			}
			p.expect(lexer.TokenRBrace)
		case lexer.TokenWhen:
			p.advance()                    // when
			b.WhenKind = p.advance().Value // detect, rule, forecast, etc.
			// Two-word forms like `find similar` / `find related`.
			if b.WhenKind == "find" && (p.at(lexer.TokenSimilar) || p.at(lexer.TokenRelated)) {
				b.WhenKind += " " + p.advance().Value
			}
			b.WhenBlock = p.expectString()
		case lexer.TokenMock:
			b.Mocks = append(b.Mocks, p.parseMockClause())
		case lexer.TokenExpect:
			p.advance() // expect
			p.expect(lexer.TokenLBrace)
			for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
				// tool_called needs richer structure than TestAssertion,
				// so it lands in its own list.
				if p.at(lexer.TokenIdent) && p.peek().Value == "tool_called" {
					b.MCPCalls = append(b.MCPCalls, p.parseMCPCalledAssertion())
					continue
				}
				// did / did_not carry a verb plus positional arg matchers,
				// which TestAssertion's flat shape can't hold.
				if p.at(lexer.TokenIdent) && (p.peek().Value == "did" || p.peek().Value == "did_not") {
					b.Actions = append(b.Actions, p.parseActionAssertion())
					continue
				}
				b.Expect = append(b.Expect, p.parseTestAssertion())
			}
			p.expect(lexer.TokenRBrace)
		default:
			p.errorf("unexpected token %q inside test block", p.peek().Value)
			p.synchronizeInBlock()
		}
	}
	p.expect(lexer.TokenRBrace)
	return b
}

// parseMockClause parses `mock tool "server" "tool" { returns { k v ... } |
// fails "msg" | fails after N }`.
func (p *parser) parseMockClause() ast.MockClause {
	p.advance() // mock
	p.expect(lexer.TokenTool)
	m := ast.MockClause{Server: p.expectString(), Tool: p.expectString()}
	if !p.expect(lexer.TokenLBrace) {
		return m
	}
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
		word := p.advance().Value
		switch word {
		case "returns":
			p.expect(lexer.TokenLBrace)
			m.Returns = map[string]any{}
			for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
				if p.at(lexer.TokenComma) {
					p.advance()
					continue
				}
				key := p.advance().Value
				if key == "" {
					break
				}
				m.Returns[key] = p.parseLiteralValue()
			}
			p.expect(lexer.TokenRBrace)
		case "fails":
			m.Fails = true
			if p.at(lexer.TokenIdent) && p.peek().Value == "after" {
				p.advance()
				m.FailAfter, _ = strconv.Atoi(p.expectNumberStr())
			} else if p.at(lexer.TokenString) {
				m.FailMsg = p.expectString()
			}
		default:
			p.errorf("unexpected token %q inside mock body (expected returns / fails)", word)
		}
	}
	p.expect(lexer.TokenRBrace)
	return m
}

// parseMCPCalledAssertion parses `tool_called "server" "tool" [with { name
// OP value ... }]`.
func (p *parser) parseMCPCalledAssertion() ast.MCPCalledAssertion {
	p.advance() // tool_called
	a := ast.MCPCalledAssertion{Server: p.expectString(), Tool: p.expectString()}
	if p.at(lexer.TokenIdent) && p.peek().Value == "with" {
		p.advance() // with
		p.expect(lexer.TokenLBrace)
		for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) {
			if p.at(lexer.TokenComma) {
				p.advance()
				continue
			}
			name := p.advance().Value
			op := p.advance().Value
			a.Args = append(a.Args, ast.ArgPredicate{Name: name, Op: op, Value: p.parseLiteralValue()})
		}
		p.expect(lexer.TokenRBrace)
	}
	return a
}

// parseLiteralValue reads a single literal (string / number / bool) as a
// Go value, for mock returns and tool_called arg predicates.
func (p *parser) parseLiteralValue() any {
	switch p.peek().Type {
	case lexer.TokenString:
		return p.expectString()
	case lexer.TokenBool:
		return p.advance().Value == "true"
	case lexer.TokenIdent:
		return p.advance().Value
	default:
		s := p.expectNumberStr()
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	}
}

func (p *parser) parseTestDatum() ast.TestDatum {
	switch p.peek().Type {
	case lexer.TokenRecord:
		p.advance() // record
		id, _ := strconv.Atoi(p.expectNumberStr())
		fields := map[string]interface{}{}
		// Parse key value pairs: type "item" category "Vehicles" status "active"
		for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) &&
			!p.at(lexer.TokenRecord) && !p.at(lexer.TokenAttr) {
			key := p.advance().Value // type, category, status, or ident
			switch p.peek().Type {
			case lexer.TokenString:
				fields[key] = p.advance().Value
			case lexer.TokenNumber:
				n, _ := strconv.ParseFloat(p.advance().Value, 64)
				fields[key] = n
			case lexer.TokenBool:
				fields[key] = p.advance().Value == "true"
			default:
				p.errorf("expected value after %q in record, got %q", key, p.peek().Value)
				p.advance()
			}
		}
		return ast.TestDatum{Kind: "record", ID: id, Fields: fields}

	case lexer.TokenAttr:
		p.advance() // attr
		id, _ := strconv.Atoi(p.expectNumberStr())
		attrName := p.expectString()
		fields := map[string]interface{}{}
		switch p.peek().Type {
		case lexer.TokenString:
			fields[attrName] = p.advance().Value
		case lexer.TokenNumber:
			n, _ := strconv.ParseFloat(p.advance().Value, 64)
			fields[attrName] = n
		case lexer.TokenBool:
			fields[attrName] = p.advance().Value == "true"
		case lexer.TokenLBracket:
			fields[attrName] = p.parseTestListValue()
		default:
			p.errorf("expected value for attr %q, got %q", attrName, p.peek().Value)
		}
		return ast.TestDatum{Kind: "attr", ID: id, Fields: fields}

	default:
		p.errorf("expected 'record' or 'attr' inside given block, got %q", p.peek().Value)
		p.advance()
		return ast.TestDatum{}
	}
}

// parseTestListValue parses a `[ v, v, ... ]` literal in a given block. List
// attributes are what the string predicates quantify over (`contains` means
// "any element contains"), so a test file that cannot express one cannot
// exercise them. An empty list is legal and distinct from an unset attribute.
func (p *parser) parseTestListValue() []any {
	p.expect(lexer.TokenLBracket)
	out := []any{}
	for !p.at(lexer.TokenRBracket) && !p.at(lexer.TokenEOF) {
		if p.at(lexer.TokenComma) {
			p.advance()
			continue
		}
		switch p.peek().Type {
		case lexer.TokenString:
			out = append(out, p.advance().Value)
		case lexer.TokenNumber:
			n, _ := strconv.ParseFloat(p.advance().Value, 64)
			out = append(out, n)
		case lexer.TokenBool:
			out = append(out, p.advance().Value == "true")
		default:
			p.errorf("expected a string, number or boolean in list literal, got %q", p.peek().Value)
			p.advance()
		}
	}
	p.expect(lexer.TokenRBracket)
	return out
}

// parseActionAssertion parses `did <id> <verb> [arg ...]` or the `did_not`
// form. Args match positionally; `contains "x"` matches a substring of that
// argument instead of the whole value.
func (p *parser) parseActionAssertion() ast.ActionAssertion {
	tok := p.advance() // did | did_not
	a := ast.ActionAssertion{
		Negate: tok.Value == "did_not",
		Pos:    ast.Pos{Line: tok.Line, Col: tok.Col},
	}
	a.ID, _ = strconv.Atoi(p.expectNumberStr())
	// The verb is required. Without this guard a `did 1` with the verb omitted
	// eats the expect block's closing brace, which desyncs brace matching for
	// everything after this test.
	if p.at(lexer.TokenRBrace) || p.at(lexer.TokenEOF) || p.atTestAssertionBoundary() {
		p.errorf("expected an action verb after `%s %d`", tok.Value, a.ID)
		return a
	}
	a.Verb = p.advance().Value
	for !p.at(lexer.TokenRBrace) && !p.at(lexer.TokenEOF) && !p.atTestAssertionBoundary() {
		if p.at(lexer.TokenContains) {
			p.advance() // contains
			a.Args = append(a.Args, ast.ActionArgMatch{Contains: true, Value: p.expectString()})
			continue
		}
		before := p.pos
		a.Args = append(a.Args, ast.ActionArgMatch{Value: p.parseLiteralValue()})
		if p.pos == before {
			// parseLiteralValue consumed nothing — bail rather than spin.
			p.errorf("unexpected token %q in arguments to `%s %d %s`",
				p.peek().Value, tok.Value, a.ID, a.Verb)
			p.advance()
			break
		}
	}
	return a
}

// atTestAssertionBoundary reports whether the current token starts a new
// assertion, terminating the argument list of a did/did_not.
func (p *parser) atTestAssertionBoundary() bool {
	switch p.peek().Type {
	case lexer.TokenFlagged, lexer.TokenNot, lexer.TokenLabel,
		lexer.TokenPriority, lexer.TokenThreshold, lexer.TokenCount:
		return true
	case lexer.TokenIdent:
		switch p.peek().Value {
		case "did", "did_not", "tool_called", "score":
			return true
		}
	}
	return false
}

func (p *parser) parseTestAssertion() ast.TestAssertion {
	switch p.peek().Type {
	case lexer.TokenFlagged:
		p.advance() // flagged
		id, _ := strconv.Atoi(p.expectNumberStr())
		return ast.TestAssertion{Kind: "flagged", ID: id}

	case lexer.TokenNot:
		p.advance() // not
		if p.at(lexer.TokenFlagged) {
			p.advance() // flagged
			id, _ := strconv.Atoi(p.expectNumberStr())
			return ast.TestAssertion{Kind: "not_flagged", ID: id}
		}
		p.errorf("expected 'flagged' after 'not' in expect, got %q", p.peek().Value)
		p.advance()
		return ast.TestAssertion{}

	case lexer.TokenLabel:
		p.advance()             // label
		op := p.advance().Value // "contains", "==", etc.
		val := p.expectString()
		return ast.TestAssertion{Kind: "label", Op: op, Value: val}

	case lexer.TokenPriority:
		p.advance()              // priority
		op := p.advance().Value  // "=="
		val := p.advance().Value // HIGH, LOW, etc.
		return ast.TestAssertion{Kind: "priority", Op: op, Value: val}

	case lexer.TokenThreshold:
		p.advance()             // threshold
		op := p.advance().Value // "~=", ">", etc.
		val := p.advance().Value
		return ast.TestAssertion{Kind: "threshold", Op: op, Value: val}

	case lexer.TokenCount:
		// `count` is a keyword (the calculate METHOD), so it never reached
		// the IDENT branch below despite checkAssertion handling "count".
		p.advance()             // count
		op := p.advance().Value // "=="
		val := p.advance().Value
		return ast.TestAssertion{Kind: "count", Op: op, Value: val}

	case lexer.TokenIdent:
		// e.g. "count == 2" or "score 808 > 2.5"
		kind := p.advance().Value
		if kind == "score" {
			id, _ := strconv.Atoi(p.expectNumberStr())
			op := p.advance().Value
			val := p.advance().Value
			return ast.TestAssertion{Kind: "score", ID: id, Op: op, Value: val}
		}
		op := p.advance().Value
		val := p.advance().Value
		return ast.TestAssertion{Kind: kind, Op: op, Value: val}

	default:
		p.errorf("unexpected token %q inside expect block", p.peek().Value)
		p.advance()
		return ast.TestAssertion{}
	}
}

// exprToAttrName extracts a string attribute name from an expression node.
func exprToAttrName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.IdentExpr:
		return v.Name
	case *ast.AttrExpr:
		return v.Name
	}
	return ""
}
