package agent

import "time"

type Config struct {
	ServerURL       string
	BootstrapToken  string
	AgentToken      string
	StatePath       string
	NodeName        string
	Mode            string
	Workspace       string
	TargetID        string
	AgentInstanceID string
	Version         string
	Interval        time.Duration
	AllowInstall    bool
	DryRun          bool
}

type State struct {
	ServerURL       string `json:"serverUrl"`
	NodeID          string `json:"nodeId"`
	NodeName        string `json:"nodeName"`
	AgentToken      string `json:"agentToken"`
	Mode            string `json:"mode,omitempty"`
	Workspace       string `json:"workspace,omitempty"`
	TargetID        string `json:"targetId,omitempty"`
	AgentInstanceID string `json:"agentInstanceId,omitempty"`
	UpdatedAt       int64  `json:"updatedAt"`
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
	BootstrapToken  string            `json:"bootstrapToken"`
	TargetKind      string            `json:"targetKind,omitempty"`
	AgentMode       string            `json:"agentMode,omitempty"`
	TargetID        string            `json:"targetId,omitempty"`
	AgentInstanceID string            `json:"agentInstanceId,omitempty"`
	Name            string            `json:"name,omitempty"`
	Hostname        string            `json:"hostname,omitempty"`
	PublicIP        string            `json:"publicIp,omitempty"`
	PrivateIP       string            `json:"privateIp,omitempty"`
	OS              string            `json:"os,omitempty"`
	Arch            string            `json:"arch,omitempty"`
	CPUCoreCount    int               `json:"cpuCores,omitempty"`
	MemoryBytes     string            `json:"memoryBytes,omitempty"`
	AgentVersion    string            `json:"agentVersion,omitempty"`
	GPUs            []GPUInfo         `json:"gpus,omitempty"`
	Kubernetes      map[string]any    `json:"kubernetes,omitempty"`
	HAMi            map[string]any    `json:"hami,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Capabilities    map[string]any    `json:"capabilities,omitempty"`
	Telemetry       map[string]any    `json:"telemetry,omitempty"`
}

type HeartbeatRequest struct {
	TargetKind      string            `json:"targetKind,omitempty"`
	AgentMode       string            `json:"agentMode,omitempty"`
	Hostname        string            `json:"hostname,omitempty"`
	PublicIP        string            `json:"publicIp,omitempty"`
	PrivateIP       string            `json:"privateIp,omitempty"`
	OS              string            `json:"os,omitempty"`
	Arch            string            `json:"arch,omitempty"`
	CPUCoreCount    int               `json:"cpuCores,omitempty"`
	MemoryBytes     string            `json:"memoryBytes,omitempty"`
	AgentVersion    string            `json:"agentVersion,omitempty"`
	GPUs            []GPUInfo         `json:"gpus,omitempty"`
	Kubernetes      map[string]any    `json:"kubernetes,omitempty"`
	HAMi            map[string]any    `json:"hami,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Telemetry       map[string]any    `json:"telemetry,omitempty"`
	Capabilities    map[string]any    `json:"capabilities,omitempty"`
	RuntimeStatuses []RuntimeStatus   `json:"runtimeStatuses,omitempty"`
	Status          string            `json:"status,omitempty"`
	StatusReason    string            `json:"statusReason,omitempty"`
}

type ClaimedTask struct {
	ID               string         `json:"id"`
	TargetID         string         `json:"targetId"`
	ClusterID        *string        `json:"clusterId"`
	RuntimeID        *string        `json:"runtimeId"`
	TaskType         string         `json:"taskType"`
	Status           string         `json:"status"`
	Payload          map[string]any `json:"payload"`
	AttemptCount     int            `json:"attemptCount"`
	MaxAttempts      int            `json:"maxAttempts"`
	LeaseOwner       string         `json:"leaseOwner"`
	LeaseExpiresAt   *int64         `json:"leaseExpiresAt"`
	CreatedTimestamp int64          `json:"createdTimestamp"`
}

type ClaimTasksResult struct {
	Items []ClaimedTask `json:"items"`
}

type TaskLeaseRequest struct {
	LeaseOwner   string `json:"leaseOwner"`
	LeaseSeconds int    `json:"leaseSeconds,omitempty"`
}

type CompleteTaskRequest struct {
	LeaseOwner string         `json:"leaseOwner"`
	Result     map[string]any `json:"result,omitempty"`
}

type FailTaskRequest struct {
	LeaseOwner        string         `json:"leaseOwner"`
	ErrorMessage      string         `json:"errorMessage"`
	Result            map[string]any `json:"result,omitempty"`
	Retryable         bool           `json:"retryable"`
	RetryDelaySeconds int            `json:"retryDelaySeconds,omitempty"`
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
	UpdatedAt      UnixMillis     `json:"updatedAt"`
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
	Hostname        string
	PublicIP        string
	PrivateIP       string
	OS              string
	Arch            string
	CPUCoreCount    int
	MemoryBytes     string
	GPUs            []GPUInfo
	Kubernetes      map[string]any
	HAMi            map[string]any
	Labels          map[string]string
	Telemetry       map[string]any
	RuntimeStatuses []RuntimeStatus
}

type RuntimeStatus struct {
	RuntimeID         string           `json:"runtimeId"`
	Namespace         string           `json:"namespace,omitempty"`
	DeploymentName    string           `json:"deploymentName,omitempty"`
	Status            string           `json:"status,omitempty"`
	Reason            string           `json:"reason,omitempty"`
	Message           string           `json:"message,omitempty"`
	DesiredReplicas   int              `json:"desiredReplicas"`
	ReadyReplicas     int              `json:"readyReplicas"`
	AvailableReplicas int              `json:"availableReplicas"`
	PodCount          int              `json:"podCount"`
	ReadyPodCount     int              `json:"readyPodCount"`
	Pods              []RuntimePodInfo `json:"pods,omitempty"`
	ObservedAt        int64            `json:"observedAt"`
}

type RuntimePodInfo struct {
	Name    string `json:"name"`
	Phase   string `json:"phase,omitempty"`
	Ready   bool   `json:"ready"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type GPUInfo struct {
	Vendor             string   `json:"vendor"`
	Index              *int     `json:"index,omitempty"`
	UUID               string   `json:"uuid,omitempty"`
	Model              string   `json:"model,omitempty"`
	Count              int      `json:"count"`
	MemoryMiB          int      `json:"memoryMiB,omitempty"`
	MemoryUsedMiB      *int     `json:"memoryUsedMiB,omitempty"`
	UtilizationPercent *float64 `json:"utilizationPercent,omitempty"`
	TemperatureC       *float64 `json:"temperatureC,omitempty"`
	PowerWatts         *float64 `json:"powerWatts,omitempty"`
	DriverVersion      string   `json:"driverVersion,omitempty"`
	CUDAVersion        string   `json:"cudaVersion,omitempty"`
}

type ActionResult struct {
	ActionType string         `json:"actionType"`
	Success    bool           `json:"success"`
	Planned    bool           `json:"planned,omitempty"`
	DryRun     bool           `json:"dryRun,omitempty"`
	Message    string         `json:"message,omitempty"`
	Command    []string       `json:"command,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
	Artifacts  map[string]any `json:"-"`
}
