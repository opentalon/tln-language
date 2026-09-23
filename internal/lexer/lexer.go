package lexer

import (
	"fmt"
	"unicode"

	"github.com/opentalon/tln-language/internal/diagnostic"
)

type TokenType int

const (
	// Literals
	TokenString TokenType = iota
	TokenNumber
	TokenBool
	TokenDuration // reserved; lexer emits NUMBER + unit separately

	// Delimiters
	TokenLBrace
	TokenRBrace
	TokenLBracket
	TokenRBracket
	TokenLParen
	TokenRParen
	TokenComma
	TokenDot

	// Operators
	TokenEq
	TokenNeq
	TokenLt
	TokenLte
	TokenGt
	TokenGte
	TokenPlus
	TokenMinus
	TokenStar
	TokenSlash
	TokenPercent
	TokenApprox

	// Keywords — block types
	TokenDetect
	TokenRule
	TokenRecommend
	TokenCombine
	TokenDefine
	TokenWorkflow
	TokenPredict
	TokenForecast
	TokenCluster
	TokenClassify
	TokenFind

	// Keywords — clauses
	TokenFor
	TokenWhere
	TokenWhen
	TokenAnd
	TokenOr
	TokenNot
	TokenIn
	TokenIs
	TokenHas
	TokenAttr
	TokenTypeKw
	TokenCategory
	TokenStatus
	TokenFlag
	TokenLabel
	TokenPriority
	TokenBlock
	TokenAllow
	TokenReason
	TokenDo
	TokenAction
	TokenSuggest
	TokenReturn
	TokenBest
	TokenMinimize
	TokenMaximize
	TokenSelect
	TokenSubjectTo
	TokenTotal
	TokenCount
	TokenAvg
	TokenSeed
	TokenSequence
	TokenCoordinates
	TokenSolver
	TokenLinear
	TokenTune
	TokenRemediate
	TokenEnrich
	TokenStaleAfter
	TokenUpdate
	TokenOnError
	TokenMock
	TokenCollect
	TokenSchedule
	TokenStore
	TokenHasOpen
	TokenHasExpired
	TokenApproaching
	TokenAgainst
	TokenRequires
	TokenApproval
	TokenFrom
	TokenRole
	TokenBefore
	TokenAfter
	TokenEvery
	TokenOn
	TokenStep
	TokenDepends
	TokenTool
	TokenConnector
	TokenVia
	TokenEnv
	TokenInvoke

	// Keywords — control flow (issue #13 groundwork): imperative if/else,
	// for-each, and bounded while inside action bodies (remediate today).
	TokenIf
	TokenElse
	TokenWhile

	// Keywords — ML modules: a `module` namespaces `export`ed members; a
	// `model` block carries inline `fitted` examples; classify/predict
	// reference one via `using model "ns.name"`.
	TokenModule
	TokenModel
	TokenExport
	TokenFitted
	TokenExample

	// Keywords — ML
	TokenAnomaly
	TokenCorrelatesWith
	TokenUsing
	TokenComparedTo
	TokenSeries
	TokenOver
	TokenWithin
	TokenSame
	TokenSimilar
	TokenRelated
	TokenCalculate
	TokenThreshold
	TokenLearnedThreshold
	TokenTrainedOn
	TokenFeatures
	TokenConfidence
	TokenClassifyKw
	TokenDecide  // "decide" block head — typed decisions
	TokenChoices // "choices [ ... ]" — decide's fixed choice set
	TokenAsk     // "ask <expr>" — decide model-mode state expression

	// Keywords — values/units
	TokenDays
	TokenWeeks
	TokenMonths
	TokenYears
	TokenKm
	TokenLast
	TokenNext
	TokenMatching
	TokenRecords
	TokenItems
	TokenEach
	TokenToday
	TokenChangedTo

	// Keywords — string ops
	TokenContains
	TokenStartsWith
	TokenEndsWith
	TokenMatches
	TokenOlderThan
	TokenNewerThan
	TokenFollowedBy
	TokenWas
	TokenAgo
	TokenHaving

	// Keywords — priority values
	TokenLow
	TokenMedium
	TokenHigh
	TokenCritical

	// Keywords — misc
	TokenContext
	TokenCategoryTree

	// Keywords — defeasible / reactive / constraint
	// These are the only new reserved words; the rest of the new vocabulary
	// (change, assert, retract, on_violation, reject, warn, quarantine,
	// logger, to, info, error) is matched contextually as TokenIdent by
	// string value, to keep the keyword set small.
	TokenStrict
	TokenOverrides
	TokenConstraint
	TokenRequire

	// Keywords — state machines + Markov primitives (this PR)
	TokenStateMachine
	TokenStates
	TokenInitial
	TokenTransition
	TokenInvariant
	TokenArrow     // "->" in transition declarations
	TokenStateAttr // "state_attr <name>"
	TokenEvent     // event_sequence keyword head
	TokenHmm       // anomaly method
	TokenProb      // "with probability N"
	TokenLabelAttr // "label_attr <name>" — classify's training label column
	TokenComputedFrom
	TokenValidUntil
	TokenDerive // "derive p(X) { ... }" — derived-predicate block

	// Keywords — local imports (#19 follow-up)
	TokenImport

	// Keywords — provenance annotations on rules + detect blocks
	// (introduced for issue #3 layer-3 auto-discovered rules).
	TokenSource

	// Keywords — test blocks
	TokenTest
	TokenGiven
	TokenExpect
	TokenFlagged
	TokenRecord

	// Special
	TokenIdent
	TokenEOF
	TokenIllegal
)

