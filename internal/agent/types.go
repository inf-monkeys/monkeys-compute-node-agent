package agent

import "time"

type Config struct {
	ServerURL      string
	BootstrapToken string
	StatePath      string
	NodeName       string
	Version        string
	Interval       time.Duration
	AllowInstall   bool
	DryRun         bool
}

type State struct {
	ServerURL  string `json:"serverUrl"`
	NodeID     string `json:"nodeId"`
	NodeName   string `json:"nodeName"`
	AgentToken string `json:"agentToken"`
	UpdatedAt  int64  `json:"updatedAt"`
}

type NodeInfo struct {
	ID             string            `json:"id"`
	ClusterID      *string           `json:"clusterId"`
	Name           string            `json:"name"`
	Role           string            `json:"role"`
	ConnectionMode string            `json:"connectionMode"`
	Status         string            `json:"status"`
	Hostname       *string           `json:"hostname"`
	PublicIP       *string           `json:"publicIp"`
	PrivateIP      *string           `json:"privateIp"`
	OS             *string           `json:"os"`
	Arch           *string           `json:"arch"`
	CPUCoreCount   *int              `json:"cpuCores"`
	MemoryBytes    *string           `json:"memoryBytes"`
	GPUs           []GPUInfo         `json:"gpus"`
	AgentVersion   *string           `json:"agentVersion"`
	Labels         map[string]string `json:"labels"`
}

type RegisterRequest struct {
	BootstrapToken string            `json:"bootstrapToken"`
	Name           string            `json:"name,omitempty"`
	Hostname       string            `json:"hostname,omitempty"`
	PublicIP       string            `json:"publicIp,omitempty"`
	PrivateIP      string            `json:"privateIp,omitempty"`
	OS             string            `json:"os,omitempty"`
	Arch           string            `json:"arch,omitempty"`
	CPUCoreCount   int               `json:"cpuCores,omitempty"`
	MemoryBytes    string            `json:"memoryBytes,omitempty"`
	AgentVersion   string            `json:"agentVersion,omitempty"`
	GPUs           []GPUInfo         `json:"gpus,omitempty"`
	Kubernetes     map[string]any    `json:"kubernetes,omitempty"`
	HAMi           map[string]any    `json:"hami,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

type HeartbeatRequest struct {
	Hostname     string            `json:"hostname,omitempty"`
	PublicIP     string            `json:"publicIp,omitempty"`
	PrivateIP    string            `json:"privateIp,omitempty"`
	OS           string            `json:"os,omitempty"`
	Arch         string            `json:"arch,omitempty"`
	CPUCoreCount int               `json:"cpuCores,omitempty"`
	MemoryBytes  string            `json:"memoryBytes,omitempty"`
	AgentVersion string            `json:"agentVersion,omitempty"`
	GPUs         []GPUInfo         `json:"gpus,omitempty"`
	Kubernetes   map[string]any    `json:"kubernetes,omitempty"`
	HAMi         map[string]any    `json:"hami,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Status       string            `json:"status,omitempty"`
	StatusReason string            `json:"statusReason,omitempty"`
}

type RegisterResult struct {
	Node       NodeInfo `json:"node"`
	AgentToken string   `json:"agentToken"`
	Plan       Plan     `json:"plan"`
}

type HeartbeatResult struct {
	Node NodeInfo `json:"node"`
	Plan Plan     `json:"plan"`
}

type Plan struct {
	ID             string         `json:"id"`
	NodeID         string         `json:"nodeId"`
	ClusterID      *string        `json:"clusterId"`
	Status         string         `json:"status"`
	ConnectionMode string         `json:"connectionMode"`
	Role           string         `json:"role"`
	Actions        []PlanAction   `json:"actions"`
	K3s            map[string]any `json:"k3s"`
	HAMi           map[string]any `json:"hami"`
	UpdatedAt      int64          `json:"updatedAt"`
}

type PlanAction struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

type EventRequest struct {
	EventType  string         `json:"eventType"`
	Severity   string         `json:"severity,omitempty"`
	Message    string         `json:"message,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	OccurredAt int64          `json:"occurredAt,omitempty"`
}

type Facts struct {
	Hostname     string
	PublicIP     string
	PrivateIP    string
	OS           string
	Arch         string
	CPUCoreCount int
	MemoryBytes  string
	GPUs         []GPUInfo
	Kubernetes   map[string]any
	HAMi         map[string]any
	Labels       map[string]string
}

type GPUInfo struct {
	Vendor        string `json:"vendor"`
	Model         string `json:"model,omitempty"`
	Count         int    `json:"count"`
	MemoryMiB     int    `json:"memoryMiB,omitempty"`
	DriverVersion string `json:"driverVersion,omitempty"`
	CUDAVersion   string `json:"cudaVersion,omitempty"`
}

type ActionResult struct {
	ActionType string         `json:"actionType"`
	Success    bool           `json:"success"`
	Planned    bool           `json:"planned,omitempty"`
	DryRun     bool           `json:"dryRun,omitempty"`
	Message    string         `json:"message,omitempty"`
	Command    []string       `json:"command,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}
