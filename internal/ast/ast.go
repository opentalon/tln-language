package ast

// Pos is a source location, used for error reporting.
type Pos struct {
	Line int
	Col  int
}

// Program is the top-level AST node for a .tln file.
//
// Imports, when present, must appear before any block in the source.
// The resolver in internal/imports walks them, recursively lex+parses
// each target, and merges the resulting blocks into the current
// program before validation runs. See docs/spec/v0.2.md.
type Program struct {
	Imports []ImportStatement
	Blocks  []Block
}

// ImportStatement is `import "./path"` — a top-level directive that
// merges the named file's blocks into the current program. Paths are
// relative to the importing file. No version resolution, no git, no
// registry — the minimum viable module story per issue #19.
type ImportStatement struct {
	Pos  Pos
	Path string
}

// Block is implemented by all top-level block types.
type Block interface {
	blockNode()
	BlockName() string
}

// ─── Block types ──────────────────────────────────────────────────────────────

type DetectBlock struct {
	Pos        Pos
	Name       string
	Selector   Selector
	Pattern    *PatternExpr
	Flag       *FlagTarget
	Label      *Template
	Priority   *Priority
	Confidence *float64 // ML filter — accept matches only above this score
	Score      *float64 // provenance annotation — the rule's own confidence
	Source     *string  // provenance annotation — where the rule came from
	Calculate  []CalculateClause
	Having     []Condition // post-calculate filter; may reference calculate vars
	Anomaly    *AnomalyClause
	Predict    *PredictClause
	Forecast   *ForecastClause
	Cluster    *ClusterClause
	Similar    *SimilarClause
	Related    *RelatedClause
	Recommend  *RecommendBlock
	Tune       *TuneClause
	Remediate  *RemediateClause // mcp side-effects fired per flagged row
	Loggers    []*LoggerAction  // logger statements fired per matched row
}

// TuneClause names a labeled test fixture the executor uses to auto-tune
// the block's ML primitive parameters (z-threshold today; extensible to
// other primitives later) via Artificial Bee Colony optimization. See
// docs/optimizers/abc-tuning.md for the design.
type TuneClause struct {
	AgainstTest string // referenced test block name
}

type RuleBlock struct {
	Pos       Pos
	Name      string
	Selector  *Selector
	When      Condition
	Every     *EveryClause
	Before    *string
	After     *string
	Block     *string
	Allow     *string
	Requires  *RequiresClause
	Do        []*DoAction // host-executed verbs, in source order
	Reason    *Template
	Priority  *Priority
	Strict    bool            // strict rules cannot be overridden
	Overrides []string        // names of rules this rule defeats when both match
	Score     *float64        // provenance annotation — the rule's own confidence
	Source    *string         // provenance annotation — where the rule came from
	Loggers   []*LoggerAction // logger statements fired per matched row
}

type RecommendBlock struct {
	Pos       Pos
	Name      string
	When      Condition
	Calculate []CalculateClause
	Suggest   *Template
	// SuggestProbability gates whether Suggest fires on a given
	// matched row. 0 (zero value) means "always fire" — preserves
	// the historical behaviour. Values in (0, 1) gate via a
	// per-Run seeded RNG so the same Run is deterministic but
	// different Runs explore. Useful for ε-greedy rollouts of new
	// recommendation logic.
	SuggestProbability float64
	// FeedbackWindowDays, when > 0, switches the block to
	// adaptive ε-greedy: before sampling, the executor queries
	// recent accept/reject feedback facts and computes a Beta-
	// distributed posterior probability. Compile-time
	// SuggestProbability becomes the prior; observed feedback
	// shifts it. See docs/design/0005-mdp-feedback.md.
	FeedbackWindowDays int
	Priority           *Priority
	Remediate          *RemediateClause // mcp side-effects fired per matched row
	Loggers            []*LoggerAction  // logger statements fired per matched row
}

// RemediateClause holds the MCP calls a detect/recommend block fires as
// side effects when it matches — once per flagged row, with each call's
// args resolved against that row's entity. Calls run in order; if one
// fails, the remaining calls for that row are skipped.
type RemediateClause struct {
	Pos   Pos
	Mode  string // "auto" | "propose" (default) | "approve" | "queue"
	Role  string // required approver role, for Mode == "approve"
	Batch string // queue name, for Mode == "queue"
	Body  []Action
}

// Action is a statement in an imperative action body. Today that body is a
// `remediate` block; `on` reactive rules and workflow steps adopt the same
// grammar as their runtimes gain execution paths. Leaf actions perform an
// effect (an MCP call); the control-flow actions (IfAction, ForEachAction,
// WhileAction) nest further actions and branch/iterate over the row context.
// This is the language-level groundwork for self-hosting (issue #13): the
// imperative control flow a compiler needs, evaluated against the existing
// Condition grammar (no new expression language).
type Action interface{ actionNode() }

// MCPAction is a leaf action wrapping a single MCP side-effect call.
type MCPAction struct{ Call *MCPCall }

// IfAction branches on Cond (evaluated against the current row) and runs
// Then, otherwise Else (nil when there is no `else`). `else if` desugars to
// a single IfAction in Else.
type IfAction struct {
	Pos  Pos
	Cond Condition
	Then []Action
	Else []Action
}

// ForEachAction binds Variable to each element of Over (which must evaluate
// to a list — a `[...]` literal or a list-valued `attr "x"`) and runs Body
// once per element. Bounded by construction: the collection is finite.
type ForEachAction struct {
	Pos      Pos
	Variable string
	Over     Expr
	Body     []Action
}