var keywords = map[string]TokenType{
	"detect":            TokenDetect,
	"rule":              TokenRule,
	"recommend":         TokenRecommend,
	"combine":           TokenCombine,
	"define":            TokenDefine,
	"workflow":          TokenWorkflow,
	"predict":           TokenPredict,
	"forecast":          TokenForecast,
	"cluster":           TokenCluster,
	"classify":          TokenClassify,
	"find":              TokenFind,
	"for":               TokenFor,
	"where":             TokenWhere,
	"when":              TokenWhen,
	"and":               TokenAnd,
	"or":                TokenOr,
	"not":               TokenNot,
	"in":                TokenIn,
	"is":                TokenIs,
	"has":               TokenHas,
	"attr":              TokenAttr,
	"type":              TokenTypeKw,
	"category":          TokenCategory,
	"status":            TokenStatus,
	"flag":              TokenFlag,
	"label":             TokenLabel,
	"priority":          TokenPriority,
	"block":             TokenBlock,
	"allow":             TokenAllow,
	"reason":            TokenReason,
	"do":                TokenDo,
	"action":            TokenAction,
	"suggest":           TokenSuggest,
	"return":            TokenReturn,
	"best":              TokenBest,
	"minimize":          TokenMinimize,
	"maximize":          TokenMaximize,
	"select":            TokenSelect,
	"subject_to":        TokenSubjectTo,
	"total":             TokenTotal,
	"count":             TokenCount,
	"avg":               TokenAvg,
	"seed":              TokenSeed,
	"sequence":          TokenSequence,
	"coordinates":       TokenCoordinates,
	"solver":            TokenSolver,
	"linear":            TokenLinear,
	"tune":              TokenTune,
	"remediate":         TokenRemediate,
	"enrich":            TokenEnrich,
	"stale_after":       TokenStaleAfter,
	"update":            TokenUpdate,
	"on_error":          TokenOnError,
	"mock":              TokenMock,
	"collect":           TokenCollect,
	"schedule":          TokenSchedule,
	"store":             TokenStore,
	"has_open":          TokenHasOpen,
	"has_expired":       TokenHasExpired,
	"approaching":       TokenApproaching,
	"against":           TokenAgainst,
	"requires":          TokenRequires,
	"approval":          TokenApproval,
	"from":              TokenFrom,
	"role":              TokenRole,
	"before":            TokenBefore,
	"after":             TokenAfter,
	"every":             TokenEvery,
	"on":                TokenOn,
	"step":              TokenStep,
	"depends_on":        TokenDepends,
	"tool":              TokenTool,
	"connector":         TokenConnector,
	"via":               TokenVia,
	"env":               TokenEnv,
	"invoke":            TokenInvoke,
	"if":                TokenIf,
	"else":              TokenElse,
	"while":             TokenWhile,
	"module":            TokenModule,
	"model":             TokenModel,
	"export":            TokenExport,
	"fitted":            TokenFitted,
	"example":           TokenExample,
	"anomaly":           TokenAnomaly,
	"correlates_with":   TokenCorrelatesWith,
	"using":             TokenUsing,
	"compared_to":       TokenComparedTo,
	"series":            TokenSeries,
	"over":              TokenOver,
	"within":            TokenWithin,
	"same":              TokenSame,
	"similar":           TokenSimilar,
	"related":           TokenRelated,
	"calculate":         TokenCalculate,
	"threshold":         TokenThreshold,
	"learned_threshold": TokenLearnedThreshold,
	"trained_on":        TokenTrainedOn,
	"features":          TokenFeatures,
	"confidence":        TokenConfidence,
	"decide":            TokenDecide,
	"choices":           TokenChoices,
	"ask":               TokenAsk,
	"days":              TokenDays,
	"weeks":             TokenWeeks,
	"months":            TokenMonths,
	"years":             TokenYears,
	"km":                TokenKm,
	"last":              TokenLast,
	"next":              TokenNext,
	"matching":          TokenMatching,
	"records":           TokenRecords,
	"items":             TokenItems,
	"each":              TokenEach,
	"today":             TokenToday,
	"changed_to":        TokenChangedTo,
	"contains":          TokenContains,
	"starts_with":       TokenStartsWith,
	"ends_with":         TokenEndsWith,
	"matches":           TokenMatches,
	"older_than":        TokenOlderThan,
	"newer_than":        TokenNewerThan,
	"followed_by":       TokenFollowedBy,
	"was":               TokenWas,
	"ago":               TokenAgo,
	"having":            TokenHaving,
	"LOW":               TokenLow,
	"MEDIUM":            TokenMedium,
	"HIGH":              TokenHigh,
	"CRITICAL":          TokenCritical,
	"context":           TokenContext,
	"category_tree":     TokenCategoryTree,
	"true":              TokenBool,
	"false":             TokenBool,
	"test":              TokenTest,
	"given":             TokenGiven,
	"expect":            TokenExpect,
	"flagged":           TokenFlagged,
	"record":            TokenRecord,
	"strict":            TokenStrict,
	"overrides":         TokenOverrides,
	"constraint":        TokenConstraint,
	"require":           TokenRequire,
	"source":            TokenSource,
	"import":            TokenImport,

	"state_machine":  TokenStateMachine,
	"states":         TokenStates,
	"initial":        TokenInitial,
	"transition":     TokenTransition,
	"invariant":      TokenInvariant,
	"state_attr":     TokenStateAttr,
	"event_sequence": TokenEvent,
	"hmm":            TokenHmm,
	"probability":    TokenProb,
	"label_attr":     TokenLabelAttr,
	"computed_from":  TokenComputedFrom,
	"valid_until":    TokenValidUntil,
	"derive":         TokenDerive,
}

