package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/memorytool"
	managementv1alpha1 "github.com/caelis-labs/memory/api/memory/management/v1alpha1"
	stewardv1alpha1 "github.com/caelis-labs/memory/api/memory/steward/v1alpha1"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

const (
	realMemoryStewardEvaluationEnv       = "CAELIS_REAL_MEMORY_STEWARD_EVAL"
	realMemoryStewardEvaluationReportEnv = "CAELIS_REAL_MEMORY_STEWARD_REPORT"
	realMemoryStewardEvaluationLimitEnv  = "CAELIS_REAL_MEMORY_STEWARD_LIMIT"
	realMemoryStewardEvaluationBatchSize = 8
)

type realMemoryStewardCase struct {
	ID      string `json:"id"`
	Group   string `json:"group"`
	Receipt string `json:"receipt"`
	Query   string `json:"query"`
}

type realMemoryStewardEvaluationReport struct {
	FormatVersion int                                   `json:"format_version"`
	Fixture       realMemoryStewardFixtureReport        `json:"fixture"`
	Model         realMemoryStewardModelReport          `json:"model"`
	Policy        realMemoryStewardPolicyReport         `json:"policy"`
	Static        realMemoryStewardModeReport           `json:"static"`
	Semantic      realMemoryStewardModeReport           `json:"semantic"`
	Lift          realMemoryStewardQualityReport        `json:"semantic_lift"`
	Groups        map[string]realMemoryStewardGroupLift `json:"groups"`
}

type realMemoryStewardFixtureReport struct {
	SHA256 string `json:"sha256"`
	Cases  int    `json:"cases"`
	Groups int    `json:"groups"`
	Rounds int    `json:"rounds"`
}

type realMemoryStewardModelReport struct {
	ProfileID string `json:"profile_id"`
	Effort    string `json:"effort"`
}

type realMemoryStewardPolicyReport struct {
	ProfileID             string `json:"profile_id"`
	ProfileVersion        uint64 `json:"profile_version"`
	ProfilePromptSHA256   string `json:"profile_prompt_sha256"`
	EffectivePromptSHA256 string `json:"effective_prompt_sha256"`
	MaxContextRecords     int    `json:"max_context_records"`
	MaxInputBytes         int    `json:"max_input_bytes"`
	MaxOutputBytes        int    `json:"max_output_bytes"`
}

type realMemoryStewardModeReport struct {
	Trajectory   []realMemoryStewardRoundReport            `json:"trajectory"`
	Final        realMemoryStewardQualityReport            `json:"final"`
	Groups       map[string]realMemoryStewardQualityReport `json:"groups"`
	Steward      realMemoryStewardStateReport              `json:"steward"`
	FailureCodes map[string]int                            `json:"failure_codes"`
	ModelUsage   realMemoryStewardUsageReport              `json:"model_usage"`
}

type realMemoryStewardRoundReport struct {
	Receipts   int                            `json:"receipts"`
	Quality    realMemoryStewardQualityReport `json:"quality"`
	Steward    realMemoryStewardStateReport   `json:"steward"`
	ModelUsage realMemoryStewardUsageReport   `json:"model_usage"`
}

type realMemoryStewardQualityReport struct {
	Queries        int     `json:"queries"`
	RetrievalAt8   float64 `json:"retrieval_at_8"`
	RecallAt1      float64 `json:"recall_at_1"`
	RecallAt5      float64 `json:"recall_at_5"`
	MRR            float64 `json:"mrr"`
	ZeroResultRate float64 `json:"zero_result_rate"`
}

type realMemoryStewardStateReport struct {
	PendingJobs   int64 `json:"pending_jobs"`
	LeasedJobs    int64 `json:"leased_jobs"`
	CompletedJobs int64 `json:"completed_jobs"`
	FailedJobs    int64 `json:"failed_jobs"`
	ActiveRecords int64 `json:"active_records"`
}

