package mesh

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ImageGen is the pluggable background-image backend (spec §bg-gen).
type ImageGen interface {
	Generate(prompt string, w, h, seed int) ([]byte, error)
}

// styleSuffix is shared by every agent so the whole set reads as one family.
const styleSuffix = ", isometric, muted palette, dark background, subtle"

const (
	bgWidth  = 1600
	bgHeight = 1000
)

// seedFor derives a stable seed from the agent name ⇒ repeatable regeneration.
func seedFor(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int(h.Sum32() % 1_000_000)
}

// buildBgPrompt constructs the prompt from the manifest for a coherent set.
func buildBgPrompt(spec AgentSpec) string {
	var kw []string
	kw = append(kw, spec.Domains...)
	kw = append(kw, spec.Owns...)
	base := strings.Join(kw, ", ")
	if base == "" {
		base = strings.ToLower(spec.Title)
	}
	return fmt.Sprintf("%s, %s%s", base, spec.Badge.Label, styleSuffix)
}

// generateBackground picks a backend and writes <dir>/.iterm2/bg.png.
func generateBackground(dir string, spec AgentSpec, bg BgOptions) error {
	prompt := bg.Prompt
	if prompt == "" {
		prompt = buildBgPrompt(spec)
	}
	backend := bg.Backend
	if backend == "" {
		backend = firstNonEmpty(os.Getenv("MESH_BG_BACKEND"), "pollinations")
	}

	var gen ImageGen
	switch backend {
	case "mflux":
		gen = &mfluxGen{}
	case "pollinations":
		gen = &pollinationsGen{token: os.Getenv("POLLINATIONS_TOKEN")}
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return fmt.Errorf("openai backend requires OPENAI_API_KEY (a platform API key, not a ChatGPT subscription)")
		}
		gen = &openaiGen{key: key}
	default:
		return fmt.Errorf("unknown bg backend %q (want mflux|pollinations|openai)", backend)
	}

	out := filepath.Join(dir, ".iterm2", "bg.png")

	// mflux writes the file itself (it needs the output path).
	if m, ok := gen.(*mfluxGen); ok {
		m.output = out
		_, err := m.Generate(prompt, bgWidth, bgHeight, seedFor(spec.Name))
		return err
	}

	data, err := gen.Generate(prompt, bgWidth, bgHeight, seedFor(spec.Name))
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}

// --- mflux: free, offline, deterministic on Apple Silicon ---

type mfluxGen struct{ output string }

func (m *mfluxGen) Generate(prompt string, w, h, seed int) ([]byte, error) {
	if _, err := exec.LookPath("mflux-generate"); err != nil {
		return nil, fmt.Errorf("mflux-generate not on PATH: %w", err)
	}
	cmd := exec.Command("mflux-generate",
		"--model", "schnell",
		"--prompt", prompt,
		"--width", strconv.Itoa(w),
		"--height", strconv.Itoa(h),
		"--seed", strconv.Itoa(seed),
		"--steps", "4",
		"--output", m.output,
	)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("mflux-generate: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil, nil
}

// --- pollinations: free, zero-setup, no SLA ---

type pollinationsGen struct{ token string }

func (p *pollinationsGen) Generate(prompt string, w, h, seed int) ([]byte, error) {
	u := fmt.Sprintf("https://image.pollinations.ai/prompt/%s?width=%d&height=%d&seed=%d&nologo=true",
		url.PathEscape(prompt), w, h, seed)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pollinations returned %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// --- openai: paid, house style (gpt-image-1.5) ---

type openaiGen struct{ key string }

func (o *openaiGen) Generate(prompt string, w, h, seed int) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{
		"model":  "gpt-image-1.5",
		"prompt": prompt,
		"size":   "1536x1024",
		"n":      1,
	})
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+o.key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai images returned %s: %s", resp.Status, strings.TrimSpace(string(rb)))
	}
	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 || out.Data[0].B64 == "" {
		return nil, fmt.Errorf("openai images: empty response")
	}
	return base64.StdEncoding.DecodeString(out.Data[0].B64)
}
