package demolib

import (
	"encoding/base64"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Resume/job-description matching — deliberately not an LLM call (no API
// key required, deterministic, good enough for a working demo). Same
// request/response contract as Prism's real /resume-screen-accurate
// endpoint: task_description + files[] in, ranked candidates[] out.

type ResumeFile struct {
	Filename      string `json:"filename,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Text          string `json:"text,omitempty"`
}

type ScreenRequest struct {
	TaskDescription string       `json:"task_description,omitempty"`
	Files           []ResumeFile `json:"files"`
}

type Candidate struct {
	Name             string   `json:"name"`
	MatchScore       float64  `json:"match_score"`
	Role             string   `json:"role"`
	KeySkills        []string `json:"key_skills"`
	PrimaryDomain    string   `json:"primary_domain"`
	AlternativeRoles []string `json:"alternative_roles"`
	Reason           string   `json:"reason"`
}

type ScreenResponse struct {
	Candidates []Candidate `json:"candidates"`
	Confidence float64     `json:"confidence"`
	LatencyMs  int64       `json:"latency_ms"`
	Timestamp  string      `json:"timestamp"`
}

var skillTaxonomy = map[string][]string{
	"Frontend Web Development": {"react", "vue", "angular", "typescript", "javascript", "css", "tailwind", "next.js", "nextjs", "html", "redux", "webpack", "vite"},
	"Backend Engineering":      {"node", "nodejs", "go", "golang", "python", "java", "rust", "postgres", "postgresql", "mysql", "redis", "api", "microservices", "grpc"},
	"Data Engineering":         {"spark", "airflow", "kafka", "etl", "sql", "warehouse", "dbt", "hadoop", "snowflake", "bigquery"},
	"DevOps / Infrastructure":  {"kubernetes", "docker", "terraform", "aws", "gcp", "azure", "ci/cd", "jenkins", "ansible", "linux"},
	"Machine Learning":         {"pytorch", "tensorflow", "ml", "nlp", "llm", "scikit-learn", "pandas", "numpy", "model"},
}

// domainOrder keeps taxonomy iteration deterministic (Go map order isn't).
var domainOrder = []string{
	"Frontend Web Development", "Backend Engineering", "Data Engineering",
	"DevOps / Infrastructure", "Machine Learning",
}

var roleByDomain = map[string]string{
	"Frontend Web Development": "Frontend Developer",
	"Backend Engineering":      "Backend Engineer",
	"Data Engineering":         "Data Engineer",
	"DevOps / Infrastructure":  "DevOps Engineer",
	"Machine Learning":         "ML Engineer",
}

var tokenSplit = regexp.MustCompile(`[^a-z0-9./+]+`)

func tokenize(text string) map[string]bool {
	tokens := map[string]bool{}
	for _, t := range tokenSplit.Split(strings.ToLower(text), -1) {
		if len(t) > 1 {
			tokens[t] = true
		}
	}
	return tokens
}

func extractText(f ResumeFile) string {
	if f.Text != "" {
		return f.Text
	}
	if f.ContentBase64 != "" {
		if decoded, err := base64.StdEncoding.DecodeString(f.ContentBase64); err == nil {
			return string(decoded)
		}
	}
	return ""
}

func extractName(f ResumeFile, text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && len(line) < 60 && !strings.ContainsAny(line, "{}<>") {
			return line
		}
	}
	if f.Filename != "" {
		base := f.Filename
		if i := strings.LastIndex(base, "."); i > 0 {
			base = base[:i]
		}
		return strings.NewReplacer("_", " ", "-", " ").Replace(base)
	}
	return "Unknown candidate"
}

type domainScore struct {
	domain string
	skills []string
}

func scoreDomains(tokens map[string]bool) []domainScore {
	scores := make([]domainScore, 0, len(domainOrder))
	for _, domain := range domainOrder {
		var hits []string
		for _, kw := range skillTaxonomy[domain] {
			if tokens[kw] {
				hits = append(hits, kw)
				continue
			}
			for t := range tokens {
				if strings.Contains(t, kw) {
					hits = append(hits, kw)
					break
				}
			}
		}
		scores = append(scores, domainScore{domain: domain, skills: hits})
	}
	sort.SliceStable(scores, func(i, j int) bool { return len(scores[i].skills) > len(scores[j].skills) })
	return scores
}

func ScreenResumes(req ScreenRequest) ScreenResponse {
	start := time.Now()
	taskTokens := tokenize(req.TaskDescription)

	candidates := make([]Candidate, 0, len(req.Files))
	for _, f := range req.Files {
		text := extractText(f)
		resumeTokens := tokenize(text)
		domains := scoreDomains(resumeTokens)

		primaryDomain, keySkills := "General", []string{}
		if len(domains) > 0 {
			primaryDomain, keySkills = domains[0].domain, domains[0].skills
		}
		var alternativeRoles []string
		for _, d := range domains[1:min(3, len(domains))] {
			if len(d.skills) > 0 {
				if role, ok := roleByDomain[d.domain]; ok {
					alternativeRoles = append(alternativeRoles, role)
				} else {
					alternativeRoles = append(alternativeRoles, d.domain)
				}
			}
		}

		overlap := 0
		for t := range taskTokens {
			if resumeTokens[t] {
				overlap++
			}
		}
		matchScore := 0.5
		if len(taskTokens) > 0 {
			denom := float64(len(taskTokens)) * 0.4
			if denom < 1 {
				denom = 1
			}
			matchScore = float64(overlap) / denom
			if matchScore > 1 {
				matchScore = 1
			}
		}

		role, ok := roleByDomain[primaryDomain]
		if !ok {
			role = "Generalist"
		}
		reason := "No strong domain keywords detected — treat this score as low-confidence."
		if len(keySkills) > 0 {
			reason = "Keyword overlap found matching skills in " + primaryDomain + " and shared terms with the task description."
		}
		if len(keySkills) > 6 {
			keySkills = keySkills[:6]
		}

		candidates = append(candidates, Candidate{
			Name:             extractName(f, text),
			MatchScore:       round2(matchScore),
			Role:             role,
			KeySkills:        keySkills,
			PrimaryDomain:    primaryDomain,
			AlternativeRoles: alternativeRoles,
			Reason:           reason,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].MatchScore > candidates[j].MatchScore })

	var confidence float64
	if len(candidates) > 0 {
		var sum float64
		for _, c := range candidates {
			sum += c.MatchScore
		}
		confidence = round2(sum / float64(len(candidates)))
	}

	return ScreenResponse{
		Candidates: candidates,
		Confidence: confidence,
		LatencyMs:  time.Since(start).Milliseconds(),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
