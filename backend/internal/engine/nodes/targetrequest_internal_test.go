package nodes

import (
	"encoding/base64"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/agentmesh/backend/internal/models"
)

func TestBuildTargetRequestGETUsesQueryString(t *testing.T) {
	node := models.WorkflowNode{
		Endpoint:      "https://api.example/opportunities",
		ParamDefaults: map[string]string{"address": "ABC123", "limit": ""},
	}
	got, body, ct := buildTargetRequest(node, http.MethodGet)
	if body != nil || ct != "" {
		t.Fatalf("GET must not carry a body, got body=%q ct=%q", body, ct)
	}
	u, err := url.Parse(got.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("address") != "ABC123" {
		t.Errorf("want address in query, got %q", got.Endpoint)
	}
	// An empty value is "not configured", not "send an empty string" — a
	// target distinguishing absent from blank would otherwise get the wrong one.
	if _, present := u.Query()["limit"]; present {
		t.Errorf("empty param must be omitted, got %q", got.Endpoint)
	}
}

func TestBuildTargetRequestNeverOverwritesExistingQueryValue(t *testing.T) {
	// executeFunctionCall appends an agent's LLM-chosen args to the URL before
	// this runs. Those are a per-call decision and must beat static config.
	node := models.WorkflowNode{
		Endpoint:      "https://api.example/x?address=FROM_AGENT",
		ParamDefaults: map[string]string{"address": "FROM_CONFIG"},
	}
	got, _, _ := buildTargetRequest(node, http.MethodGet)
	u, _ := url.Parse(got.Endpoint)
	if v := u.Query().Get("address"); v != "FROM_AGENT" {
		t.Errorf("agent-supplied value must win, got %q", v)
	}
}

func TestBuildTargetRequestPOSTUsesJSONBody(t *testing.T) {
	node := models.WorkflowNode{
		Endpoint:     "https://api.example/resume",
		CustomParams: []models.CustomParam{{Name: "resume", Kind: "text", Value: "8 yrs backend"}},
	}
	got, body, ct := buildTargetRequest(node, http.MethodPost)
	if got.Endpoint != node.Endpoint {
		t.Errorf("POST must not rewrite the URL, got %q", got.Endpoint)
	}
	if ct != "application/json" {
		t.Errorf("want application/json, got %q", ct)
	}
	if !strings.Contains(string(body), `"resume":"8 yrs backend"`) {
		t.Errorf("want resume in JSON body, got %s", body)
	}
}

func TestBuildTargetRequestCustomParamBeatsDiscoveredDefault(t *testing.T) {
	node := models.WorkflowNode{
		Endpoint:      "https://api.example/x",
		ParamDefaults: map[string]string{"address": "DISCOVERED"},
		CustomParams:  []models.CustomParam{{Name: "address", Kind: "text", Value: "TYPED_BY_USER"}},
	}
	got, _, _ := buildTargetRequest(node, http.MethodGet)
	u, _ := url.Parse(got.Endpoint)
	if v := u.Query().Get("address"); v != "TYPED_BY_USER" {
		t.Errorf("hand-typed value must win over a discovered default, got %q", v)
	}
}

func TestBuildTargetRequestFileProducesParsableMultipart(t *testing.T) {
	const fileContent = "%PDF-1.4 fake resume bytes"
	node := models.WorkflowNode{
		Endpoint: "https://api.example/resume-screen",
		CustomParams: []models.CustomParam{
			{Name: "role", Kind: "text", Value: "backend"},
			{
				Name:     "resume",
				Kind:     "file",
				Value:    base64.StdEncoding.EncodeToString([]byte(fileContent)),
				FileName: "cv.pdf",
				MIMEType: "application/pdf",
			},
		},
	}
	_, body, ct := buildTargetRequest(node, http.MethodPost)
	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Fatalf("want multipart content type, got %q", ct)
	}

	// Parse it back exactly as a receiving server would: a body the target
	// can't parse is the whole failure mode this encoding exists to avoid.
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatalf("content type not parseable: %v", err)
	}
	boundary, ok := params["boundary"]
	if !ok {
		t.Fatal("content type carries no boundary — body would be unparseable")
	}
	mr := multipart.NewReader(strings.NewReader(string(body)), boundary)
	form, err := mr.ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("multipart body did not parse: %v", err)
	}
	if got := form.Value["role"]; len(got) != 1 || got[0] != "backend" {
		t.Errorf("text field lost in multipart encoding: %v", got)
	}
	files := form.File["resume"]
	if len(files) != 1 {
		t.Fatalf("want 1 file part, got %d", len(files))
	}
	if files[0].Filename != "cv.pdf" {
		t.Errorf("want filename cv.pdf, got %q", files[0].Filename)
	}
	f, err := files[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, len(fileContent))
	if _, err := f.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != fileContent {
		t.Errorf("file bytes corrupted: got %q", buf)
	}
}

func TestBuildTargetRequestRejectsOversizeFile(t *testing.T) {
	// The frontend caps this too, but that check is bypassable — a workflow
	// can be PUT straight at the API.
	huge := base64.StdEncoding.EncodeToString(make([]byte, maxParamFileBytes+1))
	node := models.WorkflowNode{
		Endpoint:     "https://api.example/x",
		CustomParams: []models.CustomParam{{Name: "f", Kind: "file", Value: huge}},
	}
	_, body, ct := buildTargetRequest(node, http.MethodPost)
	if body != nil || ct != "" {
		t.Errorf("oversize file must yield no body rather than a partial one, got ct=%q len=%d", ct, len(body))
	}
}

func TestBuildTargetRequestFileValueNeverLeaksIntoQueryString(t *testing.T) {
	// A file's Value is base64 bytes. Treating it as a text param would paste
	// the whole encoded blob into the URL.
	node := models.WorkflowNode{
		Endpoint:      "https://api.example/x",
		ParamDefaults: map[string]string{"doc": "should-be-dropped"},
		CustomParams: []models.CustomParam{
			{Name: "doc", Kind: "file", Value: base64.StdEncoding.EncodeToString([]byte("bytes"))},
		},
	}
	got, body, ct := buildTargetRequest(node, http.MethodPost)
	if strings.Contains(got.Endpoint, "doc=") {
		t.Errorf("file param leaked into URL: %q", got.Endpoint)
	}
	if !strings.HasPrefix(ct, "multipart/") {
		t.Fatalf("want multipart, got %q", ct)
	}
	if strings.Contains(string(body), "should-be-dropped") {
		t.Error("discovered default was sent as a text field for a file-kind param")
	}
}

func TestBuildTargetRequestNoParamsIsUnchanged(t *testing.T) {
	node := models.WorkflowNode{Endpoint: "https://api.example/x"}
	got, body, ct := buildTargetRequest(node, http.MethodGet)
	if got.Endpoint != node.Endpoint || body != nil || ct != "" {
		t.Errorf("unconfigured node must be untouched, got %q %q %q", got.Endpoint, body, ct)
	}
}
