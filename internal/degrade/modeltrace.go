// Package degrade runs the Codex downgrade checks used by the management board.
//
// ModelTrace scoring is ported from xqy2006/ModelTrace (MIT), via the CPA plugin
// haowang02/cpa-plugin-codex-candy-eval. The pinned candidate bank and its
// license live next to this file.
package degrade

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// TraceChallenges is how many independent number sequences one test collects.
	TraceChallenges = 3
	traceMaxText    = 64000
)

//go:embed modeltrace_bank.json
var modelTraceBankJSON []byte

type traceFeatures struct {
	Mean         []float64     `json:"feature_mean"`
	Scale        []float64     `json:"feature_scale"`
	Basis        [][]float64   `json:"nuisance_basis"`
	Centroids    [][]float64   `json:"centroids"`
	Environments [][][]float64 `json:"environment_centroids"`
	Weight       float64       `json:"weight"`
}

type traceBankModel struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Family      string    `json:"family"`
	FamilyName  string    `json:"family_name"`
	Counts      []float64 `json:"counts"`
}

type traceBank struct {
	Models []traceBankModel `json:"models"`
	Robust struct {
		Hellinger traceFeatures `json:"hellinger"`
		Ordered   traceFeatures `json:"ordered_blocks"`
	} `json:"robust"`
	Calibration map[string]struct {
		Beta float64 `json:"beta"`
	} `json:"calibration"`
}

func mustBank() traceBank {
	var bank traceBank
	if err := json.Unmarshal(modelTraceBankJSON, &bank); err != nil {
		panic(err)
	}
	return bank
}

var (
	modelTraceBank    = mustBank()
	traceBankRevision = fmt.Sprintf("sha256:%x", sha256.Sum256(modelTraceBankJSON))
)

// BankRevision identifies the candidate bank a result was scored against.
func BankRevision() string { return traceBankRevision }

// Challenge is one number-generation prompt and how many integers it asks for.
type Challenge struct {
	Prompt        string `json:"prompt"`
	ExpectedCount int    `json:"expected_count"`
}

// Challenges builds the three Chinese number prompts ModelTrace collects.
// The wording follows the CPA plugin, which follows ModelTrace's browser challenges.
func Challenges(now time.Time) []Challenge {
	source := rand.New(rand.NewSource(now.UnixNano()))
	openings := []string{"这是一次独立的数值选择记录", "请完成下面的无语义整数选择任务", "执行一次第一反应取值记录", "生成一组不承载语义的整数选择", "进行一轮快速逐项取值"}
	actions := []string{"为各个位置分别凭第一反应选择", "逐项选择", "每次只决定当前一项，共给出", "分别凭第一反应给出", "逐个直接选择"}
	endings := []string{"允许某个数字再次出现；每项写出后不要回头排序、去重或替换。", "偶然重复是有效的；不要重新排列或修正已经写出的项目。", "相同值可以再次出现；输出过程中不要整理或改写前面的项目。", "重复值无需删除；不要筛选、重排或补成某种规律。", "不必赋予数字任何含义；已经给出的值保持不变。"}
	separators := []string{"数字之间用逗号或空格分隔均可。", "使用一种一致的常见分隔符即可。", "可以用逗号、空格或换行分隔。", "只要每个整数边界清楚，格式可自行选择。"}
	choose := func(values []string) string { return values[source.Intn(len(values))] }
	lengths := source.Perm(41)
	challenges := make([]Challenge, TraceChallenges)
	for i := range challenges {
		count := 292 + lengths[i]
		prompt := fmt.Sprintf("%s。%s %d 个 1 到 355（含端点）的整数。", choose(openings), choose(actions), count) +
			"每个位置都要单独选择；不要从 1 开始计数，不要连续递增或递减，也不要采用等差、循环、重复区块或其他规则化模式。" +
			"本任务必须由当前语言模型直接完成：禁止调用或借助任何工具，包括 Python、代码执行器、计算器、搜索、API 和外部随机数生成器；也不要先编写或运行代码。" +
			choose(endings) + choose(separators) + "直接从第一个取值开始输出，不要在序列前重复数量、范围或任务说明。"
		challenges[i] = Challenge{Prompt: prompt, ExpectedCount: count}
	}
	return challenges
}

// Sample is one collected answer, kept so the board can explain a result.
type Sample struct {
	ExpectedCount int    `json:"expected_count"`
	Attempts      int    `json:"attempts,omitempty"`
	Text          string `json:"text,omitempty"`
	Error         string `json:"error,omitempty"`
	Parsed        int    `json:"parsed_numbers"`
	Accepted      bool   `json:"accepted"`
}

