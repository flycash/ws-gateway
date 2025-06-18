//nolint:tagliatelle // 忽略
package webhook

import "time"

// GrafanaWebhookPayload 表示 Grafana Webhook 告警的完整载荷
type GrafanaWebhookPayload struct {
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	OrgID             int64             `json:"orgId"`
	Alerts            []Alert           `json:"alerts"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Title             string            `json:"title"`
	State             string            `json:"state"`
	Message           string            `json:"message"`
}

// Alert 表示单个告警项
type Alert struct {
	Status       string             `json:"status"`
	Labels       map[string]string  `json:"labels"`
	Annotations  map[string]string  `json:"annotations"`
	StartsAt     time.Time          `json:"startsAt"`
	EndsAt       time.Time          `json:"endsAt"`
	GeneratorURL string             `json:"generatorURL"`
	Fingerprint  string             `json:"fingerprint"`
	SilenceURL   string             `json:"silenceURL"`
	DashboardURL string             `json:"dashboardURL"`
	PanelURL     string             `json:"panelURL"`
	Values       map[string]float64 `json:"values"`
}

func (a *Alert) AlertName() string {
	return a.Labels["alertname"]
}

func (a *Alert) Description() string {
	return a.Annotations["description"]
}

func (a *Alert) Summary() string {
	return a.Annotations["summary"]
}

func (a *Alert) IsFiring() bool {
	return a.Status == "firing"
}

func (a *Alert) IsResolved() bool {
	return a.Status == "resolved"
}

func (a *Alert) Duration() time.Duration {
	if a.EndsAt.IsZero() {
		// 如果还没结束，计算到现在的持续时间
		return time.Since(a.StartsAt)
	}
	return a.EndsAt.Sub(a.StartsAt)
}