// WhileAction runs Body while Cond holds, re-evaluating after each iteration.
// MaxIter is a hard safety cap: because the current per-row context has no
// mutable loop state, a stateless condition would otherwise spin forever, so
// the runtime errors once MaxIter is reached rather than looping unbounded.
type WhileAction struct {
	Pos     Pos
	Cond    Condition
	Body    []Action
	MaxIter int
}

// DefaultWhileMaxIter caps a `while` action's iterations when the source
// gives no explicit bound. Reaching it is a runtime error (see WhileAction).
const DefaultWhileMaxIter = 10000

func (*MCPAction) actionNode()     {}
func (*IfAction) actionNode()      {}
func (*ForEachAction) actionNode() {}
func (*WhileAction) actionNode()   {}

type CombineBlock struct {
	Pos         Pos
	Name        string
	Selector    Selector
	Optimize    []OptimizeClause
	Constraints []ConstraintClause
	Select      *SelectClause      // nil = rank individuals (v1); non-nil = pick subset of given size (v2)
	Seed        *int64             // optional deterministic seed for GA / ACO
	Sequence    bool               // true = ACO sequence mode (permutation)
	Coordinates *CoordinatesClause // coordinate attrs for distance calc in sequence mode
	Solver      string             // "" = auto (GA/Pareto), "linear" = ILP
	Return      []string
	Label       *Template
	Priority    *Priority
}

// CoordinatesClause names the two attrs that form a 2-D position used by ACO
// to compute pairwise euclidean distance along the sequence.
type CoordinatesClause struct {
	X Expr // *AttrExpr expected
	Y Expr // *AttrExpr expected
}

type DefineBlock struct {
	Pos        Pos
	Name       string
	Params     []string
	Conditions []Condition
	ForEach    *ForEachClause
}

type WorkflowBlock struct {
	Pos   Pos
	Name  string
	Steps []WorkflowStep
}

// Top-level ML blocks — also expressible as nested clauses inside detect.

type PredictBlock struct {
	Pos        Pos
	Name       string
	Selector   Selector
	Features   []Expr
	TrainedOn  *TrainedOnClause // labeled examples the tree trains on
	UsingModel string           // qualified model name; alternative to TrainedOn — the model's inline fitted tree drives prediction
	LabelAttr  string           // attribute on training rows holding the target class
	Confidence *float64         // minimum leaf purity to keep a prediction
	Label      *Template
	Priority   *Priority
}

type ForecastBlock struct {
	Pos      Pos
	Name     string
	Selector Selector
	Series   SeriesClause
	Predict  *ForecastPredictClause
	When     Condition
	Label    *Template
	Priority *Priority

	// MarkovTarget switches the forecast to a Markov-chain mode that
	// reads the entity's state-change history (events with
	// :event/state) and predicts the probability of reaching this
	// state from the current one within Predict.HorizonSteps state
	// transitions. Empty MarkovTarget = numeric/exp-smoothing mode
	// (the original ForecastBlock semantics).
	MarkovTarget string
}

type ClusterBlock struct {
	Pos      Pos
	Name     string
	Selector Selector
	ByAttrs  []Expr
	Label    *Template
	Priority *Priority
}

type ClassifyBlock struct {
	Pos        Pos
	Name       string
	Selector   Selector
	Features   []Expr
	TrainedOn  *TrainedOnClause // labeled examples the kNN vote draws from
	UsingModel string           // qualified model name (e.g. "fleet.ml.failure_risk"); alternative to TrainedOn — the model's inline fitted examples supply the training set
	LabelAttr  string           // attribute on training rows holding the class
	Confidence *float64         // minimum vote-ratio to keep a prediction
	Label      *Template
	Priority   *Priority
}

type SimilarBlock struct {
	Pos      Pos
	Name     string
	Selector Selector
	To       Expr
	Within   *float64
	Label    *Template
	Priority *Priority

	// VectorScope selects the talon-db HNSW scope to query when the
	// block uses `using vector scope "X"`. Empty means "fall back to
	// the structured-attribute cosine path" (the original similarity
	// flow). When set, the planner emits a VectorSimilarStep instead
	// of an MLComputation; the executor calls the FactStore-backed
	// VectorSearch RPC. TopK caps the result count (default 10).
	VectorScope string
	TopK        *int
}

// RelatedBlock is `find related "name" { ... }` — Personalized PageRank
// over an entity-attribute graph projected from the FactStore. Returns
// the top-K entities most associated with one or more seeds.
//
// Exactly one of (To, Seeds) is set: To carries a single seed expression;
// Seeds carries a list literal `[e1, e2, ...]`.
type RelatedBlock struct {
	Pos      Pos
	Name     string
	Selector Selector
	To       Expr
	Seeds    []Expr
	TopK     *int
	Damping  *float64
	Tol      *float64
	MaxIter  *int
	Label    *Template
	Priority *Priority
}

// OnBlock is a reactive rule: `on change attr "x" { ... }`, `on assert <type>
// { ... }`, or `on retract <type> { ... }`. The runtime fires the block when
// the FactStore emits a matching event. See docs/reactive.md.
type OnBlock struct {
	Pos      Pos
	Name     string // synthesized: "on change attr "x"" etc., used for diagnostics
	Trigger  string // "change", "assert", "retract"
	Attr     string // for change: the attribute name
	ToValue  Expr   // for change ... to <expr>: the target value (optional)
	FactType string // for assert/retract: the fact type (e.g. "record", "activity")
	When     Condition
	Actions  []OnAction
}

// OnAction is a single statement inside an on-block body.
type OnAction interface {
	onActionNode()
}

// LoggerAction is `logger.info|warn|error "<message template>"`.
type LoggerAction struct {
	Level   string // "info", "warn", "error"
	Message Template
}