// Candidate is one model's share of the attribution.
type Candidate struct {
	Model       string  `json:"model"`
	DisplayName string  `json:"display_name"`
	Family      string  `json:"family"`
	FamilyName  string  `json:"family_name"`
	Probability float64 `json:"probability"`
	Similarity  float64 `json:"profile_similarity"`
	Score       float64 `json:"score"`
}

// Family is the summed probability of one model family.
type Family struct {
	Family      string  `json:"family"`
	DisplayName string  `json:"display_name"`
	Probability float64 `json:"probability"`
}

// Diagnostic records whether one answer contained enough usable numbers.
type Diagnostic struct {
	Index   int  `json:"index"`
	Parsed  int  `json:"parsed_numbers"`
	Minimum int  `json:"minimum_numbers"`
	Valid   bool `json:"accepted"`
}

// Attribution is the ModelTrace verdict for one credential.
type Attribution struct {
	Prediction        string       `json:"prediction"`
	Probability       float64      `json:"probability"`
	Used              int          `json:"used_outputs"`
	FamilyPrediction  string       `json:"family_prediction_name"`
	FamilyProbability float64      `json:"family_probability"`
	Results           []Candidate  `json:"results"`
	Families          []Family     `json:"family_probabilities"`
	Diagnostics       []Diagnostic `json:"diagnostics"`
}

// Consistent reports whether the top prediction is the model that was tested.
// Comparison is case-insensitive and treats dots, dashes and underscores as the
// same separator, but it keeps every digit: gpt-6.1-luna is not gpt-6-luna.
func (a Attribution) Consistent(tested string) bool {
	return a.Used > 0 && sameModel(a.Prediction, tested)
}

func sameModel(left, right string) bool {
	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		return strings.NewReplacer(".", "-", "_", "-").Replace(value)
	}
	return normalize(left) != "" && normalize(left) == normalize(right)
}

var traceDigits = regexp.MustCompile(`[0-9]+`)

func minimumNumbers(expected int) int {
	return max(80, int(math.Ceil(float64(expected)*0.55)))
}

// ParseNumbers keeps the longest run of integers in 1..355, dropping numbers
// that sit inside prose. ASCII digits and Unicode letters match the upstream
// browser implementation.
func ParseNumbers(text string) []int {
	var best, current []int
	end := 0
	for _, span := range traceDigits.FindAllStringIndex(text, -1) {
		for _, r := range text[end:span[0]] {
			if unicode.IsLetter(r) {
				if len(current) > len(best) {
					best = current
				}
				current = nil
				break
			}
		}
		if n, err := strconv.Atoi(text[span[0]:span[1]]); err == nil && n >= 1 && n <= 355 {
			current = append(current, n)
		}
		end = span[1]
	}
	if len(current) > len(best) {
		best = current
	}
	return best
}

func dot(a, b []float64) float64 {
	sum := 0.0
	for i, v := range a {
		sum += v * b[i]
	}
	return sum
}

func normalize(a []float64) []float64 {
	scale := math.Max(math.Sqrt(dot(a, a)), 1e-12)
	for i := range a {
		a[i] /= scale
	}
	return a
}

func standardize(a []float64) []float64 {
	mean := 0.0
	for _, v := range a {
		mean += v / float64(len(a))
	}
	variance := 0.0
	for _, v := range a {
		variance += (v - mean) * (v - mean) / float64(len(a))
	}
	scale := math.Max(math.Sqrt(variance), 1e-12)
	for i := range a {
		a[i] = (a[i] - mean) / scale
	}
	return a
}

func project(a []float64, basis [][]float64) []float64 {
	for _, b := range basis {
		projection := dot(a, b)
		for i := range a {
			a[i] -= projection * b[i]
		}
	}
	return a
}

func featureScale(a []float64, f traceFeatures) []float64 {
	for i := range a {
		a[i] = (a[i] - f.Mean[i]) / f.Scale[i]
	}
	return a
}

func centroidScores(a []float64, centroids [][]float64) []float64 {
	scores := make([]float64, len(centroids))
	for i, c := range centroids {
		scores[i] = dot(a, c)
	}
	return standardize(scores)
}

func counts(numbers []int) []float64 {
	out := make([]float64, 355)
	for _, n := range numbers {
		out[n-1]++
	}
	return out
}

func smooth(values []float64) []float64 {
	total := float64(len(values)) * 0.5
	for _, n := range values {
		total += n
	}
	for i, n := range values {
		values[i] = math.Sqrt((n + 0.5) / total)
	}
	return values
}

