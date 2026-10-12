package services

type Readiness struct {
	TCP  string         `json:"tcp,omitempty"`
	HTTP *HTTPReadiness `json:"http,omitempty"`
}

type HTTPReadiness struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}
