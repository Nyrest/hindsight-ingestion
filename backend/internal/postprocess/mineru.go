package postprocess

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (c extractionClient) minerU(data []byte, name string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("cannot parse an empty file")
	}
	if c.step.APIMode == "cloud" {
		return c.minerUCloud(data, name)
	}
	return c.minerUSelfHosted(data, name)
}

type minerUTask struct {
	ID     string `json:"task_id"`
	Status string `json:"status"`
	Files  []struct {
		ID        string `json:"file_id"`
		Name      string `json:"file_name"`
		Status    string `json:"status"`
		ResultURL string `json:"result_url"`
		Upload    *struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"upload"`
	} `json:"files"`
}

func (c extractionClient) minerUSelfHosted(data []byte, name string) (string, error) {
	endpoint := strings.TrimRight(c.step.URL, "/") + "/v1/tasks"
	payload := map[string]any{
		"files":          []any{map[string]any{"file_name": name, "bytes": len(data), "sha256sum": fmt.Sprintf("%x", sha256.Sum256(data)), "page_range": ""}},
		"output_formats": []string{"default"},
	}
	var task minerUTask
	if err := c.jsonRequest("POST", endpoint, payload, &task); err != nil {
		return "", err
	}
	if task.ID == "" {
		return "", fmt.Errorf("missing task_id")
	}
	taskID, fileID := task.ID, ""
	uploaded := false
	for {
		if task.ID != taskID || len(task.Files) != 1 || task.Files[0].ID == "" || task.Files[0].Name != name {
			return "", fmt.Errorf("inconsistent task response")
		}
		file := task.Files[0]
		if fileID != "" && fileID != file.ID {
			return "", fmt.Errorf("task file identity changed")
		}
		fileID = file.ID
		switch task.Status {
		case "completed":
			if file.Status != "completed" {
				return "", fmt.Errorf("task file did not complete")
			}
			return c.downloadMarkdown(file.ResultURL)
		case "failed", "canceled", "partial":
			return "", fmt.Errorf("task %s ended with status %s", taskID, task.Status)
		case "waiting-file", "pending", "running":
		default:
			return "", fmt.Errorf("unknown task status %q", task.Status)
		}
		if !uploaded && file.Status == "waiting-file" && file.Upload != nil {
			headers := make(http.Header)
			for key, value := range file.Upload.Headers {
				headers.Set(key, value)
			}
			if _, err := c.storageRequest("PUT", file.Upload.URL, data, headers); err != nil {
				return "", err
			}
			uploaded = true
		}
		if err := waitForExtraction(c.vm.ctx); err != nil {
			return "", err
		}
		task = minerUTask{}
		if err := c.jsonRequest("GET", endpoint+"/"+url.PathEscape(taskID), nil, &task); err != nil {
			return "", err
		}
	}
}

type minerUCloudResponse struct {
	Code *int `json:"code"`
	Data struct {
		BatchID  string   `json:"batch_id"`
		FileURLs []string `json:"file_urls"`
		Results  []struct {
			Name   string `json:"file_name"`
			State  string `json:"state"`
			ZIPURL string `json:"full_zip_url"`
		} `json:"extract_result"`
	} `json:"data"`
}

func (c extractionClient) cloudRequest(method, endpoint string, payload any) (minerUCloudResponse, error) {
	var response minerUCloudResponse
	err := c.jsonRequest(method, endpoint, payload, &response)
	if err == nil && (response.Code == nil || *response.Code != 0) {
		err = fmt.Errorf("MinerU cloud returned an unsuccessful or missing API code")
	}
	return response, err
}

func (c extractionClient) minerUCloud(data []byte, name string) (string, error) {
	base := strings.TrimRight(c.step.URL, "/")
	payload := map[string]any{"files": []any{map[string]string{"name": name}}}
	created, err := c.cloudRequest("POST", base+"/api/v4/file-urls/batch", payload)
	if err != nil {
		return "", err
	}
	if created.Data.BatchID == "" || len(created.Data.FileURLs) != 1 {
		return "", fmt.Errorf("missing batch ID or upload URL")
	}
	if _, err := c.storageRequest("PUT", created.Data.FileURLs[0], data, nil); err != nil {
		return "", err
	}
	for {
		result, err := c.cloudRequest("GET", base+"/api/v4/extract-results/batch/"+url.PathEscape(created.Data.BatchID), nil)
		if err != nil {
			return "", err
		}
		if result.Data.BatchID != created.Data.BatchID || len(result.Data.Results) != 1 || result.Data.Results[0].Name != name {
			return "", fmt.Errorf("inconsistent cloud batch response")
		}
		file := result.Data.Results[0]
		switch file.State {
		case "done":
			return c.downloadMarkdown(file.ZIPURL)
		case "failed":
			return "", fmt.Errorf("cloud task failed")
		case "waiting-file", "pending", "running", "converting":
		default:
			return "", fmt.Errorf("unknown cloud task state %q", file.State)
		}
		if err := waitForExtraction(c.vm.ctx); err != nil {
			return "", err
		}
	}
}

func (c extractionClient) downloadMarkdown(reference string) (string, error) {
	data, err := c.storageRequest("GET", reference, nil, nil)
	if err != nil {
		return "", err
	}
	return markdownFromZIP(data)
}