// BlockRefAction is a named reference to another top-level block, e.g.
// `recommend "Order stock"`, `detect "Defective item without ticket"`,
// or `workflow "Refill stock"`.
type BlockRefAction struct {
	Kind string // "recommend", "detect", "workflow"
	Name string
}

func (*LoggerAction) onActionNode()   {}
func (*BlockRefAction) onActionNode() {}

// ConstraintBlock is an invariant checked on every fact mutation. See
// docs/constraints.md.
type ConstraintBlock struct {
	Pos         Pos
	Name        string
	Selector    Selector
	Require     Condition
	OnViolation ViolationClause
}

// StateMachineBlock declares a finite-state machine over a class of
// entities. Each matched entity carries a current state in StateAttr
// (default ":record/state"); transitions move the entity from one
// declared state to another when their guard condition holds.
// Substates (parent state qualified with "/") form a two-level
// hierarchy: in_flight/boarding, in_flight/cruising. A parent-state
// transition that targets the parent moves the entity into the
// parent's Initial substate; a transition from a parent matches any
// of its substates (Harel-style "outermost matches first").
type StateMachineBlock struct {
	Pos         Pos
	Name        string
	Selector    Selector
	States      []StateDecl
	Initial     string
	StateAttr   string // attribute holding current state; "" defaults to ":record/state"
	Transitions []Transition
	Invariants  []StateInvariant
}

// StateDecl is one state declaration. Substates name their parent via
// Parent ("" for top-level). Initial is the substate to enter when a
// transition targets the parent.
type StateDecl struct {
	Name    string
	Parent  string // "" for top-level
	Initial string // substate to enter when transitioning into a composite parent
}

// Transition is one labelled arrow in the state machine. When the
// entity is currently in From and When holds, write To into
// StateAttr. From may be a parent state — matches any of its
// substates.
type Transition struct {
	Pos  Pos
	From string
	To   string
	When Condition
}

// StateInvariant is an integrity check active only while the entity
// is in State. The semantics mirror ConstraintBlock.Require:
// Required must hold; on violation the runtime warns (we don't
// reject mutations from a state_machine block — that's what
// ConstraintBlock is for).
type StateInvariant struct {
	State    string
	Required Condition
}

// EventSequenceCondition matches entities whose event history
// contains the given ordered sequence of events within a sliding
// window. Each step is an event name (e.g. "cart_opened"); the
// runtime walks the entity's event facts (records with attribute
// :event/name and :event/at timestamp) ordered by time and checks
// that all Steps appear in order with relative gaps bounded by
// Window. Used inside selectors like:
//
//	for records where event_sequence "cart_opened" -> "item_added"
//	                  -> "abandoned" within 7 days
//
// Implemented as a regular automaton at runtime, hence the name —
// it's effectively a regex over event streams with one star-free
// pattern per step.
type EventSequenceCondition struct {
	Steps  []string
	Window Duration // upper bound on total elapsed time across the sequence
}

// RecordSequenceCondition is the detect-block `when` form for ordered
// record-of-type sequences across a grouping key. Syntax:
//
//	record type "electrical_fault"
//	  followed_by record type "engine_failure"
//	  [followed_by record type "C" ...]
//	  [on same IDENT]   // grouping attribute; defaults to "item"
//	  [within N <unit>] // upper bound on first→last span; 0 = unbounded
//
// Compiles to a RecordSequenceStep that runs per-candidate against the
// FactStore: for each candidate's grouping value (typically an item
// id), pull records of each step's type whose `:record/<On>` points at
// the candidate, walk them by `:record/at`, and look for an ordered
// match within Window.
type RecordSequenceCondition struct {
	Steps  []string // record types, in required order (length ≥ 2)
	On     string   // grouping attribute key (default "item")
	Window Duration // 0 = unbounded
}

type ViolationClause struct {
	Mode    string // "reject" | "warn" | "quarantine"
	Message string // optional; empty if not provided
}

func (*DetectBlock) blockNode()       {}
func (*RuleBlock) blockNode()         {}
func (*RecommendBlock) blockNode()    {}
func (*CombineBlock) blockNode()      {}
func (*DefineBlock) blockNode()       {}
func (*WorkflowBlock) blockNode()     {}
func (*PredictBlock) blockNode()      {}
func (*ForecastBlock) blockNode()     {}
func (*ClusterBlock) blockNode()      {}
func (*ClassifyBlock) blockNode()     {}
func (*SimilarBlock) blockNode()      {}
func (*RelatedBlock) blockNode()      {}
func (*OnBlock) blockNode()           {}
func (*ConstraintBlock) blockNode()   {}
func (*StateMachineBlock) blockNode() {}
func (*EnrichBlock) blockNode()       {}
func (*CollectBlock) blockNode()      {}
func (*ThresholdBlock) blockNode()    {}
func (*ConnectorBlock) blockNode()    {}
func (*DeriveBlock) blockNode()       {}
func (*ModelBlock) blockNode()        {}
func (*ModuleBlock) blockNode()       {}