type Token struct {
	Type  TokenType
	Value string
	Line  int
	Col   int
}

type scanner struct {
	file   string
	source []rune
	pos    int
	line   int
	col    int
	tokens []Token
	diags  diagnostic.List
}

func Lex(file, source string) ([]Token, diagnostic.List) {
	s := &scanner{
		file:   file,
		source: []rune(source),
		line:   1,
		col:    1,
	}
	s.scan()
	return s.tokens, s.diags
}

func (s *scanner) scan() {
	for !s.atEnd() {
		s.skipWhitespace()
		if s.atEnd() {
			break
		}
		line, col := s.line, s.col
		ch := s.peek()
		switch {
		case ch == '/' && s.peek2() == '/':
			s.scanLineComment()
		case ch == '/' && s.peek2() == '*':
			s.scanBlockComment(line, col)
		case ch == '"':
			s.scanString(line, col)
		case unicode.IsDigit(ch):
			s.scanNumber(line, col)
		case unicode.IsLetter(ch) || ch == '_':
			s.scanIdent(line, col)
		default:
			s.scanSymbol(line, col)
		}
	}
	s.tokens = append(s.tokens, Token{Type: TokenEOF, Line: s.line, Col: s.col})
}

func (s *scanner) scanString(line, col int) {
	s.advance() // opening "
	var buf []rune
	for !s.atEnd() {
		ch := s.peek()
		if ch == '"' {
			s.advance()
			s.emit(TokenString, string(buf), line, col)
			return
		}
		if ch == '\n' {
			s.diags.AddError(s.file, line, col, "unterminated string literal", "strings cannot span multiple lines")
			return
		}
		if ch == '\\' {
			s.advance()
			if s.atEnd() {
				s.diags.AddError(s.file, s.line, s.col, "unterminated string escape", "")
				return
			}
			esc := s.advance()
			switch esc {
			case '"':
				buf = append(buf, '"')
			case '\\':
				buf = append(buf, '\\')
			case 'n':
				buf = append(buf, '\n')
			case 't':
				buf = append(buf, '\t')
			case 'r':
				buf = append(buf, '\r')
			default:
				s.diags.AddError(s.file, s.line, s.col-1, fmt.Sprintf("unknown escape sequence \\%c", esc), "")
				buf = append(buf, esc)
			}
			continue
		}
		buf = append(buf, s.advance())
	}
	s.diags.AddError(s.file, line, col, "unterminated string literal", "")
}

func (s *scanner) scanNumber(line, col int) {
	var buf []rune
	for !s.atEnd() && unicode.IsDigit(s.peek()) {
		buf = append(buf, s.advance())
	}
	if !s.atEnd() && s.peek() == '.' && s.peek2() != 0 && unicode.IsDigit(s.peek2()) {
		buf = append(buf, s.advance()) // .
		for !s.atEnd() && unicode.IsDigit(s.peek()) {
			buf = append(buf, s.advance())
		}
	}
	s.emit(TokenNumber, string(buf), line, col)
}

