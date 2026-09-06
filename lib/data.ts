export interface SymbolNode {
  id: string;
  name: string;
  kind: "function" | "class" | "method" | "interface" | "type" | "variable" | "route";
  language: "typescript" | "go" | "python" | "csharp" | "rust" | "java";
  signature: string;
  path: string;
  startLine: number;
  endLine: number;
  container?: string;
  codeSnippet: string;
  docstring?: string;
}

export interface RelationEdge {
  id: string;
  fromId: string;
  toId: string;
  fromName: string;
  toName: string;
  relation: "CALLS" | "IMPORTS" | "EXTENDS" | "HANDLES_ROUTE" | "USES_TYPE" | "PARAM_TYPE" | "RETURNS_TYPE";
  confidence: "EXACT" | "HEURISTIC" | "UNRESOLVED";
  callsite: string;
}

export interface BenchmarkSystem {
  name: string;
  locomoScore: number;
  indexTokens: string;
  version: string;
  notes: string;
  isLeader?: boolean;
}

export interface EntityDiffItem {
  id: string;
  type: "SIGNATURE_CHANGED" | "BODY_CHANGED" | "ADDED" | "RENAMED" | "MOVED" | "REMOVED";
  kind: string;
  name: string;
  path: string;
  oldPath?: string;
  oldSignature?: string;
  newSignature?: string;
  dependentsCount: number;
  riskLevel: "CRITICAL" | "HIGH" | "MEDIUM" | "LOW";
  reconciliation?: string;
  explanation: string;
}