type realMemoryStewardUsageReport struct {
	Calls             int            `json:"calls"`
	CallFailures      int            `json:"call_failures"`
	PromptTokens      int            `json:"prompt_tokens"`
	CachedInputTokens int            `json:"cached_input_tokens"`
	CompletionTokens  int            `json:"completion_tokens"`
	ReasoningTokens   int            `json:"reasoning_tokens"`
	TotalTokens       int            `json:"total_tokens"`
	ElapsedMS         int64          `json:"elapsed_ms"`
	CallP50MS         int64          `json:"call_p50_ms"`
	CallP95MS         int64          `json:"call_p95_ms"`
	OutputParseErrors map[string]int `json:"output_parse_errors,omitempty"`
	OutputShapes      map[string]int `json:"output_shapes,omitempty"`
}

type realMemoryStewardGroupLift struct {
	Static   realMemoryStewardQualityReport `json:"static"`
	Semantic realMemoryStewardQualityReport `json:"semantic"`
	Lift     realMemoryStewardQualityReport `json:"lift"`
}

type realMemoryStewardReceipt struct {
	caseIndex int
	receiptID memoryv1alpha1.ReceiptID
}

type realMemoryStewardRecordingRunner struct {
	inner systemManagedAgentRunner

	mu           sync.Mutex
	calls        int
	failures     int
	usage        session.UsageSnapshot
	durations    []time.Duration
	parseErrors  map[string]int
	outputShapes map[string]int
}

func TestMemoryStewardSemanticEvaluationFixtureAndPolicy(t *testing.T) {
	cases, digest := loadRealMemoryStewardCases(t)
	groups := make(map[string]struct{})
	for _, test := range cases {
		groups[test.Group] = struct{}{}
	}
	if len(cases) != 64 || len(groups) < 4 || len(digest) != 64 {
		t.Fatalf("semantic fixture = cases:%d groups:%d digest:%q", len(cases), len(groups), digest)
	}
	if defaultMemoryStewardProfile.Version != 3 || defaultMemoryStewardProfile.MaxContextRecords != 16 ||
		defaultMemoryStewardProfile.MaxInputBytes != 128<<10 || defaultMemoryStewardProfile.MaxOutputBytes != 4<<10 {
		t.Fatalf("Memory Steward policy bounds = %+v", defaultMemoryStewardProfile)
	}
	for _, phrase := range []string{"Use ADD unless", "at most two commonplace", "Chinese/English equivalent", "never a new fact", "exact supplied target"} {
		if !strings.Contains(defaultMemoryStewardProfile.SystemPrompt, phrase) {
			t.Fatalf("Memory Steward policy is missing %q", phrase)
		}
	}
}

func TestMemoryStewardOutputReceivesExactContractAndNarrowFenceHandling(t *testing.T) {
	instructions := memoryStewardPolicyInstructions("preserve facts", false)
	for _, phrase := range []string{`"operation":"ADD"`, `"target_record_id"`, `"expected_revision"`, `may use only these top-level keys`, `with no Markdown fence`} {
		if !strings.Contains(instructions, phrase) {
			t.Fatalf("text output instructions are missing %q: %s", phrase, instructions)
		}
	}
	if strings.Contains(instructions, "lexicon_terms") {
		t.Fatalf("request without lexicon candidates received lexicon instructions: %s", instructions)
	}
	withLexicon := memoryStewardPolicyInstructions("preserve facts", true)
	if !strings.Contains(withLexicon, "lexicon_terms") || !strings.Contains(withLexicon, "exact candidate term values") {
		t.Fatalf("lexicon candidate request is missing its bounded contract: %s", withLexicon)
	}
	structured := memoryStewardPolicyInstructions("preserve facts", false)
	if !strings.Contains(structured, `"operation":"ADD"`) {
		t.Fatalf("structured output is missing its exact JSON object contract: %s", structured)
	}
	properties, _ := memoryStewardOutputSpec(4 << 10).JSONSchema["properties"].(map[string]any)
	if properties["lexicon_terms"] == nil {
		t.Fatalf("structured output schema is missing lexicon_terms: %+v", properties)
	}
	encoded, err := json.Marshal(memoryStewardModelInput{
		LexiconCandidates: []stewardv1alpha1.LexiconCandidate{{Term: "量子织网", DocumentFrequency: 3}},
	})
	if err != nil || !strings.Contains(string(encoded), `"lexicon_candidates":[{"term":"量子织网"`) {
		t.Fatalf("Steward lexicon input = %s, %v", encoded, err)
	}
	fenced := "```json\n" + `{"operation":"ADD","kind":"fact","text":"durable","evidence_refs":["receipt-1"]}` + "\n```"
	proposal, err := parseMemoryStewardProposal(fenced)
	if err != nil || proposal.Operation != "ADD" || proposal.Text != "durable" {
		t.Fatalf("parse fenced JSON = %+v, %v", proposal, err)
	}
	if _, err := parseMemoryStewardProposal("proposal follows\n" + fenced); err == nil {
		t.Fatal("parser accepted prose outside the JSON fence")
	}
}