func (b *DetectBlock) BlockName() string       { return b.Name }
func (b *RuleBlock) BlockName() string         { return b.Name }
func (b *RecommendBlock) BlockName() string    { return b.Name }
func (b *CombineBlock) BlockName() string      { return b.Name }
func (b *DefineBlock) BlockName() string       { return b.Name }
func (b *WorkflowBlock) BlockName() string     { return b.Name }
func (b *PredictBlock) BlockName() string      { return b.Name }
func (b *ForecastBlock) BlockName() string     { return b.Name }
func (b *ClusterBlock) BlockName() string      { return b.Name }
func (b *ClassifyBlock) BlockName() string     { return b.Name }
func (b *SimilarBlock) BlockName() string      { return b.Name }
func (b *RelatedBlock) BlockName() string      { return b.Name }
func (b *OnBlock) BlockName() string           { return b.Name }
func (b *ConstraintBlock) BlockName() string   { return b.Name }
func (b *StateMachineBlock) BlockName() string { return b.Name }
func (b *EnrichBlock) BlockName() string       { return b.Name }
func (b *CollectBlock) BlockName() string      { return b.Name }
func (b *ThresholdBlock) BlockName() string    { return b.Name }
func (b *ConnectorBlock) BlockName() string    { return b.Name }
func (b *DeriveBlock) BlockName() string       { return b.Name }
func (b *ModelBlock) BlockName() string        { return b.Name }
func (b *ModuleBlock) BlockName() string       { return "module " + b.Namespace }

// ModelBlock is a named ML model carrying inline fitted params (issue #13
// ML-module system). v1 is a lazy kNN classifier: the "fitted" params are
// the labeled examples themselves. A classify/predict block references it
// via `using model "<qualified name>"` instead of training inline.
type ModelBlock struct {
	Pos          Pos
	Name         string
	Algo         string          // "classify_knn" | "predict_decision_tree"
	K            int             // neighbours, for kNN
	Features     []Expr          // ordered feature attr references
	Examples     []FittedExample // kNN: the labeled points (lazy — this IS the model)
	Tree         []TreeNode      // decision tree: the fitted splits + leaves
	ComputedFrom string          // optional provenance (mirrors ThresholdBlock)
	ValidUntil   string          // optional expiry hint
}

// FittedExample is one labeled point in a model's inline fitted set: a
// feature vector (positionally aligned with the model's Features) and a class.
type FittedExample struct {
	Features []float64
	Label    string
}

// TreeNode is one node of an inline `fitted tree { ... }` decision tree. Nodes
// are flat and index-referenced (root = index 0). An internal node names a
// feature + threshold and its Left/Right child indices (feature ≤ threshold
// goes Left); a leaf carries a class and optional purity.
type TreeNode struct {
	Index     int
	Leaf      bool
	Class     string
	Purity    float64
	Feature   string
	Threshold float64
	Left      int
	Right     int
}

// ModuleBlock namespaces a set of exported members. An exported member named
// "m" inside `module "fleet.ml"` is referenceable as "fleet.ml.m". Members
// are the blocks declared with `export` in the module body.
type ModuleBlock struct {
	Pos       Pos
	Namespace string
	Members   []Block
}

// QualifiedName returns a module member's fully-qualified name.
func (b *ModuleBlock) QualifiedName(member string) string {
	return b.Namespace + "." + member
}

// CollectBlock is scheduled, host-driven MCP fact ingestion: on the
// declared Schedule, fetch a batch from an MCP tool and assert the
// results into the FactStore as records of type StoreAs (tagged Tag).
// tln does not run the scheduler — Schedule is metadata a host cron
// reads via `tln collect list`, and the host fires `tln collect run`.
type CollectBlock struct {
	Pos      Pos
	Name     string
	Schedule string // metadata: "weekly" | "daily" | "hourly" | "every N hours" | "cron:<expr>"
	Call     *MCPCall
	StoreAs  string // fact type for ingested records
	Tag      string // optional tag attribute value
}

// DeriveBlock is a Datalog-style derived predicate: `derive overdue(v) { for
// records where ... }` defines a boolean predicate over a single record,
// referenceable as `overdue(v)` in any other block's conditions. The planner
// inlines the body into the referencing query, so a derived predicate reads
// exactly like an asserted fact — closing the deductive cycle without host
// glue. See docs/derive.md and issue #91.
//
// v1 is arity-1 and non-recursive (the validator rejects cycles); recursive /
// arity-N derivations via the FactStore's recursive rule resolver are a
// tracked follow-up.
type DeriveBlock struct {
	Pos      Pos
	Name     string   // predicate name, e.g. "overdue"
	Var      string   // head variable, e.g. "v" (arity 1; cosmetic — always the record)
	Selector Selector // body: for records where <conditions>
}

// ThresholdBlock is a host-precomputed, cached threshold — the layer-2
// counterpart to the compute-on-demand `learned_threshold` expression. The
// host's discovery job writes it into a generated .tln file with provenance
// (ComputedFrom) and an expiry (ValidUntil); the runtime resolves a
// `threshold "name"` reference to Value at plan time (a single lookup, no
// per-eval series walk). See docs/thresholds.md and issue #74.
type ThresholdBlock struct {
	Pos          Pos
	Name         string
	Value        float64
	ComputedFrom string // optional provenance string
	ValidUntil   string // optional RFC 3339 / YYYY-MM-DD expiry; validator warns when past
}

// ConnectorBlock binds a tool-server name to the plugin that backs it, with
// plugin-specific config (ADR 0012). `connector "inventory" via mcp { endpoint
// env "X" bearer env "Y" }` — Name is the server referenced by `tool "Name"
// …`, Plugin is the backing plugin ("mcp", "io", …), and Config is free-form
// key→value the plugin interprets. Credentials/endpoints use [EnvExpr] so
// secrets stay out of source.
type ConnectorBlock struct {
	Pos    Pos
	Name   string
	Plugin string
	Config map[string]Expr
}

// EnrichBlock refreshes stale facts from an MCP tool. It selects records
// via Selector, and for each whose target attributes are older than
// StaleAfter (per the FactStore's Freshness capability), calls Call once
// and asserts the mapped response fields back via Updates. Host-driven:
// it runs when the program runs / an agent ticks it, not on fact reads.
type EnrichBlock struct {
	Pos        Pos
	Name       string
	Selector   Selector
	StaleAfter Duration
	Call       *MCPCall
	Updates    []UpdateClause
}

