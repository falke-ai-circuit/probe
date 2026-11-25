package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Profile type
// ---------------------------------------------------------------------------

// Profile is a reusable build configuration template. It has the same fields
// as BuildConfig except it omits ID, Token, Status, and lifecycle fields —
// those are assigned when a build is created from a profile.
type Profile struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	OS           string          `json:"os"`
	Arch         string          `json:"arch"`
	Capabilities []string        `json:"capabilities"`
	ServerURL    string          `json:"server_url"`
	Permissions  string          `json:"permissions"`
	SandboxDir   string          `json:"sandbox_dir,omitempty"`
	Disguise     *DisguiseConfig `json:"disguise,omitempty"`
	Autostart    bool            `json:"autostart"`
	BackoffMin   string          `json:"backoff_min,omitempty"`
	BackoffMax   string          `json:"backoff_max,omitempty"`
	MaxRetries   int             `json:"max_retries,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

// ---------------------------------------------------------------------------
// ProfileManager
// ---------------------------------------------------------------------------

// ProfileManager manages build profiles and persists them to profiles.json.
// It follows the same load/save pattern as OperatorManager and Registry.
type ProfileManager struct {
	mu       sync.RWMutex
	profiles map[string]*Profile // ID -> Profile
	savePath string
}

// NewProfileManager creates a new ProfileManager. If savePath is non-empty,
// existing profiles are loaded from disk.
func NewProfileManager(savePath string) *ProfileManager {
	pm := &ProfileManager{
		profiles: make(map[string]*Profile),
		savePath:  savePath,
	}
	pm.load()
	return pm
}

// Create adds a new profile. An ID is generated and assigned. Returns the
// created profile or an error if validation fails.
func (pm *ProfileManager) Create(p *Profile) (*Profile, error) {
	if err := pm.validate(p); err != nil {
		return nil, err
	}

	p.ID = generateProfileID()
	p.CreatedAt = time.Now().UTC()

	pm.mu.Lock()
	pm.profiles[p.ID] = p
	pm.save()
	pm.mu.Unlock()

	return p, nil
}

// Get returns the profile by ID, or nil if not found.
func (pm *ProfileManager) Get(id string) *Profile {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if p, ok := pm.profiles[id]; ok {
		snap := *p
		return &snap
	}
	return nil
}

// List returns all profiles.
func (pm *ProfileManager) List() []Profile {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make([]Profile, 0, len(pm.profiles))
	for _, p := range pm.profiles {
		result = append(result, *p)
	}
	return result
}

// Delete removes a profile by ID. Returns true if the profile existed.
func (pm *ProfileManager) Delete(id string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, ok := pm.profiles[id]; !ok {
		return false
	}
	delete(pm.profiles, id)
	pm.save()
	return true
}

// ToBuildConfig converts a profile into a BuildConfig by adding the required
// token and generating a new build ID (assigned by BuilderManager.CreateBuild).
func (p *Profile) ToBuildConfig(token string) *BuildConfig {
	return &BuildConfig{
		Name:         p.Name,
		OS:           p.OS,
		Arch:         p.Arch,
		Capabilities: p.Capabilities,
		ServerURL:    p.ServerURL,
		Token:        token,
		Permissions:  p.Permissions,
		SandboxDir:   p.SandboxDir,
		Disguise:     p.Disguise,
		Autostart:    p.Autostart,
		BackoffMin:   p.BackoffMin,
		BackoffMax:   p.BackoffMax,
		MaxRetries:   p.MaxRetries,
		Status:       BuildStatusPending,
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func (pm *ProfileManager) validate(p *Profile) error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !validBuildOS[p.OS] {
		return fmt.Errorf("invalid os %q: must be windows, linux, or darwin", p.OS)
	}
	if !validBuildArch[p.Arch] {
		return fmt.Errorf("invalid arch %q: must be amd64, 386, or arm64", p.Arch)
	}
	if p.ServerURL == "" {
		return fmt.Errorf("server_url is required")
	}
	if p.Permissions == "" {
		p.Permissions = "full"
	}
	if !isValidPermission(p.Permissions) {
		return fmt.Errorf("invalid permissions %q: must be read-only, standard, sandboxed, or full", p.Permissions)
	}
	for _, cap := range p.Capabilities {
		if !validCapabilities[cap] {
			return fmt.Errorf("invalid capability %q", cap)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func (pm *ProfileManager) save() {
	if pm.savePath == "" {
		return
	}
	ensureDir(pm.savePath)
	data, err := json.MarshalIndent(pm.profiles, "", "  ")
	if err != nil {
		log.Printf("[profiles] save marshal error: %v", err)
		return
	}
	if err := writeFileAtomic(pm.savePath, data, 0644); err != nil {
		log.Printf("[profiles] save write error: %v", err)
	}
}

func (pm *ProfileManager) load() {
	if pm.savePath == "" {
		return
	}
	data, err := os.ReadFile(pm.savePath)
	if err != nil {
		return
	}
	var profiles map[string]*Profile
	if err := json.Unmarshal(data, &profiles); err != nil {
		log.Printf("[profiles] load unmarshal error: %v", err)
		return
	}
	pm.profiles = profiles
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func generateProfileID() string {
	return "profile-" + generateUUID()
}