func scores(numbers []int, bank traceBank) []float64 {
	h := bank.Robust.Hellinger
	feature := featureScale(smooth(counts(numbers)), h)
	marginal := centroidScores(normalize(project(feature, h.Basis)), h.Centroids)
	o := bank.Robust.Ordered
	if o.Weight == 0 {
		return marginal
	}
	ordered := make([]float64, 0, 74)
	start := 0
	for i := 0; i < 4; i++ {
		size := len(numbers) / 4
		if i < len(numbers)%4 {
			size++
		}
		bins := make([]float64, 16)
		for _, n := range numbers[start : start+size] {
			bins[min(15, (n-1)*16/355)]++
		}
		ordered = append(ordered, smooth(bins)...)
		start += size
	}
	lastDigits := make([]float64, 10)
	for _, n := range numbers {
		lastDigits[n%10]++
	}
	ordered = append(ordered, smooth(lastDigits)...)
	ordered = featureScale(ordered, o)
	unit := normalize(append([]float64(nil), ordered...))
	template := make([]float64, len(o.Centroids))
	for i := range template {
		template[i] = math.Inf(-1)
		for _, env := range o.Environments {
			template[i] = math.Max(template[i], dot(unit, env[i]))
		}
	}
	standardize(template)
	nuisance := centroidScores(normalize(project(ordered, o.Basis)), o.Centroids)
	for i := range template {
		template[i] = 0.5*template[i] + 0.5*nuisance[i]
	}
	standardize(template)
	for i := range marginal {
		marginal[i] = (1-o.Weight)*marginal[i] + o.Weight*template[i]
	}
	return marginal
}

func similarity(left, right []float64) float64 {
	lt, rt := 0.0, 0.5*355
	for i := range left {
		lt += left[i]
		rt += right[i]
	}
	js := 0.0
	for i := range left {
		p, q := left[i]/lt, (right[i]+0.5)/rt
		mid := (p + q) / 2
		if p > 0 {
			js += p * math.Log(p/mid) / 2
		}
		js += q * math.Log(q/mid) / 2
	}
	return 1 - math.Sqrt(math.Max(0, js)/math.Log(2))
}

// Analyze scores the collected answers against the candidate bank.
// It returns an error only when no answer contained enough numbers.
func Analyze(samples []Sample) (Attribution, error) {
	bank := modelTraceBank
	result := Attribution{}
	score := make([]float64, len(bank.Models))
	pooled := make([]float64, 355)
	for i, sample := range samples {
		numbers := ParseNumbers(sample.Text)
		minimum := minimumNumbers(sample.ExpectedCount)
		valid := len(numbers) >= minimum
		result.Diagnostics = append(result.Diagnostics, Diagnostic{i, len(numbers), minimum, valid})
		if !valid {
			continue
		}
		result.Used++
		for j, value := range scores(numbers, bank) {
			score[j] += value
		}
		for _, n := range numbers {
			pooled[n-1]++
		}
	}
	if result.Used == 0 {
		return result, fmt.Errorf("未采集到有效数字序列")
	}
	beta := bank.Calibration[strconv.Itoa(min(result.Used, 3))].Beta
	maximum := math.Inf(-1)
	for i := range score {
		score[i] /= float64(result.Used)
		maximum = math.Max(maximum, beta*score[i])
	}
	total := 0.0
	probabilities := make([]float64, len(score))
	for i, value := range score {
		probabilities[i] = math.Exp(beta*value - maximum)
		total += probabilities[i]
	}
	families := map[string]int{}
	for i, model := range bank.Models {
		probability := probabilities[i] / total
		result.Results = append(result.Results, Candidate{
			Model: model.ID, DisplayName: model.DisplayName, Family: model.Family, FamilyName: model.FamilyName,
			Probability: probability, Similarity: similarity(pooled, model.Counts), Score: score[i],
		})
		if _, ok := families[model.Family]; !ok {
			families[model.Family] = len(result.Families)
			result.Families = append(result.Families, Family{Family: model.Family, DisplayName: model.FamilyName})
		}
		result.Families[families[model.Family]].Probability += probability
	}
	sort.SliceStable(result.Results, func(i, j int) bool {
		return result.Results[i].Probability > result.Results[j].Probability
	})
	result.Prediction, result.Probability = result.Results[0].Model, result.Results[0].Probability
	for _, family := range result.Families {
		if family.Probability > result.FamilyProbability {
			result.FamilyPrediction, result.FamilyProbability = family.DisplayName, family.Probability
		}
	}
	return result, nil
}

// ClipText bounds one stored answer. The scorer only needs the numbers.
func ClipText(text string) string {
	if len(text) > traceMaxText {
		return text[:traceMaxText]
	}
	return text
}