// UpdateClause maps an MCP response field back onto a fact: after Call,
// `update attr "current_stock" from result.current_stock` asserts the
// record's :attr/current_stock to response["current_stock"]. ResultPath
// is the dot path into the response ("current_stock", "data.level", ...).
type UpdateClause struct {
	Attr       string
	ResultPath string
}

// ─── Expressions ──────────────────────────────────────────────────────────────

type Expr interface {
	exprNode()
}

// AttrExpr is `attr "name"`.
type AttrExpr struct {
	Name string
}

// LiteralExpr holds a string, float64, bool, or Duration.
type LiteralExpr struct {
	Value interface{}
}

// IdentExpr is a bare identifier (variable, keyword-derived name).
type IdentExpr struct {
	Name string
}

type BinaryExpr struct {
	Left  Expr
	Op    string
	Right Expr
}

type UnaryExpr struct {
	Op      string
	Operand Expr
}

type ListExpr struct {
	Elements []Expr
}

// ContextExpr is `context.field`.
type ContextExpr struct {
	Field string
}

// StepResultExpr is `step("name").result.field`.
type StepResultExpr struct {
	StepName string
	Field    string
}

// CategoryTreeExpr is `category_tree("Root")`.
type CategoryTreeExpr struct {
	Root string
}

// ThresholdRefExpr is `threshold "name"` — a reference to a cached
// ThresholdBlock's value. The planner resolves it to the block's Value
// (a LiteralExpr) at plan time.
type ThresholdRefExpr struct {
	Name string
}

// EnvExpr is `env "VAR"` — a value resolved from the environment at run time
// (ADR 0012). It is valid ONLY inside connector config, for credentials and
// endpoints: the parser does not accept it in the general expression grammar,
// so an environment value can never flow into a label, a stored fact, or a tool
// argument. That confinement is a security boundary — secrets stay inside the
// connector that needs them and are never observable elsewhere.
type EnvExpr struct {
	Name string
}

// TodayExpr is the `today` keyword.
type TodayExpr struct{}

// MapExpr is `expr.map(field)` — extracts a field from each element of an array.
type MapExpr struct {
	Source Expr   // the array expression, e.g. step("find").result.items
	Field  string // the field to extract, e.g. "id"
}

// FindExpr is `expr.find(<cond>).<field>` — the FIRST element of an array result
// whose fields satisfy Cond, then Field navigated on it. Lets a workflow pick a
// specific record out of a list tool's result by a field value instead of a
// positional index — e.g.
// step("cats").ticket_categories.find(name == "Defect").id. Field is "" to
// return the whole matched element.
type FindExpr struct {
	Source Expr      // the array expression (e.g. a StepResultExpr)
	Cond   Condition // predicate evaluated per element (element fields as attrs)
	Field  string    // dot path read from the matched element ("" = the element)
}

// LearnedThresholdExpr is `learned_threshold METHOD of EXPR over last N UNIT`.
// Method is one of "p50", "p90", "p95", "p99" (or any "p<int>").
// Subject is the attr/ident expression whose historical values feed the threshold.
type LearnedThresholdExpr struct {
	Method  string
	Subject Expr
	Window  Duration
}

// CallExpr is a builtin function call in expression position, e.g.
// `upper(attr "code")`, `substring(attr "sku", 0, 2)`, `concat(a, "-", b)`.
// v1 is the string-builtin toolkit (issue #13 groundwork: a lexer/compiler
// is mostly string work). Func is one of the names in StringBuiltins.
type CallExpr struct {
	Func string
	Args []Expr
}

// stringBuiltins are the builtin string-valued functions callable as
// `name(args...)`. Kept as a set so the parser can tell a function call
// apart from a derived-predicate call (`overdue(v)`), and the evaluator
// can dispatch on the name.
//
//	upper(s) lower(s) trim(s)         → string
//	length(s)                         → number (rune count)
//	substring(s, start [, len])       → string (rune-safe)
//	replace(s, old, new)              → string (replace all)
//	concat(a, b, ...)                 → string (stringify + join)
//	split(s, sep)                     → list
//	join(list, sep)                   → string
//
// The date toolkit complements the `today` keyword and `date + N units`
// arithmetic (both already in the expression evaluator):
//
//	now()                             → date-time (current instant)
//	date(s)                           → date (coerce a string, so arithmetic /
//	                                    comparison works on any date field,
//	                                    not just `today`)
//	days_until(d)                     → number (whole days from today to d;
//	                                    negative when d is in the past)
//	days_between(a, b)                → number (whole days from a to b)
var stringBuiltins = map[string]bool{
	"upper": true, "lower": true, "trim": true, "length": true,
	"substring": true, "replace": true, "concat": true,
	"split": true, "join": true,
	"now": true, "date": true, "days_until": true, "days_between": true,
}

// IsStringBuiltin reports whether name is a builtin string function, so
// `name(` parses as a CallExpr rather than a derived-predicate call.
func IsStringBuiltin(name string) bool { return stringBuiltins[name] }

func (*AttrExpr) exprNode()             {}
func (*LiteralExpr) exprNode()          {}
func (*IdentExpr) exprNode()            {}
func (*BinaryExpr) exprNode()           {}
func (*UnaryExpr) exprNode()            {}
func (*ListExpr) exprNode()             {}
func (*ContextExpr) exprNode()          {}
func (*StepResultExpr) exprNode()       {}
func (*CategoryTreeExpr) exprNode()     {}
func (*TodayExpr) exprNode()            {}
func (*ThresholdRefExpr) exprNode()     {}
func (*EnvExpr) exprNode()              {}
func (*MapExpr) exprNode()              {}
func (*FindExpr) exprNode()             {}
func (*LearnedThresholdExpr) exprNode() {}
func (*CallExpr) exprNode()             {}

