package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FlowTemplate is a named, predefined flow definition that can be instantiated
// into a regular Flow. Templates live as JSON files in flowtemplates/ and are
// loaded at startup.
type FlowTemplate struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Trigger     FlowTrigger `json:"trigger"`
	Steps       []FlowStep  `json:"steps"`
}

// TemplateManager owns the loaded set of flow templates. Templates are
// immutable after load — mutating requires restart.
type TemplateManager struct {
	mu        sync.RWMutex
	templates map[string]*FlowTemplate
	dir       string
}

// NewTemplateManager loads all *.json files in dir as templates. Files that
// fail to parse are logged and skipped (one bad template doesn't break the
// others). Returns a manager ready to serve templates.
func NewTemplateManager(dir string) *TemplateManager {
	tm := &TemplateManager{
		templates: make(map[string]*FlowTemplate),
		dir:       dir,
	}
	tm.loadAll()
	return tm
}

func (tm *TemplateManager) loadAll() {
	entries, err := os.ReadDir(tm.dir)
	if err != nil {
		// Directory may not exist on first run; that's OK.
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(tm.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var t FlowTemplate
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		if t.Name == "" {
			t.Name = strings.TrimSuffix(entry.Name(), ".json")
		}
		tm.templates[t.Name] = &t
	}
}

// List returns all loaded templates.
func (tm *TemplateManager) List() []*FlowTemplate {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	out := make([]*FlowTemplate, 0, len(tm.templates))
	for _, t := range tm.templates {
		cp := *t
		out = append(out, &cp)
	}
	return out
}

// Get returns a template by name.
func (tm *TemplateManager) Get(name string) (*FlowTemplate, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	t, ok := tm.templates[name]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// Instantiate creates a new Flow from a template, assigning a fresh name
// (optionally overridden) and operator ID.
func (tm *TemplateManager) Instantiate(name string, fm *FlowManager, operatorID string) (*Flow, error) {
	t, ok := tm.Get(name)
	if !ok {
		return nil, fmt.Errorf("template %q not found", name)
	}
	// Deep-copy the steps so mutations to the instantiated flow don't
	// affect the template.
	stepsCopy := make([]FlowStep, len(t.Steps))
	for i, s := range t.Steps {
		stepsCopy[i] = s
		if s.Params != nil {
			cp := make(json.RawMessage, len(s.Params))
			copy(cp, s.Params)
			stepsCopy[i].Params = cp
		}
	}
	return fm.Create(t.Name+" (from template)", t.Description, t.Trigger, stepsCopy, nil, operatorID)
}
