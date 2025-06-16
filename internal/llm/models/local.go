package models

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/cap-ai/cap/internal/logging"
	"github.com/spf13/viper"
)

const (
	ProviderLocal ModelProvider = "local"

	localModelsPath        = "v1/models"
	lmStudioBetaModelsPath = "api/v0/models"
)

func init() {
	if endpoint := os.Getenv("LOCAL_ENDPOINT"); endpoint != "" {
		localEndpoint, err := url.Parse(endpoint)
		if err != nil {
			logging.Debug("Failed to parse local endpoint",
				"error", err,
				"endpoint", endpoint,
			)
			return
		}

		// 2025.06.15 Kawata added orgEndpoint to fetch details such as context_length from ollama
		load := func(orgEndpoint string, url *url.URL, path string) []localModel {
			url.Path = path
			return listLocalModels(orgEndpoint, url.String())
		}

		// 2025.06.15 Kawata added orgEndpoint to fetch details such as context_length from ollama
		models := load(endpoint, localEndpoint, lmStudioBetaModelsPath)

		if len(models) == 0 {
			// 2025.06.15 Kawata added orgEndpoint to fetch details such as context_length from ollama
			models = load(endpoint, localEndpoint, localModelsPath)
		}

		if len(models) == 0 {
			logging.Debug("No local models found",
				"endpoint", endpoint,
			)
			return
		}

		loadLocalModels(models)

		viper.SetDefault("providers.local.apiKey", "dummy")
		ProviderPopularity[ProviderLocal] = 0
	}
}

// 2025.06.14 Kawata added endpoint for provider
func InitLocal(endpoint string) {
	localEndpoint, err := url.Parse(endpoint)
	if err != nil {
		logging.Debug("Failed to parse local endpoint",
			"error", err,
			"endpoint", endpoint,
		)
		return
	}

	// 2025.06.15 Kawata added orgEndpoint to fetch details such as context_length from ollama
	load := func(orgEndpoint string, url *url.URL, path string) []localModel {
		url.Path = path
		return listLocalModels(orgEndpoint, url.String())
	}

	// 2025.06.15 Kawata added orgEndpoint to fetch details such as context_length from ollama
	models := load(endpoint, localEndpoint, lmStudioBetaModelsPath)

	if len(models) == 0 {
		// 2025.06.15 Kawata added orgEndpoint to fetch details such as context_length from ollama
		models = load(endpoint, localEndpoint, localModelsPath)
	}

	if len(models) == 0 {
		logging.Debug("No local models found",
			"endpoint", endpoint,
		)
		return
	}

	loadLocalModels(models)

	viper.SetDefault("providers.local.apiKey", "dummy")
	ProviderPopularity[ProviderLocal] = 0
}

type localModelList struct {
	Data []localModel `json:"data"`
}

type localModel struct {
	ID                  string `json:"id"`
	Object              string `json:"object"`
	Type                string `json:"type"`
	Publisher           string `json:"publisher"`
	Arch                string `json:"arch"`
	CompatibilityType   string `json:"compatibility_type"`
	Quantization        string `json:"quantization"`
	State               string `json:"state"`
	MaxContextLength    int64  `json:"max_context_length"`
	LoadedContextLength int64  `json:"loaded_context_length"`
}

