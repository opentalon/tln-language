package lexer

import (
	"testing"
)

func lex(t *testing.T, src string) []Token {
	t.Helper()
	tokens, diags := Lex("test.tln", src)
	if diags.HasErrors() {
		t.Fatalf("unexpected lex errors: %v", diags)
	}
	return tokens
}

func tok(t *testing.T, tokens []Token, i int, wantType TokenType, wantValue string) {
	t.Helper()
	if i >= len(tokens) {
		t.Fatalf("token[%d]: out of range (len=%d)", i, len(tokens))
	}
	got := tokens[i]
	if got.Type != wantType || got.Value != wantValue {
		t.Errorf("token[%d]: got (%v, %q), want (%v, %q)", i, got.Type, got.Value, wantType, wantValue)
	}
}

func TestEOF(t *testing.T) {
	tokens := lex(t, "")
	if len(tokens) != 1 || tokens[0].Type != TokenEOF {
		t.Fatalf("empty source must produce only EOF, got %v", tokens)
	}
}

func TestKeywords(t *testing.T) {
	cases := []struct {
		src string
		tt  TokenType
	}{
		{"detect", TokenDetect},
		{"rule", TokenRule},
		{"recommend", TokenRecommend},
		{"combine", TokenCombine},
		{"define", TokenDefine},
		{"workflow", TokenWorkflow},
		{"predict", TokenPredict},
		{"forecast", TokenForecast},
		{"cluster", TokenCluster},
		{"classify", TokenClassify},
		{"decide", TokenDecide},
		{"choices", TokenChoices},
		{"ask", TokenAsk},
		{"find", TokenFind},
		{"for", TokenFor},
		{"where", TokenWhere},
		{"when", TokenWhen},
		{"and", TokenAnd},
		{"or", TokenOr},
		{"not", TokenNot},
		{"in", TokenIn},
		{"is", TokenIs},
		{"has", TokenHas},
		{"attr", TokenAttr},
		{"type", TokenTypeKw},
		{"flag", TokenFlag},
		{"label", TokenLabel},
		{"priority", TokenPriority},
		{"block", TokenBlock},
		{"allow", TokenAllow},
		{"reason", TokenReason},
		{"requires", TokenRequires},
		{"approval", TokenApproval},
		{"from", TokenFrom},
		{"role", TokenRole},
		{"before", TokenBefore},
		{"after", TokenAfter},
		{"every", TokenEvery},
		{"on", TokenOn},
		{"step", TokenStep},
		{"tool", TokenTool},
		{"anomaly", TokenAnomaly},
		{"compared_to", TokenComparedTo},
		{"series", TokenSeries},
		{"over", TokenOver},
		{"within", TokenWithin},
		{"similar", TokenSimilar},
		{"calculate", TokenCalculate},
		{"trained_on", TokenTrainedOn},
		{"features", TokenFeatures},
		{"confidence", TokenConfidence},
		{"days", TokenDays},
		{"weeks", TokenWeeks},
		{"months", TokenMonths},
		{"years", TokenYears},
		{"km", TokenKm},
		{"last", TokenLast},
		{"matching", TokenMatching},
		{"records", TokenRecords},
		{"items", TokenItems},
		{"each", TokenEach},
		{"today", TokenToday},
		{"changed_to", TokenChangedTo},
		{"contains", TokenContains},
		{"starts_with", TokenStartsWith},
		{"ends_with", TokenEndsWith},
		{"older_than", TokenOlderThan},
		{"newer_than", TokenNewerThan},
		{"was", TokenWas},
		{"ago", TokenAgo},
		{"having", TokenHaving},
		{"LOW", TokenLow},
		{"MEDIUM", TokenMedium},
		{"HIGH", TokenHigh},
		{"CRITICAL", TokenCritical},
		{"context", TokenContext},
		{"category_tree", TokenCategoryTree},
		{"suggest", TokenSuggest},
		{"return", TokenReturn},
	}
	for _, c := range cases {
		tokens := lex(t, c.src)
		tok(t, tokens, 0, c.tt, c.src)
	}
}

func TestBooleans(t *testing.T) {
	tokens := lex(t, "true")
	tok(t, tokens, 0, TokenBool, "true")

	tokens = lex(t, "false")
	tok(t, tokens, 0, TokenBool, "false")
}

func TestIdent(t *testing.T) {
	tokens := lex(t, "my_variable")
	tok(t, tokens, 0, TokenIdent, "my_variable")

	tokens = lex(t, "x")
	tok(t, tokens, 0, TokenIdent, "x")

	tokens = lex(t, "_private")
	tok(t, tokens, 0, TokenIdent, "_private")
}

func TestNumbers(t *testing.T) {
	tokens := lex(t, "42")
	tok(t, tokens, 0, TokenNumber, "42")

	tokens = lex(t, "3.14")
	tok(t, tokens, 0, TokenNumber, "3.14")

	tokens = lex(t, "0")
	tok(t, tokens, 0, TokenNumber, "0")

	tokens = lex(t, "50000")
	tok(t, tokens, 0, TokenNumber, "50000")
}