// ─── Conditions ───────────────────────────────────────────────────────────────

type Condition interface {
	condNode()
}

type CompareCondition struct {
	Left  Expr
	Op    string // "==", "!=", ">", "<", ">=", "<="
	Right Expr
}

type LogicalCondition struct {
	Op    string // "and", "or"
	Left  Condition
	Right Condition
}

type NotCondition struct {
	Inner Condition
}

type MembershipCondition struct {
	Expr    Expr
	Negated bool
	Members []Expr
}

// IsCondition is `is "define_name"` — resolves a define block by name.
type IsCondition struct {
	Subject Expr
	Name    string
}

type HasCondition struct {
	Subject Expr
	Type    string
}

type StringMatchCondition struct {
	Subject Expr
	Op      string // "contains", "starts_with", "ends_with", "matches", "matches_phrase"
	Value   string
}

// AnomalyCondition is `attr X is anomaly [using METHOD] compared_to last N days`.
// Method defaults to "zscore" when the optional `using` clause is omitted;
// the only other recognized value today is "grubbs" (Grubbs' single-outlier
// test). Validator + planner route to the matching mlruntime primitive.
type AnomalyCondition struct {
	Subject Expr
	Method  string // "zscore" (default) or "grubbs"
	Window  Duration
}

// CorrelationCondition is
// `attr "X" correlates_with attr "Y" over last N <unit> OP <threshold>`.
// It computes the Pearson correlation r between the two attributes across
// the matched record population and gates the set on `r OP Threshold`.
// Method is "pearson" (the only variant today).
type CorrelationCondition struct {
	Left      Expr
	Right     Expr
	Method    string // "pearson"
	Window    Duration
	Op        string // ">", "<", ">=", "<=", "==", "!="
	Threshold float64
}

// TemporalCondition is `attr X older_than 90 days`.
type TemporalCondition struct {
	Subject Expr
	Op      string // "older_than", "newer_than"
	Value   Duration
}

// ChangedToCondition is `status changed_to "value"`.
type ChangedToCondition struct {
	Attribute string
	Value     Expr
}

// AsOfCondition is `was ( <inner> ) N <unit> ago` — the inner condition
// held about the record N units before now. The planner evaluates Inner
// against a time-travel snapshot at now−Delta and intersects with the
// present-day candidates. Inner must be Datalog-expressible (no nested
// go-side conditions) and appear as a top-level AND conjunct.
type AsOfCondition struct {
	Inner Condition
	Delta Duration
}

// BlockMatchesCondition is `detect "name" matches` inside a recommend `when`.
type BlockMatchesCondition struct {
	Kind string // "detect", "predict", "forecast", etc.
	Name string
}

// PredicateCallCondition is `overdue(v)` — a reference to a derived predicate
// declared by a DeriveBlock. The planner inlines the predicate's body
// conditions into the referencing query. Var is the argument (arity 1).
type PredicateCallCondition struct {
	Name string
	Var  string
}

func (*CompareCondition) condNode()        {}
func (*LogicalCondition) condNode()        {}
func (*NotCondition) condNode()            {}
func (*MembershipCondition) condNode()     {}
func (*IsCondition) condNode()             {}
func (*HasCondition) condNode()            {}
func (*StringMatchCondition) condNode()    {}
func (*AnomalyCondition) condNode()        {}
func (*CorrelationCondition) condNode()    {}
func (*TemporalCondition) condNode()       {}
func (*ChangedToCondition) condNode()      {}
func (*AsOfCondition) condNode()           {}
func (*BlockMatchesCondition) condNode()   {}
func (*PredicateCallCondition) condNode()  {}
func (*EventSequenceCondition) condNode()  {}
func (*RecordSequenceCondition) condNode() {}

// ─── Supporting types ─────────────────────────────────────────────────────────

type Selector struct {
	Target     string // "records" or a type literal
	Conditions []Condition
}

type Priority int

const (
	PriorityLow      Priority = iota
	PriorityMedium   Priority = iota
	PriorityHigh     Priority = iota
	PriorityCritical Priority = iota
)

type Duration struct {
	Value int
	Unit  string // "days", "weeks", "months", "years", "km"
}

// Template is a string that may contain `{...}` interpolations. The
// language parser pre-parses Raw into Nodes so the validator can flag
// unknown functions and the renderer doesn't reparse on every call.
//
// Nodes is empty for templates constructed without going through
// ParseTemplate (e.g. when test code synthesises one). Renderers should
// fall back to Raw + a regex pass in that case for backward compat.
type Template struct {
	Raw   string
	Nodes []TemplateNode
}

// TemplateNode is implemented by LiteralNode, RefNode, FuncNode.
type TemplateNode interface {
	templateNode()
}

// LiteralNode is plain text between interpolations.
type LiteralNode struct {
	Text string
}

// RefNode is `{path}` — a dotted reference like `item.name`, `attr.km`,
// `context.role`. Renderers resolve against the matched row.
type RefNode struct {
	Path string
}

// FuncNode is `{fn(args...)}` — a template function call. Supported
// functions: count (no args), total/sum/avg/min/max (one attr.x arg),
// days_until / days_since (one date arg). Validator enforces the
// argument count.
type FuncNode struct {
	Fn   string
	Args []string // raw arg sources — e.g. "attr.km", "expires_at"
}