// TestMemoryStewardRealProviderSemanticEvaluation is an opt-in quality
// experiment. It copies one explicitly selected local Mimo profile into
// temporary Stores, compares zero-token static Recall with model-organized
// Recall on the same frozen cases, and emits only aggregate evidence.
func TestMemoryStewardRealProviderSemanticEvaluation(t *testing.T) {
	if strings.TrimSpace(os.Getenv(realMemoryStewardEvaluationEnv)) != "1" {
		t.Skip("set CAELIS_REAL_MEMORY_STEWARD_EVAL=1 to run the real-provider Memory Steward evaluation")
	}
	sourceStore := strings.TrimSpace(os.Getenv(realMimoE2ESourceStoreEnv))
	if sourceStore == "" {
		t.Fatalf("%s is required", realMimoE2ESourceStoreEnv)
	}
	profileID := strings.TrimSpace(os.Getenv(realMimoE2EProfileEnv))
	if profileID == "" {
		profileID = defaultRealMimoProfile
	}
	cases, fixtureDigest := loadRealMemoryStewardCases(t)
	if rawLimit := strings.TrimSpace(os.Getenv(realMemoryStewardEvaluationLimitEnv)); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > len(cases) {
			t.Fatalf("%s must be within 1..%d", realMemoryStewardEvaluationLimitEnv, len(cases))
		}
		cases = cases[:limit]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	static, staticProfile, staticEffort := runRealMemoryStewardMode(t, ctx, sourceStore, profileID, cases, false)
	semantic, semanticProfile, semanticEffort := runRealMemoryStewardMode(t, ctx, sourceStore, profileID, cases, true)
	if staticProfile != semanticProfile || staticEffort != semanticEffort {
		t.Fatalf("evaluation model drifted: static %s/%s semantic %s/%s", staticProfile, staticEffort, semanticProfile, semanticEffort)
	}
	groups := make(map[string]struct{})
	for _, test := range cases {
		groups[test.Group] = struct{}{}
	}
	report := realMemoryStewardEvaluationReport{
		FormatVersion: 2,
		Fixture: realMemoryStewardFixtureReport{
			SHA256: fixtureDigest, Cases: len(cases), Groups: len(groups),
			Rounds: (len(cases) + realMemoryStewardEvaluationBatchSize - 1) / realMemoryStewardEvaluationBatchSize,
		},
		Model: realMemoryStewardModelReport{ProfileID: semanticProfile, Effort: semanticEffort},
		Policy: realMemoryStewardPolicyReport{
			ProfileID: string(defaultMemoryStewardProfile.ProfileID), ProfileVersion: defaultMemoryStewardProfile.Version,
			ProfilePromptSHA256:   digestString(defaultMemoryStewardProfile.SystemPrompt),
			EffectivePromptSHA256: digestString(memoryStewardPolicyInstructions(defaultMemoryStewardProfile.SystemPrompt, false)),
			MaxContextRecords:     defaultMemoryStewardProfile.MaxContextRecords,
			MaxInputBytes:         defaultMemoryStewardProfile.MaxInputBytes, MaxOutputBytes: defaultMemoryStewardProfile.MaxOutputBytes,
		},
		Static: static, Semantic: semantic,
		Lift:   subtractRealMemoryStewardQuality(semantic.Final, static.Final),
		Groups: compareRealMemoryStewardGroups(static.Groups, semantic.Groups),
	}
	if output := strings.TrimSpace(os.Getenv(realMemoryStewardEvaluationReportEnv)); output != "" {
		writeRealMemoryStewardReport(t, output, report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real Memory Steward evaluation: %s", encoded)
	if semantic.ModelUsage.Calls != len(cases) || semantic.Steward.CompletedJobs+semantic.Steward.FailedJobs != int64(len(cases)) {
		t.Fatalf("incomplete Steward execution: usage=%+v state=%+v", semantic.ModelUsage, semantic.Steward)
	}
	if len(cases) == 64 {
		validateRealMemoryStewardAcceptance(t, semantic)
	}
}

func validateRealMemoryStewardAcceptance(t *testing.T, report realMemoryStewardModeReport) {
	t.Helper()
	if report.ModelUsage.CallFailures != 0 || len(report.ModelUsage.OutputParseErrors) != 0 ||
		report.Steward.FailedJobs != 0 || len(report.FailureCodes) != 0 {
		t.Fatalf("Steward protocol reliability gate failed: usage=%+v state=%+v failures=%v", report.ModelUsage, report.Steward, report.FailureCodes)
	}
	if report.Final.RecallAt1 < 0.70 || report.Final.RecallAt5 < 0.75 || report.Final.ZeroResultRate > 0.25 {
		t.Fatalf("Steward aggregate quality gate failed: %+v", report.Final)
	}
	if report.Groups["technical-alias"].RecallAt1 < 0.70 || report.Groups["chinese-alias"].RecallAt1 < 0.65 {
		t.Fatalf("Steward alias quality gate failed: technical=%+v chinese=%+v", report.Groups["technical-alias"], report.Groups["chinese-alias"])
	}
}

func runRealMemoryStewardMode(
	t *testing.T,
	ctx context.Context,
	sourceStore string,
	profileID string,
	cases []realMemoryStewardCase,
	semantic bool,
) (realMemoryStewardModeReport, string, string) {
	t.Helper()
	storeDir := t.TempDir()
	profile, effort := copyRealMimoConfiguration(t, ctx, sourceStore, storeDir, profileID)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	stack, err := NewLocalStack(Config{
		StoreDir: storeDir, WorkspaceKey: "memory-steward-evaluation", WorkspaceCWD: workspace,
		ApprovalMode: "auto-review", ModelProfileID: profile.ID, ModelProfileEffort: effort,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stack.Close(); err != nil {
			t.Errorf("close Memory evaluation Host: %v", err)
		}
	}()
	recorder := &realMemoryStewardRecordingRunner{inner: stack.memorySteward.runner}
	stack.memorySteward.runner = recorder
	if semantic {
		if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
			Handle: agentbinding.HandleSteward, ProfileID: profile.ID, Effort: effort,
		}); err != nil {
			t.Fatalf("bind real Memory Steward: %v", err)
		}
		waitRealMemoryStewardState(t, ctx, "activate Steward", func() (bool, error) {
			return stack.memorySteward.active.Load(), nil
		})
	}
	document, err := newAppConfigStore(storeDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	binding, found, err := memorybinding.Resolve(document.Memory, memorybinding.RuntimeSelection{})
	if err != nil || !found {
		t.Fatalf("resolve Memory evaluation binding = found:%t err:%v", found, err)
	}
	client, err := stack.memoryRuntime.Bind(binding, memoryv1alpha1.SourceContext{
		ActorRef: string(binding.RuntimeActorRef), SourceType: "memory-steward-real-evaluation",
	}, memorytool.DefaultRecallBudget())
	if err != nil {
		t.Fatal(err)
	}
	receipts := make([]realMemoryStewardReceipt, 0, len(cases))
	trajectory := make([]realMemoryStewardRoundReport, 0, (len(cases)+realMemoryStewardEvaluationBatchSize-1)/realMemoryStewardEvaluationBatchSize)
	var consistencyToken memoryv1alpha1.ConsistencyToken
	for start := 0; start < len(cases); start += realMemoryStewardEvaluationBatchSize {
		end := min(start+realMemoryStewardEvaluationBatchSize, len(cases))
		for index := start; index < end; index++ {
			response, err := client.Remember(ctx, cases[index].Receipt, "real-steward-eval-"+cases[index].ID, nil)
			if err != nil {
				t.Fatalf("Remember case %q: %v", cases[index].ID, err)
			}
			consistencyToken = response.ConsistencyToken
			receipts = append(receipts, realMemoryStewardReceipt{caseIndex: index, receiptID: response.ReceiptID})
		}
		if semantic {
			waitRealMemoryStewardState(t, ctx, fmt.Sprintf("complete %d Steward jobs", end), func() (bool, error) {
				inspection, inspectErr := stack.memoryRuntime.Management().Inspect(ctx)
				if inspectErr != nil {
					return false, inspectErr
				}
				finished := inspection.Steward.CompletedJobs + inspection.Steward.FailedJobs
				return finished == int64(end) && inspection.Steward.PendingJobs == 0 && inspection.Steward.LeasedJobs == 0, nil
			})
		}
		quality := evaluateRealMemoryStewardQuality(t, ctx, client, cases[:end], receipts, consistencyToken, "")
		inspection, err := stack.memoryRuntime.Management().Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		trajectory = append(trajectory, realMemoryStewardRoundReport{
			Receipts: end, Quality: quality, Steward: realMemoryStewardState(inspection.Steward), ModelUsage: recorder.snapshot(),
		})
	}
	groups := make(map[string]realMemoryStewardQualityReport)
	for _, test := range cases {
		if _, found := groups[test.Group]; found {
			continue
		}
		groups[test.Group] = evaluateRealMemoryStewardQuality(t, ctx, client, cases, receipts, consistencyToken, test.Group)
	}
	failureCodes := make(map[string]int)
	for _, receipt := range receipts {
		status, err := client.GetReceiptStatus(ctx, receipt.receiptID)
		if err != nil {
			t.Fatalf("read receipt status: %v", err)
		}
		if status.TerminalErrorCode != "" {
			failureCodes[string(status.TerminalErrorCode)]++
		}
	}
	return realMemoryStewardModeReport{
		Trajectory: trajectory, Final: trajectory[len(trajectory)-1].Quality,
		Groups: groups, Steward: trajectory[len(trajectory)-1].Steward,
		FailureCodes: failureCodes, ModelUsage: recorder.snapshot(),
	}, profile.ID, effort
}

func evaluateRealMemoryStewardQuality(
	t *testing.T,
	ctx context.Context,
	client interface {
		Recall(context.Context, string, memoryv1alpha1.ConsistencyToken) (memoryv1alpha1.RecallResponse, error)
	},
	cases []realMemoryStewardCase,
	receipts []realMemoryStewardReceipt,
	token memoryv1alpha1.ConsistencyToken,
	group string,
) realMemoryStewardQualityReport {
	t.Helper()
	byCase := make(map[int]memoryv1alpha1.ReceiptID, len(receipts))
	for _, receipt := range receipts {
		byCase[receipt.caseIndex] = receipt.receiptID
	}
	report := realMemoryStewardQualityReport{}
	reciprocalRank := 0.0
	zero := 0
	for index, test := range cases {
		if group != "" && test.Group != group {
			continue
		}
		report.Queries++
		response, err := client.Recall(ctx, test.Query, token)
		if err != nil {
			t.Fatalf("Recall case %q: %v", test.ID, err)
		}
		if len(response.Fragments) == 0 {
			zero++
		}
		position := -1
		for fragmentIndex, fragment := range response.Fragments {
			if containsRealMemoryStewardEvidence(fragment.EvidenceRefs, byCase[index]) {
				position = fragmentIndex
				break
			}
		}
		if position < 0 {
			continue
		}
		report.RetrievalAt8++
		reciprocalRank += 1 / float64(position+1)
		if position == 0 {
			report.RecallAt1++
		}
		if position < 5 {
			report.RecallAt5++
		}
	}
	if report.Queries == 0 {
		return report
	}
	count := float64(report.Queries)
	report.RetrievalAt8 /= count
	report.RecallAt1 /= count
	report.RecallAt5 /= count
	report.MRR = reciprocalRank / count
	report.ZeroResultRate = float64(zero) / count
	return report
}

func compareRealMemoryStewardGroups(
	static map[string]realMemoryStewardQualityReport,
	semantic map[string]realMemoryStewardQualityReport,
) map[string]realMemoryStewardGroupLift {
	result := make(map[string]realMemoryStewardGroupLift, len(semantic))
	for group, semanticQuality := range semantic {
		staticQuality := static[group]
		result[group] = realMemoryStewardGroupLift{
			Static: staticQuality, Semantic: semanticQuality,
			Lift: subtractRealMemoryStewardQuality(semanticQuality, staticQuality),
		}
	}
	return result
}

func (r *realMemoryStewardRecordingRunner) Run(
	ctx context.Context,
	request systemManagedAgentRunRequest,
) (systemManagedAgentRunResult, error) {
	started := time.Now()
	result, err := r.inner.Run(ctx, request)
	usage := latestRealMemoryStewardUsage(result.ContextEvents)
	r.mu.Lock()
	r.calls++
	if err != nil {
		r.failures++
	} else if _, parseErr := parseMemoryStewardProposal(result.Text); parseErr != nil {
		if r.parseErrors == nil {
			r.parseErrors = make(map[string]int)
		}
		r.parseErrors[parseErr.Error()]++
	}
	if err == nil {
		if r.outputShapes == nil {
			r.outputShapes = make(map[string]int)
		}
		r.outputShapes[realMemoryStewardJSONShape(result.Text)]++
	}
	r.usage.PromptTokens += usage.PromptTokens
	r.usage.CachedInputTokens += usage.CachedInputTokens
	r.usage.CompletionTokens += usage.CompletionTokens
	r.usage.ReasoningTokens += usage.ReasoningTokens
	r.usage.TotalTokens += usage.TotalTokens
	r.durations = append(r.durations, time.Since(started))
	r.mu.Unlock()
	return result, err
}

func (r *realMemoryStewardRecordingRunner) snapshot() realMemoryStewardUsageReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	durations := append([]time.Duration(nil), r.durations...)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	percentile := func(value int) int64 {
		if len(durations) == 0 {
			return 0
		}
		index := (len(durations)*value + 99) / 100
		return durations[index-1].Milliseconds()
	}
	elapsed := time.Duration(0)
	for _, duration := range durations {
		elapsed += duration
	}
	return realMemoryStewardUsageReport{
		Calls: r.calls, CallFailures: r.failures,
		PromptTokens: r.usage.PromptTokens, CachedInputTokens: r.usage.CachedInputTokens,
		CompletionTokens: r.usage.CompletionTokens, ReasoningTokens: r.usage.ReasoningTokens,
		TotalTokens: r.usage.TotalTokens, ElapsedMS: elapsed.Milliseconds(),
		CallP50MS: percentile(50), CallP95MS: percentile(95),
		OutputParseErrors: cloneStringIntMap(r.parseErrors),
		OutputShapes:      cloneStringIntMap(r.outputShapes),
	}
}

func realMemoryStewardJSONShape(text string) string {
	var value any
	if err := json.Unmarshal([]byte(memoryStewardJSONText(text)), &value); err != nil {
		return "invalid-json"
	}
	return realMemoryStewardValueShape(value, 0, "")
}

func realMemoryStewardValueShape(value any, depth int, key string) string {
	if depth >= 3 {
		return "..."
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+":"+realMemoryStewardValueShape(typed[key], depth+1, key))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(typed) == 0 {
			return "[]"
		}
		return "[" + realMemoryStewardValueShape(typed[0], depth+1, key) + "]"
	case string:
		if key == "action" || key == "operation" {
			return "string(" + strings.ToLower(strings.TrimSpace(typed)) + ")"
		}
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

func cloneStringIntMap(source map[string]int) map[string]int {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func latestRealMemoryStewardUsage(events []*session.Event) session.UsageSnapshot {
	var result session.UsageSnapshot
	for _, event := range events {
		usage := session.UsageSnapshotFromSessionEvent(event)
		if usage != nil && usage.TotalTokens >= result.TotalTokens {
			result = *usage
		}
	}
	return result
}

func loadRealMemoryStewardCases(t *testing.T) ([]realMemoryStewardCase, string) {
	t.Helper()
	path := filepath.Join("testdata", "memory_steward_semantic_cases.json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []realMemoryStewardCase
	if err := json.Unmarshal(encoded, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 50 || len(cases)%realMemoryStewardEvaluationBatchSize != 0 {
		t.Fatalf("semantic fixture has %d cases, want at least 50 and complete batches", len(cases))
	}
	ids := make(map[string]struct{}, len(cases))
	for _, test := range cases {
		if strings.TrimSpace(test.ID) == "" || strings.TrimSpace(test.Group) == "" ||
			strings.TrimSpace(test.Receipt) == "" || strings.TrimSpace(test.Query) == "" {
			t.Fatalf("incomplete semantic fixture case: %+v", test)
		}
		if _, duplicate := ids[test.ID]; duplicate {
			t.Fatalf("duplicate semantic fixture ID %q", test.ID)
		}
		ids[test.ID] = struct{}{}
		if strings.Contains(strings.ToLower(test.Receipt), strings.ToLower(test.Query)) {
			t.Fatalf("case %q query is already a literal receipt substring", test.ID)
		}
	}
	return cases, digestBytes(encoded)
}

func waitRealMemoryStewardState(t *testing.T, ctx context.Context, name string, condition func() (bool, error)) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		ready, err := condition()
		if err != nil {
			t.Fatalf("wait for %s: %v", name, err)
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s: %v", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func realMemoryStewardState(value managementv1alpha1.StewardDiagnostics) realMemoryStewardStateReport {
	return realMemoryStewardStateReport{
		PendingJobs: value.PendingJobs, LeasedJobs: value.LeasedJobs,
		CompletedJobs: value.CompletedJobs, FailedJobs: value.FailedJobs,
		ActiveRecords: value.ActiveRecords,
	}
}

func containsRealMemoryStewardEvidence(values []memoryv1alpha1.ReceiptID, target memoryv1alpha1.ReceiptID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func subtractRealMemoryStewardQuality(
	left realMemoryStewardQualityReport,
	right realMemoryStewardQualityReport,
) realMemoryStewardQualityReport {
	return realMemoryStewardQualityReport{
		Queries:        left.Queries,
		RetrievalAt8:   left.RetrievalAt8 - right.RetrievalAt8,
		RecallAt1:      left.RecallAt1 - right.RecallAt1,
		RecallAt5:      left.RecallAt5 - right.RecallAt5,
		MRR:            left.MRR - right.MRR,
		ZeroResultRate: left.ZeroResultRate - right.ZeroResultRate,
	}
}

func digestString(value string) string { return digestBytes([]byte(value)) }

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func writeRealMemoryStewardReport(t *testing.T, path string, report realMemoryStewardEvaluationReport) {
	t.Helper()
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	temporary, err := os.CreateTemp(directory, ".memory-steward-report-*")
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		t.Fatal(err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		t.Fatal(err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		t.Fatal(err)
	}
}