func TestStrings(t *testing.T) {
	tokens := lex(t, `"hello"`)
	tok(t, tokens, 0, TokenString, "hello")

	tokens = lex(t, `"brake_repair"`)
	tok(t, tokens, 0, TokenString, "brake_repair")

	// escape sequences
	tokens = lex(t, `"line1\nline2"`)
	tok(t, tokens, 0, TokenString, "line1\nline2")

	tokens = lex(t, `"say \"hi\""`)
	tok(t, tokens, 0, TokenString, `say "hi"`)

	// interpolations — lexer preserves them as-is in the string value
	tokens = lex(t, `"{item.name}: {attr.km} km"`)
	tok(t, tokens, 0, TokenString, "{item.name}: {attr.km} km")
}

func TestDurationUnits(t *testing.T) {
	tokens := lex(t, "30 days")
	tok(t, tokens, 0, TokenNumber, "30")
	tok(t, tokens, 1, TokenDays, "days")

	tokens = lex(t, "12 months")
	tok(t, tokens, 0, TokenNumber, "12")
	tok(t, tokens, 1, TokenMonths, "months")
}

func TestOperators(t *testing.T) {
	cases := []struct {
		src string
		tt  TokenType
		val string
	}{
		{"==", TokenEq, "=="},
		{"!=", TokenNeq, "!="},
		{">", TokenGt, ">"},
		{"<", TokenLt, "<"},
		{">=", TokenGte, ">="},
		{"<=", TokenLte, "<="},
		{"+", TokenPlus, "+"},
		{"-", TokenMinus, "-"},
		{"*", TokenStar, "*"},
		{"/", TokenSlash, "/"},
		{"%", TokenPercent, "%"},
	}
	for _, c := range cases {
		tokens := lex(t, c.src)
		tok(t, tokens, 0, c.tt, c.val)
	}
}

func TestDelimiters(t *testing.T) {
	tokens := lex(t, "{}[](),.")
	expected := []struct {
		tt  TokenType
		val string
	}{
		{TokenLBrace, "{"},
		{TokenRBrace, "}"},
		{TokenLBracket, "["},
		{TokenRBracket, "]"},
		{TokenLParen, "("},
		{TokenRParen, ")"},
		{TokenComma, ","},
		{TokenDot, "."},
	}
	for i, e := range expected {
		tok(t, tokens, i, e.tt, e.val)
	}
}

func TestLineCommentSkipped(t *testing.T) {
	tokens := lex(t, "// this is a comment\ndetect")
	tok(t, tokens, 0, TokenDetect, "detect")
	tok(t, tokens, 1, TokenEOF, "")
}

func TestBlockCommentSkipped(t *testing.T) {
	tokens := lex(t, "/* multi\nline\ncomment */detect")
	tok(t, tokens, 0, TokenDetect, "detect")
	tok(t, tokens, 1, TokenEOF, "")
}

func TestLineColTracking(t *testing.T) {
	tokens := lex(t, "detect\n  rule")
	if tokens[0].Line != 1 || tokens[0].Col != 1 {
		t.Errorf("detect: want line=1 col=1, got line=%d col=%d", tokens[0].Line, tokens[0].Col)
	}
	if tokens[1].Line != 2 || tokens[1].Col != 3 {
		t.Errorf("rule: want line=2 col=3, got line=%d col=%d", tokens[1].Line, tokens[1].Col)
	}
}

func TestMultipleErrors(t *testing.T) {
	_, diags := Lex("test.tln", "@ $")
	if !diags.HasErrors() {
		t.Fatal("expected errors for illegal characters")
	}
	var errCount int
	for _, d := range diags {
		if d.Severity == 0 { // diagnostic.Error = 0
			errCount++
		}
	}
	if errCount < 2 {
		t.Errorf("expected at least 2 errors for two illegal chars, got %d", errCount)
	}
}

func TestUnterminatedString(t *testing.T) {
	_, diags := Lex("test.tln", `"unterminated`)
	if !diags.HasErrors() {
		t.Fatal("expected error for unterminated string")
	}
}

func TestUnterminatedBlockComment(t *testing.T) {
	_, diags := Lex("test.tln", "/* not closed")
	if !diags.HasErrors() {
		t.Fatal("expected error for unterminated block comment")
	}
}

func TestDetectBlockTokens(t *testing.T) {
	src := `detect "Cement running low" {
  for records where type == "stock_item"
  flag matching items
  priority CRITICAL
}`
	tokens := lex(t, src)
	tok(t, tokens, 0, TokenDetect, "detect")
	tok(t, tokens, 1, TokenString, "Cement running low")
	tok(t, tokens, 2, TokenLBrace, "{")
	tok(t, tokens, 3, TokenFor, "for")
	tok(t, tokens, 4, TokenRecords, "records")
	tok(t, tokens, 5, TokenWhere, "where")
	tok(t, tokens, 6, TokenTypeKw, "type")
	tok(t, tokens, 7, TokenEq, "==")
	tok(t, tokens, 8, TokenString, "stock_item")
	tok(t, tokens, 9, TokenFlag, "flag")
	tok(t, tokens, 10, TokenMatching, "matching")
	tok(t, tokens, 11, TokenItems, "items")
	tok(t, tokens, 12, TokenPriority, "priority")
	tok(t, tokens, 13, TokenCritical, "CRITICAL")
	tok(t, tokens, 14, TokenRBrace, "}")
}