func (*LiteralNode) templateNode() {}
func (*RefNode) templateNode()     {}
func (*FuncNode) templateNode()    {}

// KnownTemplateFunctions enumerates the functions ParseTemplate accepts.
// Used by the validator to surface unknown-function diagnostics.
var KnownTemplateFunctions = map[string]struct{}{
	"count":      {},
	"total":      {},
	"sum":        {},
	"avg":        {},
	"min":        {},
	"max":        {},
	"days_until": {},
	"days_since": {},
}

// ParseTemplate parses a raw template string into a Template with Nodes
// populated. Unknown functions are still admitted into the AST — the
// validator decides whether to reject; renderers leave them as the
// original `{...}` literal so the user sees a hint of what was wrong.
//
// Grammar:
//
//	template      = { literal | interpolation }
//	interpolation = "{" ( funcCall | path ) "}"
//	funcCall      = ident "(" [ args ] ")"
//	args          = arg { "," arg }            // whitespace-tolerant
//	path          = ident { "." ident }
func ParseTemplate(raw string) Template {
	tmpl := Template{Raw: raw}
	if raw == "" {
		return tmpl
	}
	var nodes []TemplateNode
	var lit []byte
	flushLit := func() {
		if len(lit) > 0 {
			nodes = append(nodes, &LiteralNode{Text: string(lit)})
			lit = lit[:0]
		}
	}
	i := 0
	for i < len(raw) {
		c := raw[i]
		if c != '{' {
			lit = append(lit, c)
			i++
			continue
		}
		// Find matching '}'. Templates don't nest, so the next '}' is it.
		j := i + 1
		for j < len(raw) && raw[j] != '}' {
			j++
		}
		if j >= len(raw) {
			// Unterminated — treat the rest as a literal so the bad
			// source surfaces in the rendered output.
			lit = append(lit, raw[i:]...)
			break
		}
		body := raw[i+1 : j]
		flushLit()
		nodes = append(nodes, parseInterpolation(body))
		i = j + 1
	}
	flushLit()
	tmpl.Nodes = nodes
	return tmpl
}

// parseInterpolation classifies one `{...}` body as either a function
// call or a dotted reference. Bare known-function names (e.g. `{count}`)
// are admitted as zero-arg FuncNodes so the documented `{count}` form
// works without parens.
func parseInterpolation(body string) TemplateNode {
	body = trimASCIISpace(body)
	if body == "" {
		return &LiteralNode{Text: "{}"}
	}
	if open := indexByte(body, '('); open >= 0 && lastByte(body) == ')' {
		fn := trimASCIISpace(body[:open])
		argsRaw := trimASCIISpace(body[open+1 : len(body)-1])
		return &FuncNode{Fn: fn, Args: splitArgs(argsRaw)}
	}
	if _, ok := KnownTemplateFunctions[body]; ok {
		return &FuncNode{Fn: body}
	}
	return &RefNode{Path: body}
}

func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, trimASCIISpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, trimASCIISpace(s[start:]))
	return out
}

func trimASCIISpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func lastByte(s string) byte {
	if s == "" {
		return 0
	}
	return s[len(s)-1]
}

type FlagTarget struct {
	Kind string // "items", "records", or a custom type name
}

// PatternExpr covers multi-record patterns: "when 3+ records same category".
type PatternExpr struct {
	MinCount int
	GroupBy  string
	Window   *Duration
}

type EveryClause struct {
	Value  int
	Unit   string // "km", "days", etc.
	OnAttr string
}

type RequiresClause struct {
	What     string
	Approval *ApprovalExpr
}

type ApprovalExpr struct {
	Role string
}

// DoAction is one `do <verb> [arg ...]` clause on a rule. tln does not
// execute it and does not know what any verb means — the host does. The
// engine's job is to decide *which* actions fire, resolve their arguments
// against the matched row, and hand them back as data.
//
// Resolution lives in internal/actions, shared by the runtime (the fired
// actions land on executor.BlockResult.Actions) and the .tln.test runner,
// so an assertion and a host see the same payload. The engine's decision
// trace does not carry them yet.
//
// The verb vocabulary is therefore deliberately open. A host that understands
// `approve` / `block` / `comment` validates those names itself; the language
// only guarantees that an unknown verb reaches the host intact rather than
// being silently dropped.
type DoAction struct {
	Pos  Pos
	Verb string
	Args []Expr
}

type CalculateClause struct {
	Name   string
	From   string // "activities", "records"
	Value  Expr   // the `of attr "X"` value column; nil for count
	Method string // "average" (default), "sum", "count", "wma"
	Where  []Condition
	Within *Duration
}

// PredictClause is the nested ML predict inside a detect block.
type PredictClause struct {
	Features   []Expr
	TrainedOn  *TrainedOnClause
	Confidence *float64
}

type TrainedOnClause struct {
	Conditions []Condition
}

// ForecastClause is the nested ML forecast inside a detect block.
type ForecastClause struct {
	Series  SeriesClause
	Predict *ForecastPredictClause
}

type SeriesClause struct {
	Attr   Expr
	Window Duration
}

// ForecastPredictClause is `predict days_until value <= 0` inside a forecast.
type ForecastPredictClause struct {
	Variable  string
	Condition Condition
}

type AnomalyClause struct {
	Method string // "zscore" (default) or "grubbs"
	Window Duration
}

type ClusterClause struct {
	ByAttrs []Expr
}

type SimilarClause struct {
	To     Expr
	Within *float64
}

// RelatedClause is the nested form of `find related ...` inside a detect block.
type RelatedClause struct {
	To      Expr
	Seeds   []Expr
	TopK    *int
	Damping *float64
}