export const INITIAL_SYMBOLS: SymbolNode[] = [
  {
    id: "internal/sem/analyze.go::AnalyzeGitRangeWithOptions",
    name: "AnalyzeGitRangeWithOptions",
    kind: "function",
    language: "go",
    signature: "func AnalyzeGitRangeWithOptions(ctx context.Context, repo string, base, head string, opts AnalyzeOptions) (*Result, error)",
    path: "internal/sem/analyze.go",
    startLine: 42,
    endLine: 118,
    docstring: "AnalyzeGitRangeWithOptions executes entity-level semantic diffing between two Git commits or worktrees.",
    codeSnippet: `func AnalyzeGitRangeWithOptions(ctx context.Context, repo string, base, head string, opts AnalyzeOptions) (*Result, error) {
	snapBase, err := BuildCommittedSnapshot(ctx, repo, base, opts.Profile)
	if err != nil {
		return nil, fmt.Errorf("failed to build base snapshot: %w", err)
	}
	snapHead, err := BuildCommittedSnapshot(ctx, repo, head, opts.Profile)
	if err != nil {
		return nil, fmt.Errorf("failed to build head snapshot: %w", err)
	}
	changes := ReconcileEntityDeltas(snapBase, snapHead)
	return &Result{
		Base:          base,
		Head:          head,
		Files:         changes,
		SchemaVersion: SchemaVersionV1,
	}, nil
}`
  },
  {
    id: "internal/sem/search.go::SearchRepo",
    name: "SearchRepo",
    kind: "function",
    language: "go",
    signature: "func SearchRepo(ctx context.Context, repo string, query string, opts SearchOptions) (*SearchResults, error)",
    path: "internal/sem/search.go",
    startLine: 85,
    endLine: 154,
    docstring: "SearchRepo provides ranked source retrieval with byte budgeting and hybrid identifier/body match scoring.",
    codeSnippet: `func SearchRepo(ctx context.Context, repo string, query string, opts SearchOptions) (*SearchResults, error) {
	terms := TokenizeQuery(query)
	index, err := GetOrBuildCacheIndex(repo, opts.Profile)
	if err != nil {
		return nil, err
	}
	candidates := index.LookupCandidateSymbols(terms)
	ranked := RankWithGraphNeighborhood(candidates, terms, opts.TopK)
	return BudgetContextWindow(ranked, opts.MaxContextBytes), nil
}`
  },
  {
    id: "internal/cli/impact.go::ComputeImpact",
    name: "ComputeImpact",
    kind: "function",
    language: "go",
    signature: "func ComputeImpact(ctx context.Context, repo string, symbol string, opts ImpactOptions) (*ImpactReport, error)",
    path: "internal/cli/impact.go",
    startLine: 35,
    endLine: 95,
    docstring: "ComputeImpact calculates the full blast radius for a symbol including direct/transitive callers, type consumers, and co-changes.",
    codeSnippet: `func ComputeImpact(ctx context.Context, repo string, symbol string, opts ImpactOptions) (*ImpactReport, error) {
	sym, err := DisambiguateSymbol(repo, symbol, opts.File)
	if err != nil {
		return nil, err
	}
	callers := ResolveTransitiveCallers(sym, opts.Depth)
	types := ResolveTypeDependents(sym)
	coChanges := GitHistoryCoChanges(repo, sym.Path, 50)
	return &ImpactReport{
		Symbol:        sym,
		DirectCallers: callers.Direct,
		Transitive:    callers.Transitive,
		TypeConsumers: types,
		CoChanges:     coChanges,
		BlastRadius:   calculateRadiusScore(callers, types),
	}, nil
}`
  },
  {
    id: "internal/sem/dependents.go::CountDependents",
    name: "CountDependents",
    kind: "function",
    language: "go",
    signature: "func CountDependents(graph *Graph, entity *Entity) int",
    path: "internal/sem/dependents.go",
    startLine: 28,
    endLine: 65,
    docstring: "CountDependents calculates downstream callers and type dependencies to compute impact risk on code modification.",
    codeSnippet: `func CountDependents(graph *Graph, entity *Entity) int {
	visited := make(map[string]bool)
	queue := []string{entity.ID()}
	count := 0
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, incoming := range graph.IncomingEdges(curr) {
			if !visited[incoming.FromID] {
				visited[incoming.FromID] = true
				queue = append(queue, incoming.FromID)
				count++
			}
		}
	}
	return count
}`
  },
  {
    id: "internal/sem/call_scanners.go::ResolveCallSites",
    name: "ResolveCallSites",
    kind: "function",
    language: "go",
    signature: "func ResolveCallSites(ast *tree_sitter.Node, src []byte, scope *ScopeTable) []CallSite",
    path: "internal/sem/call_scanners.go",
    startLine: 50,
    endLine: 110,
    docstring: "ResolveCallSites traverses Tree-Sitter AST nodes to link identifier invocations to qualified symbol definitions.",
    codeSnippet: `func ResolveCallSites(ast *tree_sitter.Node, src []byte, scope *ScopeTable) []CallSite {
	var sites []CallSite
	WalkAST(ast, func(n *tree_sitter.Node) bool {
		if n.Type() == "call_expression" {
			target := ExtractCallTarget(n, src)
			resolved, confidence := scope.Lookup(target)
			sites = append(sites, CallSite{
				Target:     target,
				ResolvedTo: resolved,
				Confidence: confidence,
				Line:       n.StartPoint().Row + 1,
			})
		}
		return true
	})
	return sites
}`
  },
  {
    id: "src/auth/token_service.ts::TokenService",
    name: "TokenService",
    kind: "class",
    language: "typescript",
    signature: "class TokenService implements ITokenProvider",
    path: "src/auth/token_service.ts",
    startLine: 12,
    endLine: 68,
    docstring: "TokenService manages cryptographic JWT token issuance, verification, and revocation caching.",
    codeSnippet: `export class TokenService implements ITokenProvider {
  private readonly secretKey: string;
  private readonly tokenCache: Map<string, TokenMetadata>;

  constructor(secretKey: string) {
    this.secretKey = secretKey;
    this.tokenCache = new Map();
  }

  public async generateToken(userId: string, claims: Record<string, unknown>): Promise<string> {
    const payload = { sub: userId, ...claims, exp: Math.floor(Date.now() / 1000) + 3600 };
    return signJWT(payload, this.secretKey);
  }

  public verifyToken(token: string): TokenPayload {
    return verifyJWT(token, this.secretKey);
  }
}`
  },
  {
    id: "src/auth/token_service.ts::generateToken",
    name: "generateToken",
    kind: "method",
    language: "typescript",
    container: "TokenService",
    signature: "public async generateToken(userId: string, claims: Record<string, unknown>): Promise<string>",
    path: "src/auth/token_service.ts",
    startLine: 24,
    endLine: 35,
    codeSnippet: `public async generateToken(userId: string, claims: Record<string, unknown>): Promise<string> {
  const payload = { sub: userId, ...claims, exp: Math.floor(Date.now() / 1000) + 3600 };
  const token = await signJWT(payload, this.secretKey);
  this.tokenCache.set(token, { issuedAt: Date.now(), userId });
  return token;
}`
  },
  {
    id: "src/api/routes.ts::handleAuthRoute",
    name: "handleAuthRoute",
    kind: "route",
    language: "typescript",
    signature: "export async function handleAuthRoute(req: Request, res: Response): Promise<void>",
    path: "src/api/routes.ts",
    startLine: 45,
    endLine: 82,
    docstring: "HTTP Route handler for /api/auth/login validating user credentials and delegating to TokenService.",
    codeSnippet: `export async function handleAuthRoute(req: Request, res: Response): Promise<void> {
  const { email, password } = req.body;
  const user = await UserRepository.findByEmail(email);
  if (!user || !await verifyPassword(password, user.passwordHash)) {
    res.status(401).json({ error: "Invalid credentials" });
    return;
  }
  const token = await tokenService.generateToken(user.id, { role: user.role });
  res.status(200).json({ token, user: { id: user.id, email: user.email } });
}`
  },
  {
    id: "internal/sem/cache_key.go::ComputeCacheKey",
    name: "ComputeCacheKey",
    kind: "function",
    language: "go",
    signature: "func ComputeCacheKey(repoRoot, commitHash string, profile ProfileMode, ignoreRules []string) string",
    path: "internal/sem/cache_key.go",
    startLine: 18,
    endLine: 54,
    docstring: "Derives cryptographic cache key ensuring zero cache contamination across branches and security boundaries.",
    codeSnippet: `func ComputeCacheKey(repoRoot, commitHash string, profile ProfileMode, ignoreRules []string) string {
	h := sha256.New()
	h.Write([]byte(SchemaVersionV1))
	h.Write([]byte(commitHash))
	h.Write([]byte(profile))
	for _, rule := range ignoreRules {
		h.Write([]byte(rule))
	}
	return hex.EncodeToString(h.Sum(nil))
}`
  },
  {
    id: "pkg/parser/python.go::ParsePythonAST",
    name: "ParsePythonAST",
    kind: "function",
    language: "go",
    signature: "func ParsePythonAST(source []byte) (*PythonModule, error)",
    path: "pkg/parser/python.go",
    startLine: 30,
    endLine: 78,
    docstring: "Parses Python source into semantic symbol definitions, decorator bindings, and class inheritance trees.",
    codeSnippet: `func ParsePythonAST(source []byte) (*PythonModule, error) {
	parser := tree_sitter.NewParser()
	parser.SetLanguage(tree_sitter_python.GetLanguage())
	tree := parser.Parse(source)
	if tree.RootNode().HasError() {
		return RecoverFromParseErrors(tree, source)
	}
	return ExtractDefinitions(tree.RootNode(), source), nil
}`
  }
];