func (s *scanner) scanIdent(line, col int) {
	var buf []rune
	for !s.atEnd() && (unicode.IsLetter(s.peek()) || unicode.IsDigit(s.peek()) || s.peek() == '_') {
		buf = append(buf, s.advance())
	}
	word := string(buf)
	if tt, ok := keywords[word]; ok {
		s.emit(tt, word, line, col)
	} else {
		s.emit(TokenIdent, word, line, col)
	}
}

func (s *scanner) scanSymbol(line, col int) {
	ch := s.advance()
	switch ch {
	case '{':
		s.emit(TokenLBrace, "{", line, col)
	case '}':
		s.emit(TokenRBrace, "}", line, col)
	case '[':
		s.emit(TokenLBracket, "[", line, col)
	case ']':
		s.emit(TokenRBracket, "]", line, col)
	case '(':
		s.emit(TokenLParen, "(", line, col)
	case ')':
		s.emit(TokenRParen, ")", line, col)
	case ',':
		s.emit(TokenComma, ",", line, col)
	case '.':
		s.emit(TokenDot, ".", line, col)
	case '+':
		s.emit(TokenPlus, "+", line, col)
	case '-':
		// `->` is the state-machine transition arrow; everything else
		// stays minus (arithmetic). The lookahead is tiny so this is
		// cheap; without it `pending -> approved` would lex as MINUS
		// MINUS GT which the parser can't disambiguate.
		if !s.atEnd() && s.peek() == '>' {
			s.advance()
			s.emit(TokenArrow, "->", line, col)
		} else {
			s.emit(TokenMinus, "-", line, col)
		}
	case '*':
		s.emit(TokenStar, "*", line, col)
	case '/':
		s.emit(TokenSlash, "/", line, col)
	case '%':
		s.emit(TokenPercent, "%", line, col)
	case '>':
		if !s.atEnd() && s.peek() == '=' {
			s.advance()
			s.emit(TokenGte, ">=", line, col)
		} else {
			s.emit(TokenGt, ">", line, col)
		}
	case '<':
		if !s.atEnd() && s.peek() == '=' {
			s.advance()
			s.emit(TokenLte, "<=", line, col)
		} else {
			s.emit(TokenLt, "<", line, col)
		}
	case '=':
		if !s.atEnd() && s.peek() == '=' {
			s.advance()
			s.emit(TokenEq, "==", line, col)
		} else {
			s.diags.AddError(s.file, line, col, "unexpected character '='", "did you mean '=='?")
			s.emit(TokenIllegal, "=", line, col)
		}
	case '!':
		if !s.atEnd() && s.peek() == '=' {
			s.advance()
			s.emit(TokenNeq, "!=", line, col)
		} else {
			s.diags.AddError(s.file, line, col, "unexpected character '!'", "did you mean '!='?")
			s.emit(TokenIllegal, "!", line, col)
		}
	case '~':
		if !s.atEnd() && s.peek() == '=' {
			s.advance()
			s.emit(TokenApprox, "~=", line, col)
		} else {
			s.diags.AddError(s.file, line, col, "unexpected character '~'", "did you mean '~='?")
			s.emit(TokenIllegal, "~", line, col)
		}
	default:
		s.diags.AddError(s.file, line, col, fmt.Sprintf("unexpected character %q", ch), "")
		s.emit(TokenIllegal, string(ch), line, col)
	}
}

func (s *scanner) scanLineComment() {
	for !s.atEnd() && s.peek() != '\n' {
		s.advance()
	}
}

func (s *scanner) scanBlockComment(line, col int) {
	s.advance() // /
	s.advance() // *
	for !s.atEnd() {
		if s.peek() == '*' && s.peek2() == '/' {
			s.advance() // *
			s.advance() // /
			return
		}
		s.advance()
	}
	s.diags.AddError(s.file, line, col, "unterminated block comment", "")
}

func (s *scanner) peek() rune {
	if s.atEnd() {
		return 0
	}
	return s.source[s.pos]
}

func (s *scanner) peek2() rune {
	if s.pos+1 >= len(s.source) {
		return 0
	}
	return s.source[s.pos+1]
}

func (s *scanner) advance() rune {
	ch := s.source[s.pos]
	s.pos++
	if ch == '\n' {
		s.line++
		s.col = 1
	} else {
		s.col++
	}
	return ch
}

func (s *scanner) atEnd() bool {
	return s.pos >= len(s.source)
}

func (s *scanner) skipWhitespace() {
	for !s.atEnd() && unicode.IsSpace(s.peek()) {
		s.advance()
	}
}

func (s *scanner) emit(tt TokenType, value string, line, col int) {
	s.tokens = append(s.tokens, Token{Type: tt, Value: value, Line: line, Col: col})
}