type OptimizeClause struct {
	Direction string // "minimize", "maximize"
	Attr      Expr   // may be *AggregateExpr in v2 subset mode
}

// SelectClause is `select K from records` inside a combine block.
// Marks the block as a subset-selection problem rather than per-row Pareto.
type SelectClause struct {
	Size int
}

// ConstraintClause is `subject_to AGG OP LITERAL`. v2 supports a single
// aggregate-vs-literal inequality form; richer expressions can land later
// without breaking this shape (Expr is general).
type ConstraintClause struct {
	Pos   Pos
	Left  Expr   // typically *AggregateExpr over selected subset
	Op    string // "<=", ">=", "<", ">", "==", "!="
	Right Expr   // typically *LiteralExpr (numeric)
}

// AggregateExpr is `total(...)`, `count(...)`, `avg(...)` evaluated over
// the selected subset's rows.
type AggregateExpr struct {
	Fn  string // "total", "count", "avg"
	Arg Expr   // typically *AttrExpr; for `count(records)` Arg may be nil
}

func (*AggregateExpr) exprNode() {}

type ForEachClause struct {
	Variable string
	Over     Expr
	Body     []Condition
}

type WorkflowStep struct {
	Name      string
	DependsOn []string
	MCPCall   *MCPCall
	// When is an optional guard: the step's MCP call runs only if the
	// condition holds. It may compare the triggering record's fields and/or a
	// prior step's result (step("search").total, length(step("search").items)),
	// enabling "search → decide → act" workflows such as dedup. nil = always run.
	When Condition
}

type MCPCall struct {
	Server  string
	Tool    string
	Args    map[string]Expr
	OnError *OnErrorClause // optional resilience policy; nil = fail on error
}

// OnErrorClause is the `on_error { ... }` resilience policy on an MCP
// call. Actions run in declared order: a RetryAction sets how many extra
// attempts to make before falling through; LogErrorAction / SkipAction /
// FailAction then decide the outcome. With no Skip/Fail, the error
// propagates (fail is the implicit default).
type OnErrorClause struct {
	Actions []ErrorAction
}

// ErrorAction is one clause inside on_error.
type ErrorAction interface{ errorActionNode() }

// RetryAction retries the call up to Times additional attempts before
// falling through to the remaining actions.
type RetryAction struct{ Times int }

// LogErrorAction writes a structured log line; Message may reference
// {error} plus the row context ({item.name}, {attr.x}).
type LogErrorAction struct{ Message Template }

// SkipAction swallows the failure and continues (the call is a no-op).
type SkipAction struct{}

// FailAction propagates the error (the implicit default, accepted
// explicitly for symmetry).
type FailAction struct{}

func (*RetryAction) errorActionNode()    {}
func (*LogErrorAction) errorActionNode() {}
func (*SkipAction) errorActionNode()     {}
func (*FailAction) errorActionNode()     {}

// ─── Test types ──────────────────────────────────────────────────────────────

// TestBlock is a single test case in a .tln.test file.
type TestBlock struct {
	Pos       Pos
	Name      string
	Given     []TestDatum
	WhenKind  string // "detect", "rule", "forecast", etc.
	WhenBlock string // block name
	Expect    []TestAssertion
	Mocks     []MockClause         // `mock tool ...` stubs installed before `when`
	MCPCalls  []MCPCalledAssertion // `tool_called ...` assertions inside `expect`
	Actions   []ActionAssertion    // `did` / `did_not` assertions inside `expect`
}

// ActionAssertion checks the `do` actions a rule produced for one row:
// `did <id> <verb> [arg ...]` / `did_not <id> <verb> [arg ...]`.
//
// With no args it asserts only that the verb fired for that row. With args it
// matches positionally; a `contains "substr"` arg matches a substring instead
// of the whole value, which is what makes an assertion on an interpolated
// comment readable.
type ActionAssertion struct {
	Pos    Pos
	Negate bool
	ID     int
	Verb   string
	Args   []ActionArgMatch
}

// ActionArgMatch is one positional argument matcher inside a did/did_not
// assertion. Contains selects substring matching over equality.
type ActionArgMatch struct {
	Contains bool
	Value    any
}

// MockClause stubs one MCP tool for a test: `mock tool "server" "tool" {
// returns { k v ... } | fails "msg" | fails after N }`. Returns holds the
// canned response; Fails makes the call error (after FailAfter successes,
// if set).
type MockClause struct {
	Server    string
	Tool      string
	Returns   map[string]any
	Fails     bool
	FailMsg   string
	FailAfter int
}

// MCPCalledAssertion checks that the block called an MCP tool, optionally
// constraining the arguments: `tool_called "server" "tool" [with { name OP
// value ... }]`.
type MCPCalledAssertion struct {
	Server string
	Tool   string
	Args   []ArgPredicate
}

// ArgPredicate is one `name OP value` check inside `tool_called ... with`.
// Op is one of "==", "!=", "contains".
type ArgPredicate struct {
	Name  string
	Op    string
	Value any
}

func (*TestBlock) blockNode()          {}
func (b *TestBlock) BlockName() string { return b.Name }

// TestDatum is one line inside a given { } block.
type TestDatum struct {
	Kind   string                 // "record" or "attr"
	ID     int                    // record/entity ID
	Fields map[string]interface{} // key → value pairs
}

// TestAssertion is one line inside an expect { } block.
type TestAssertion struct {
	Kind  string // "flagged", "not_flagged", "label", "priority", "count"
	ID    int    // entity ID (for flagged/not_flagged)
	Op    string // "contains", "==", etc.
	Value string // expected value
}