export const INITIAL_EDGES: RelationEdge[] = [
  {
    id: "e1",
    fromId: "src/api/routes.ts::handleAuthRoute",
    toId: "src/auth/token_service.ts::generateToken",
    fromName: "handleAuthRoute",
    toName: "generateToken",
    relation: "CALLS",
    confidence: "EXACT",
    callsite: "src/api/routes.ts:52"
  },
  {
    id: "e2",
    fromId: "src/api/routes.ts::handleAuthRoute",
    toId: "src/auth/token_service.ts::TokenService",
    fromName: "handleAuthRoute",
    toName: "TokenService",
    relation: "USES_TYPE",
    confidence: "EXACT",
    callsite: "src/api/routes.ts:46"
  },
  {
    id: "e3",
    fromId: "internal/cli/impact.go::ComputeImpact",
    toId: "internal/sem/dependents.go::CountDependents",
    fromName: "ComputeImpact",
    toName: "CountDependents",
    relation: "CALLS",
    confidence: "EXACT",
    callsite: "internal/cli/impact.go:64"
  },
  {
    id: "e4",
    fromId: "internal/sem/analyze.go::AnalyzeGitRangeWithOptions",
    toId: "internal/sem/cache_key.go::ComputeCacheKey",
    fromName: "AnalyzeGitRangeWithOptions",
    toName: "ComputeCacheKey",
    relation: "CALLS",
    confidence: "EXACT",
    callsite: "internal/sem/analyze.go:78"
  },
  {
    id: "e5",
    fromId: "internal/sem/analyze.go::AnalyzeGitRangeWithOptions",
    toId: "internal/sem/dependents.go::CountDependents",
    fromName: "AnalyzeGitRangeWithOptions",
    toName: "CountDependents",
    relation: "CALLS",
    confidence: "EXACT",
    callsite: "internal/sem/analyze.go:94"
  },
  {
    id: "e6",
    fromId: "internal/sem/search.go::SearchRepo",
    toId: "internal/sem/cache_key.go::ComputeCacheKey",
    fromName: "SearchRepo",
    toName: "ComputeCacheKey",
    relation: "CALLS",
    confidence: "EXACT",
    callsite: "internal/sem/search.go:102"
  },
  {
    id: "e7",
    fromId: "internal/sem/search.go::SearchRepo",
    toId: "internal/sem/call_scanners.go::ResolveCallSites",
    fromName: "SearchRepo",
    toName: "ResolveCallSites",
    relation: "CALLS",
    confidence: "EXACT",
    callsite: "internal/sem/search.go:121"
  }
];

