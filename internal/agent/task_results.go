package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type cachedTaskOutcome struct {
	Status       string         `json:"status"`
	Result       map[string]any `json:"result,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	Retryable    bool           `json:"retryable,omitempty"`
	CompletedAt  int64          `json:"completedAt"`
}

type taskResultStore struct {
	path string
	mu   sync.Mutex
}

func newTaskResultStore(cfg Config) *taskResultStore {
	root := strings.TrimSpace(cfg.Workspace)
	if root == "" {
		root = filepath.Dir(cfg.StatePath)
	}
	if root == "" {
		root = "."
	}
	return &taskResultStore{path: filepath.Join(root, ".monkeys", "task-results.json")}
}

func (s *taskResultStore) get(taskID string) (cachedTaskOutcome, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return cachedTaskOutcome{}, false, err
	}
	value, ok := values[taskID]
	return value, ok, nil
}

func (s *taskResultStore) put(taskID string, value cachedTaskOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return err
	}
	values[taskID] = value
	pruneTaskOutcomes(values)
	return writeJSONAtomic(s.path, values)
}

func (s *taskResultStore) remove(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return err
	}
	delete(values, taskID)
	return writeJSONAtomic(s.path, values)
}

func (s *taskResultStore) load() (map[string]cachedTaskOutcome, error) {
	values := map[string]cachedTaskOutcome{}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func pruneTaskOutcomes(values map[string]cachedTaskOutcome) {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	for taskID, value := range values {
		if value.CompletedAt < cutoff {
			delete(values, taskID)
		}
	}
	if len(values) <= 2000 {
		return
	}
	for len(values) > 2000 {
		oldestID := ""
		oldestAt := int64(^uint64(0) >> 1)
		for taskID, value := range values {
			if value.CompletedAt < oldestAt {
				oldestID, oldestAt = taskID, value.CompletedAt
			}
		}
		delete(values, oldestID)
	}
}
