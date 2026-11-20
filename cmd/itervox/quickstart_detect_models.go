package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
)

// Detection of model servers on this machine for `itervox quickstart`'s
// local-model step: one GET per candidate port on the loopback address, in
// parallel, each bounded by modelServerDetectTimeout. Read-only: nothing is
// started, configured or written to the servers.

// modelServerCandidate is a port a local model server listens on unless
// told otherwise, and the server that usually does.
type modelServerCandidate struct {
	Port   int
	Server string
}

// quickstartModelServerCandidates are the default ports of common local
// model servers, probed in this order: Ollama (11434), LM Studio's local
// server (1234), llama.cpp's llama-server (8080) and vLLM's OpenAI-compatible
// server (8000). None of them is a default for itervox: detection lists
// what answers and the operator picks. Tests replace the list (TestMain
// empties it so no test probes a real port).
var quickstartModelServerCandidates = []modelServerCandidate{
	{Port: 11434, Server: "Ollama"},
	{Port: 1234, Server: "LM Studio"},
	{Port: 8080, Server: "llama.cpp server"},
	{Port: 8000, Server: "vLLM"},
}

// modelServerDetectTimeout bounds each candidate's probe.
const modelServerDetectTimeout = 1500 * time.Millisecond

// detectedModelServer is a candidate port that answered HTTP.
type detectedModelServer struct {
	Port int
	// URL is http://127.0.0.1:<port>, the server's base URL.
	URL string
	// Kind is the server the answer identifies ("" when it does not);
	// Usual is the server that usually listens on the port.
	Kind, Usual string
	// Models are the first model ids /v1/models lists, if it does.
	Models []string
}

// describe is the server's line in the quickstart list.
func (d detectedModelServer) describe() string {
	what := d.Kind
	if what == "" {
		what = "answers HTTP; the usual port of " + d.Usual
	}
	if len(d.Models) > 0 {
		what += " (models: " + strings.Join(d.Models, ", ") + ")"
	}
	return d.URL + " — " + what
}

// detectModelServers probes every candidate on 127.0.0.1 and returns the
// ones that answered, in candidate order.
func detectModelServers(candidates []modelServerCandidate) []detectedModelServer {
	found := make([]*detectedModelServer, len(candidates))
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		go func(i int, c modelServerCandidate) {
			defer orchestrator.RecoverGoroutine("quickstart-detect-model-server", "", nil)
			defer wg.Done()
			found[i] = probeModelServerCandidate(c)
		}(i, c)
	}
	wg.Wait()
	var out []detectedModelServer
	for _, d := range found {
		if d != nil {
			out = append(out, *d)
		}
	}
	return out
}

// maxModelsListed caps the model ids shown per server.
const maxModelsListed = 3

// probeModelServerCandidate asks the candidate's /v1/models (served by all
// four servers' OpenAI-compatible APIs) and, when the answer does not say
// which server it is, its root, where Ollama answers "Ollama is running".
// nil when nothing answers HTTP on the port.
func probeModelServerCandidate(c modelServerCandidate) *detectedModelServer {
	base := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))
	status, body, err := detectGet(base + "/v1/models")
	if err != nil {
		return nil
	}
	d := &detectedModelServer{Port: c.Port, URL: base, Usual: c.Server}
	if status == http.StatusOK {
		d.Kind, d.Models = parseModelList(body)
	}
	if d.Kind == "" {
		if _, root, rootErr := detectGet(base + "/"); rootErr == nil && strings.Contains(string(root), "Ollama is running") {
			d.Kind = "Ollama"
		}
	}
	return d
}

// detectGet is one bounded GET; the body is read up to 64 KiB.
func detectGet(rawURL string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelServerDetectTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, nil, nil // it answered; the body does not matter
	}
	return resp.StatusCode, body, nil
}

// parseModelList reads an OpenAI-style model list ({"data":[{"id",
// "owned_by"}]}): the first ids, and the server its owned_by names, as these
// servers fill it in ("vllm", "llamacpp", "library" for Ollama,
// "organization_owner" for LM Studio).
func parseModelList(body []byte) (kind string, models []string) {
	var list struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &list) != nil {
		return "", nil
	}
	for _, m := range list.Data {
		if kind == "" {
			switch m.OwnedBy {
			case "vllm":
				kind = "vLLM"
			case "llamacpp":
				kind = "llama.cpp server"
			case "library":
				kind = "Ollama"
			case "organization_owner":
				kind = "LM Studio"
			}
		}
		if id := strings.TrimSpace(m.ID); id != "" && len(models) < maxModelsListed {
			models = append(models, id)
		}
	}
	return kind, models
}

// modelServerFromAnswer turns what the operator typed at the local-model
// prompt into a model_server URL: a number from the detected list, a port
// (on 127.0.0.1), host:port, or an http(s) URL with its port. "" for an
// empty answer. A codex profile's URL from a number or port gets /v1, the
// path Codex's OpenAI provider expects.
func modelServerFromAnswer(answer string, detected []detectedModelServer, backend string) (string, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", nil
	}
	suffix := ""
	if backend == config.BackendCodex {
		suffix = "/v1"
	}
	if n, err := strconv.Atoi(answer); err == nil {
		if n >= 1 && n <= len(detected) {
			return detected[n-1].URL + suffix, nil
		}
		if n < 1 || n > 65535 {
			return "", fmt.Errorf("%d is not a port (1-65535)", n)
		}
		return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(n)) + suffix, nil
	}
	if !strings.Contains(answer, "://") {
		if host, port, err := net.SplitHostPort(answer); err == nil && host != "" && port != "" {
			return "http://" + net.JoinHostPort(host, port) + suffix, nil
		}
		return "", fmt.Errorf("enter a port, host:port, or a URL with its port (http://host:<port>)")
	}
	return answer, nil
}