export const BENCHMARKS_DATA: BenchmarkSystem[] = [
  {
    name: "entire-graph",
    locomoScore: 94.74,
    indexTokens: "0 (Local Tree-Sitter)",
    version: "#104 branch (pre-merge) / v0.4.0",
    notes: "Ranked 1st in 1,540 LoCoMo queries with strictly local AST analysis and zero egress.",
    isLeader: true
  },
  {
    name: "mem0",
    locomoScore: 93.83,
    indexTokens: "50.85M",
    version: "commit 4debc58",
    notes: "Requires 50+ million embedding and extraction tokens during index phase."
  },
  {
    name: "cognee",
    locomoScore: 92.86,
    indexTokens: "12.35M",
    version: "commit 38eece5",
    notes: "Graph pipeline relying on LLM-driven entity extraction."
  },
  {
    name: "bm25 (lexical)",
    locomoScore: 91.88,
    indexTokens: "0",
    version: "0.2.2",
    notes: "Standard BM25 baseline over raw file tokens."
  },
  {
    name: "codebase-memory-mcp",
    locomoScore: 91.30,
    indexTokens: "0",
    version: "v0.9.0 (patched)",
    notes: "Patched to emit Markdown sections."
  },
  {
    name: "graphify",
    locomoScore: 87.34,
    indexTokens: "0",
    version: "unpinned snapshot",
    notes: "AST graph extraction without relational scoring."
  },
  {
    name: "letta",
    locomoScore: 84.68,
    indexTokens: "Not projectable",
    version: "0.16.8",
    notes: "Multi-step memory synthesis architecture."
  },
  {
    name: "supermemory",
    locomoScore: 82.08,
    indexTokens: "Hosted API",
    version: "server-v0.0.7-rc.2",
    notes: "Retrieval capped at 100 items compared to 200 items in other arms."
  }
];

export const RECENT_DIFFS: EntityDiffItem[] = [
  {
    id: "diff-1",
    type: "SIGNATURE_CHANGED",
    kind: "function",
    name: "AnalyzeGitRangeWithOptions",
    path: "internal/sem/analyze.go",
    oldSignature: "func AnalyzeGitRange(repo, base, head string) (*Result, error)",
    newSignature: "func AnalyzeGitRangeWithOptions(ctx context.Context, repo string, base, head string, opts AnalyzeOptions) (*Result, error)",
    dependentsCount: 18,
    riskLevel: "HIGH",
    reconciliation: "RECONCILED_FROM",
    explanation: "Added context cancellation support and custom profiling options. High dependent footprint across CLI commands."
  },
  {
    id: "diff-2",
    type: "BODY_CHANGED",
    kind: "method",
    name: "generateToken",
    path: "src/auth/token_service.ts",
    dependentsCount: 9,
    riskLevel: "MEDIUM",
    explanation: "Added token cache revocation indexing and timestamped metadata."
  },
  {
    id: "diff-3",
    type: "ADDED",
    kind: "function",
    name: "ComputeImpact",
    path: "internal/cli/impact.go",
    newSignature: "func ComputeImpact(ctx context.Context, repo string, symbol string, opts ImpactOptions) (*ImpactReport, error)",
    dependentsCount: 4,
    riskLevel: "LOW",
    explanation: "New one-shot blast radius retrieval verb providing caller, type, and co-change analysis."
  },
  {
    id: "diff-4",
    type: "SIGNATURE_CHANGED",
    kind: "function",
    name: "ComputeCacheKey",
    path: "internal/sem/cache_key.go",
    oldSignature: "func ComputeCacheKey(commitHash string) string",
    newSignature: "func ComputeCacheKey(repoRoot, commitHash string, profile ProfileMode, ignoreRules []string) string",
    dependentsCount: 26,
    riskLevel: "CRITICAL",
    explanation: "Cache key calculation now binds worktree path and .graphignore rules to prevent cache leakage across repositories."
  }
];

export const MOCK_STATS = {
  totalQueries: 1420,
  graphCalls: 894,
  rawFileReadsPrevented: 3820,
  estimatedTokensSaved: "1,940,250",
  graphFirstRate: "88.4%",
  medianLookupTimeMs: 14.2,
  cacheHitRatio: "96.5%"
};