// 2025.06.15 Kawata added orgEndpoint to fetch details such as context_length from ollama
func listLocalModels(orgEndpoint string, modelsEndpoint string) []localModel {
	res, err := http.Get(modelsEndpoint)
	if err != nil {
		logging.Debug("Failed to list local models",
			"error", err,
			"endpoint", modelsEndpoint,
		)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		logging.Debug("Failed to list local models",
			"status", res.StatusCode,
			"endpoint", modelsEndpoint,
		)
	}

	var modelList localModelList
	if err = json.NewDecoder(res.Body).Decode(&modelList); err != nil {
		logging.Debug("Failed to list local models",
			"error", err,
			"endpoint", modelsEndpoint,
		)
	}

	var supportedModels []localModel
	for _, model := range modelList.Data {
		if strings.HasSuffix(modelsEndpoint, lmStudioBetaModelsPath) {
			if model.Object != "model" || model.Type != "llm" {
				logging.Debug("Skipping unsupported LMStudio model",
					"endpoint", modelsEndpoint,
					"id", model.ID,
					"object", model.Object,
					"type", model.Type,
				)

				continue
			}
		}

		supportedModels = append(supportedModels, model)
	}

	// 2025.06.15 Kawata added orgEndpoint to fetch details such as context_length from ollama
	// 2025.06.15 Kawata added context_length fetching from ollama
	url, err := url.Parse(orgEndpoint)
	if err == nil {
		type ApiShowBody struct {
			ModelInfo map[string]any `json:"model_info"`
		}
		url.Path = "/api/show"
		for i, m := range supportedModels {
			bodyMap := map[string]any{"name": m.ID}
			bodyJson, err := json.Marshal(bodyMap)
			if err != nil {
				logging.Debug(fmt.Sprintf("Failed to build body json for POST %s", url.Path), "error", err, "model", m.ID)
				continue
			}
			// POST request to orgEndpoint + /api/show with JSON like {"name": <model_name>}
			res, err := http.Post(url.String(), "application/json", bytes.NewBuffer(bodyJson))
			if err != nil {
				logging.Debug(fmt.Sprintf("Failed to POST %s", url.Path), "error", err, "model", m.ID)
				continue
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				logging.Debug(fmt.Sprintf("Failed to POST %s with status-code = %d", url.Path, res.StatusCode), "error", err, "model", m.ID)
				continue
			}
			var apiShowBody = ApiShowBody{}
			if err = json.NewDecoder(res.Body).Decode(&apiShowBody); err != nil {
				logging.Debug(fmt.Sprintf("Failed to parse model_info of the model: %s", m.ID), "error", err, "endpoint", modelsEndpoint)
				continue
			}
			var contextLength int64 = 0
			for k, v := range apiShowBody.ModelInfo {
				if strings.Contains(k, "context") && strings.Contains(k, "length") {
					cl, err := anyToInt64(v)
					if err != nil {
						logging.Debug(fmt.Sprintf("Failed to parse %s of the model: %s", k, m.ID), "error", err)
					} else {
						contextLength = cl
					}
					continue
				}
			}
			supportedModels[i].LoadedContextLength = contextLength
		}
	} else {
		logging.Debug("Failed to parse orgEndpoint", "error", err, "orgEndpoint", orgEndpoint)
	}

	return supportedModels
}

// 2025.06.15 Kawata added context_length fetching from ollama
func anyToInt64(a any) (int64, error) {
	switch v := a.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported type: %T", a)
	}
}

func loadLocalModels(models []localModel) {
	for i, m := range models {
		model := convertLocalModel(m)
		SupportedModels[model.ID] = model

		if i == 0 || m.State == "loaded" {
			viper.SetDefault("agents.coder.model", model.ID)
			viper.SetDefault("agents.summarizer.model", model.ID)
			viper.SetDefault("agents.task.model", model.ID)
			viper.SetDefault("agents.title.model", model.ID)
		}
	}
}

func convertLocalModel(model localModel) Model {
	return Model{
		ID:                  ModelID("local." + model.ID),
		Name:                friendlyModelName(model.ID),
		Provider:            ProviderLocal,
		APIModel:            model.ID,
		ContextWindow:       cmp.Or(model.LoadedContextLength, 4096),
		DefaultMaxTokens:    cmp.Or(model.LoadedContextLength, 4096),
		CanReason:           true,
		SupportsAttachments: true,
	}
}

var modelInfoRegex = regexp.MustCompile(`(?i)^([a-z0-9]+)(?:[-_]?([rv]?\d[\.\d]*))?(?:[-_]?([a-z]+))?.*`)

func friendlyModelName(modelID string) string {
	mainID := modelID
	tag := ""

	if slash := strings.LastIndex(mainID, "/"); slash != -1 {
		mainID = mainID[slash+1:]
	}

	if at := strings.Index(modelID, "@"); at != -1 {
		mainID = modelID[:at]
		tag = modelID[at+1:]
	}

	match := modelInfoRegex.FindStringSubmatch(mainID)
	if match == nil {
		return modelID
	}

	capitalize := func(s string) string {
		if s == "" {
			return ""
		}
		runes := []rune(s)
		runes[0] = unicode.ToUpper(runes[0])
		return string(runes)
	}

	family := capitalize(match[1])
	version := ""
	label := ""

	if len(match) > 2 && match[2] != "" {
		version = strings.ToUpper(match[2])
	}

	if len(match) > 3 && match[3] != "" {
		label = capitalize(match[3])
	}

	var parts []string
	if family != "" {
		parts = append(parts, family)
	}
	if version != "" {
		parts = append(parts, version)
	}
	if label != "" {
		parts = append(parts, label)
	}
	if tag != "" {
		parts = append(parts, tag)
	}

	return strings.Join(parts, " ")
}
